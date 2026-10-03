package mimori

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Query struct {
	Version  int    `json:"version"`
	Health   bool   `json:"health,omitempty"`
	Revision string `json:"revision,omitempty"`
	Provider string `json:"provider,omitempty"`
	Session  string `json:"session_id,omitempty"`
}

// CollectorDiagnostic is the latest historical collection error, not a health
// verdict. It is held only for this daemon lifetime and never stores payloads.
type CollectorDiagnostic struct {
	Message    string `json:"message"`
	ObservedAt string `json:"observed_at"`
}

type Response struct {
	LatestCollectorDiagnostic *CollectorDiagnostic `json:"latest_collector_diagnostic,omitempty"`
	Version                   int                  `json:"version"`
	Revision                  string               `json:"revision"`
	Unchanged                 bool                 `json:"unchanged,omitempty"`
	Roots                     []Session            `json:"roots,omitempty"`
	Unclassified              []Session            `json:"unclassified,omitempty"`
	Session                   *Session             `json:"session,omitempty"`
	Error                     string               `json:"error,omitempty"`
	ErrorCode                 string               `json:"error_code,omitempty"`
	Complete                  bool                 `json:"complete,omitempty"`
}
type Snapshot struct {
	Revision string
	Sessions []Session
}

// Validators bind the published revision to the exact query shape.
func scopedRevision(base string, q Query) string {
	scope, _ := json.Marshal([]string{q.Provider, q.Session})
	sum := sha256.Sum256(scope)
	return fmt.Sprintf("%s:%x", base, sum[:8])
}
func (s Snapshot) Query(q Query) Response {
	r := Response{Version: 1, Revision: scopedRevision(s.Revision, q)}
	if q.Version != 1 {
		r.Error = "unsupported API version"
		r.ErrorCode = "unsupported_version"
		return r
	}
	if q.Session != "" && q.Provider == "" {
		r.Error = "provider required for session detail"
		r.ErrorCode = "invalid_query"
		return r
	}
	if q.Session != "" {
		for _, v := range s.Sessions {
			if v.Provider == q.Provider && v.ID == q.Session {
				v := v
				r.Session = &v
				break
			}
		}
		if r.Session == nil {
			r.Error = "session not found"
			r.ErrorCode = "not_found"
			return r
		}
	}
	if q.Revision == r.Revision {
		r.Unchanged = true
		r.Session = nil
		return r
	}
	r.Complete = true
	if r.Session != nil {
		return r
	}
	for _, v := range s.Sessions {
		if q.Provider != "" && q.Provider != v.Provider {
			continue
		}
		if v.Classification != "resolved" {
			r.Unclassified = append(r.Unclassified, v)
		} else if v.ID == v.Root {
			r.Roots = append(r.Roots, v)
		}
	}
	return r
}

const MaxResponse = 4 * 1024 * 1024

var errResponseLimit = errors.New("response exceeds 4 MiB; narrow provider filter or request a session")

type responseBuffer struct{ bytes.Buffer }

func (b *responseBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > MaxResponse {
		return 0, errResponseLimit
	}
	return b.Buffer.Write(p)
}

// Build a complete bounded frame before publishing any bytes, never a partial list.
func writeResponse(w io.Writer, r Response) error {
	var b responseBuffer
	if err := json.NewEncoder(&b).Encode(r); err != nil {
		return json.NewEncoder(w).Encode(Response{Version: 1, Revision: r.Revision, Error: err.Error(), ErrorCode: "response_too_large"})
	}
	_, err := w.Write(b.Bytes())
	return err
}
func Serve(ctx context.Context, dir string, diagnose func(string)) error {
	return ServeReady(ctx, dir, diagnose, func() {})
}

// ServeReady notifies only after storage and the query endpoint are ready.
func ServeReady(ctx context.Context, dir string, diagnose func(string), ready func()) error {
	if err := EnsureDir(dir); err != nil {
		return err
	}
	l, err := lock(dir, "daemon.lock", false)
	if err != nil {
		return err
	}
	defer unlock(l)
	store, err := OpenStore(dir)
	if err != nil {
		return err
	}
	defer store.Close()
	var mu sync.RWMutex
	var diagnostic *CollectorDiagnostic
	report := func(message string) {
		if len(message) > 4096 {
			message = message[:4096]
		}
		message = strings.ToValidUTF8(message, "")
		mu.Lock()
		diagnostic = &CollectorDiagnostic{Message: message, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		mu.Unlock()
		diagnose(message)
	}
	store.Drain(dir, report)
	epoch := NewID()
	snap := Snapshot{revision(epoch, len(store.events)), Reduce(store.events)}
	path := filepath.Join(dir, "query.sock")
	if st, err := os.Lstat(path); err == nil {
		if st.Mode()&os.ModeSocket == 0 {
			return errors.New("query.sock exists and is not a socket")
		}
		// Refuse to unlink any live endpoint, including a foreign/incompatible
		// service that does not participate in our advisory lock.
		c, dialErr := net.DialTimeout("unix", path, time.Second)
		if dialErr == nil {
			c.Close()
			return errors.New("query.sock has a live listener")
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) && !errors.Is(dialErr, syscall.ENOENT) {
			return fmt.Errorf("check existing socket: %w", dialErr)
		}
		if err = os.Remove(path); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(path)
	if err = os.Chmod(path, 0600); err != nil {
		return err
	}
	ready()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				listener.Close()
				return
			case <-ticker.C:
				if store.Drain(dir, report) {
					next := Snapshot{revision(epoch, len(store.events)), Reduce(store.events)}
					mu.Lock()
					snap = next
					mu.Unlock()
				}
			}
		}
	}()
	var wg sync.WaitGroup
	defer func() { cancel(); <-done; wg.Wait() }()
	slots := make(chan struct{}, 32)
	for {
		c, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case slots <- struct{}{}:
		default:
			c.Close()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			defer c.Close()
			c.SetDeadline(time.Now().Add(2 * time.Second))
			var q Query
			if err := json.NewDecoder(io.LimitReader(c, 8192)).Decode(&q); err != nil {
				writeResponse(c, Response{Version: 1, Error: "invalid query", ErrorCode: "invalid_query"})
				return
			}
			mu.RLock()
			var r Response
			if q.Health && q.Version == 1 {
				r = Response{Version: 1, Revision: scopedRevision(snap.Revision, q), Complete: true}
			} else {
				r = snap.Query(q)
			}
			r.LatestCollectorDiagnostic = diagnostic
			mu.RUnlock()
			writeResponse(c, r)
		}()
	}
}
func Fetch(dir string, q Query) (Response, error) {
	var r Response
	c, err := net.DialTimeout("unix", filepath.Join(dir, "query.sock"), time.Second)
	if err != nil {
		return r, err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	if err = json.NewEncoder(c).Encode(q); err != nil {
		return r, err
	}
	err = json.NewDecoder(io.LimitReader(c, MaxResponse+1)).Decode(&r)
	if err == nil && r.Error != "" {
		err = fmt.Errorf("query: %s", r.Error)
	}
	return r, err
}

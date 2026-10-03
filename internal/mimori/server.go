package mimori

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Query struct {
	Version  int    `json:"version"`
	Revision string `json:"revision,omitempty"`
	Provider string `json:"provider,omitempty"`
	Session  string `json:"session_id,omitempty"`
}
type Response struct {
	Version      int       `json:"version"`
	Revision     string    `json:"revision"`
	Unchanged    bool      `json:"unchanged,omitempty"`
	Roots        []Session `json:"roots,omitempty"`
	Unclassified []Session `json:"unclassified,omitempty"`
	Session      *Session  `json:"session,omitempty"`
	Error        string    `json:"error,omitempty"`
}
type Snapshot struct {
	Revision string
	Sessions []Session
}

func (s Snapshot) Query(q Query) Response {
	r := Response{Version: 1, Revision: s.Revision}
	if q.Version != 1 {
		r.Error = "unsupported API version"
		return r
	}
	if q.Session != "" && q.Provider == "" {
		r.Error = "provider required for session detail"
		return r
	}
	if q.Revision == s.Revision {
		r.Unchanged = true
		return r
	}
	for _, v := range s.Sessions {
		if q.Provider != "" && q.Provider != v.Provider {
			continue
		}
		if q.Session != "" {
			if v.ID == q.Session {
				v := v
				r.Session = &v
			}
			continue
		}
		if v.Classification != "resolved" {
			r.Unclassified = append(r.Unclassified, v)
		} else if v.ID == v.Root {
			r.Roots = append(r.Roots, v)
		}
	}
	if q.Session != "" && r.Session == nil {
		r.Error = "session not found"
	}
	return r
}
func Serve(ctx context.Context, dir string, diagnose func(string)) error {
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
	store.Drain(dir, diagnose)
	epoch := NewID()
	snap := Snapshot{revision(epoch, len(store.events)), Reduce(store.events)}
	var mu sync.RWMutex
	path := filepath.Join(dir, "query.sock")
	if st, err := os.Lstat(path); err == nil {
		if st.Mode()&os.ModeSocket == 0 {
			return errors.New("query.sock exists and is not a socket")
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
				if store.Drain(dir, diagnose) {
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
				json.NewEncoder(c).Encode(Response{Version: 1, Error: "invalid query"})
				return
			}
			mu.RLock()
			r := snap.Query(q)
			mu.RUnlock()
			json.NewEncoder(c).Encode(r)
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
	err = json.NewDecoder(io.LimitReader(c, 64*1024*1024)).Decode(&r)
	if err == nil && r.Error != "" {
		err = fmt.Errorf("query: %s", r.Error)
	}
	return r, err
}

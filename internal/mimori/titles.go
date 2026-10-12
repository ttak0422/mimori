package mimori

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const titleInterval = 30 * time.Second
const titleTTL = 5 * time.Minute
const titleBatch = 16
const titleCacheLimit = 256

type titleKey struct {
	Provider, ID, ThreadID, SessionID string
	Generation                        uint64
}

func sessionTitleKey(s Session) titleKey {
	root := s.ID
	if s.Parent != "" {
		root = s.Parent
	}
	return titleKey{s.Provider, s.ID, s.ThreadID, root, s.Generation}
}

type cachedTitle struct {
	value string
	at    time.Time
}
type titleFetcher func(context.Context, string, []Session) (map[titleKey]string, error)

// One cache per mimori daemon, shared by all querying editors. No provider
// process is launched; disabled/unreachable endpoints preserve the old labels.
type titleCache struct {
	mu      sync.Mutex
	socket  string
	entries map[titleKey]cachedTitle
	next    time.Time
	cursor  int
	busy    bool
	status  string
	fetch   titleFetcher
	wg      sync.WaitGroup
}

func newTitleCache(socket string) *titleCache {
	return &titleCache{socket: socket, entries: map[titleKey]cachedTitle{}, status: "disabled", fetch: fetchCodexTitles}
}
func (c *titleCache) close() { c.wg.Wait() }

// view is nonblocking. Refreshes only start on view demand and are bounded by
// batch size, time, response bytes, cache size, and a daemon-wide cooldown.
func (c *titleCache) view(ctx context.Context, s Snapshot, now time.Time) (Snapshot, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.socket == "" {
		return s, "disabled"
	}
	if !filepath.IsAbs(c.socket) {
		return s, "unavailable"
	}
	visible := s.visibleAt(now)
	candidates := []Session{}
	known := map[titleKey]bool{}
	for _, row := range visible.Sessions {
		if row.Provider == "codex" && row.ThreadID != "" {
			candidates = append(candidates, row)
			known[sessionTitleKey(row)] = true
		}
	}
	for k, v := range c.entries {
		if !known[k] || !now.Before(v.at.Add(titleTTL)) {
			delete(c.entries, k)
		}
	}
	// Spend a bounded batch on live summaries first, retaining a round-robin
	// cursor so larger sets do not permanently starve later identities.
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].State != "ended" && candidates[j].State == "ended" })
	if !c.busy && !now.Before(c.next) && len(candidates) > 0 && ctx.Err() == nil {
		batch := make([]Session, 0, titleBatch)
		for i := 0; i < len(candidates) && i < titleBatch; i++ {
			batch = append(batch, candidates[(c.cursor+i)%len(candidates)])
		}
		c.cursor = (c.cursor + len(batch)) % len(candidates)
		c.next = now.Add(titleInterval)
		c.busy = true
		c.status = "refreshing"
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			work, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			results, err := c.fetch(work, c.socket, batch)
			c.mu.Lock()
			defer c.mu.Unlock()
			c.busy = false
			if err != nil {
				c.entries = map[titleKey]cachedTitle{}
				c.status = "unavailable"
				return
			}
			c.status = "ready"
			for _, row := range batch {
				k := sessionTitleKey(row)
				delete(c.entries, k)
				if value, ok := results[k]; ok {
					c.entries[k] = cachedTitle{value, time.Now()}
				}
			}
			for len(c.entries) > titleCacheLimit {
				var oldest titleKey
				var at time.Time
				for k, v := range c.entries {
					if at.IsZero() || v.at.Before(at) {
						oldest, at = k, v.at
					}
				}
				delete(c.entries, oldest)
			}
		}()
	}
	out := Snapshot{Revision: s.Revision, Sessions: append([]Session(nil), s.Sessions...)}
	h := sha256.New()
	for i := range out.Sessions {
		row := &out.Sessions[i]
		if value, ok := c.entries[sessionTitleKey(*row)]; ok {
			row.Title = value.value
			row.TitleSource = ""
			if row.Title != "" {
				row.TitleSource = "codex_app_server"
			}
			// Encode arrays to avoid delimiter ambiguity in user-controlled titles.
			_ = json.NewEncoder(h).Encode([]any{row.Provider, row.ID, row.Generation, row.Title})
		}
	}
	out.Revision = fmt.Sprintf("%s:titles:%x", s.Revision, h.Sum(nil))
	return out, c.status
}

var errTitleMetadata = errors.New("Codex title metadata unavailable")
var errTitleRPC = errors.New("Codex metadata request rejected")

// Codex 0.160 exposes a WebSocket over its local Unix control socket. Only
// initialize + thread/read are used; never list/resume/subscribe/turn/start.
func fetchCodexTitles(ctx context.Context, socket string, rows []Session) (map[titleKey]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	d := websocket.Dialer{HandshakeTimeout: time.Second, Proxy: nil, NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	conn, resp, err := d.DialContext(ctx, "ws://localhost/", http.Header{})
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	if err != nil {
		return nil, errTitleMetadata
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	_ = conn.SetReadDeadline(deadline)
	_ = conn.SetWriteDeadline(deadline)
	conn.SetReadLimit(256 * 1024)
	remaining := 2 * 1024 * 1024
	frames := 64
	rpc := func(id int, method string, params any) (json.RawMessage, error) {
		if err := conn.WriteJSON(map[string]any{"id": id, "method": method, "params": params}); err != nil {
			return nil, errTitleMetadata
		}
		for frames > 0 && remaining > 0 {
			frames--
			_, b, err := conn.ReadMessage()
			if err != nil {
				return nil, errTitleMetadata
			}
			remaining -= len(b)
			if remaining < 0 {
				return nil, errTitleMetadata
			}
			var envelope struct {
				ID     int             `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
				Method string          `json:"method"`
			}
			if json.Unmarshal(b, &envelope) != nil {
				return nil, errTitleMetadata
			}
			if envelope.Method != "" {
				continue
			}
			if envelope.ID != id {
				return nil, errTitleMetadata
			}
			if len(envelope.Error) > 0 && string(envelope.Error) != "null" {
				return nil, errTitleRPC
			}
			if len(envelope.Result) == 0 {
				return nil, errTitleMetadata
			}
			return envelope.Result, nil
		}
		return nil, errTitleMetadata
	}
	if _, err := rpc(1, "initialize", map[string]any{"clientInfo": map[string]string{"name": "mimori", "version": "0.1.0"}, "capabilities": map[string]bool{"experimentalApi": false}}); err != nil {
		return nil, errTitleMetadata
	}
	if err := conn.WriteJSON(map[string]any{"method": "initialized"}); err != nil {
		return nil, errTitleMetadata
	}
	results := map[titleKey]string{}
	for i, row := range rows {
		b, err := rpc(i+2, "thread/read", map[string]any{"threadId": row.ThreadID, "includeTurns": false})
		if errors.Is(err, errTitleRPC) {
			continue
		}
		if err != nil {
			return nil, errTitleMetadata
		}
		var r struct {
			Thread struct {
				ID        string            `json:"id"`
				SessionID string            `json:"sessionId"`
				Name      json.RawMessage   `json:"name"`
				Turns     []json.RawMessage `json:"turns"`
			} `json:"thread"`
		}
		if json.Unmarshal(b, &r) != nil {
			continue
		}
		sessionID := row.ID
		if row.Parent != "" {
			sessionID = row.Parent
		}
		// session_id is shared by a Codex tree. Only the explicit concrete thread
		// candidate plus its returned session identity establish this association.
		if r.Thread.ID != row.ThreadID || r.Thread.SessionID != sessionID || len(r.Thread.Turns) != 0 || len(r.Thread.Name) == 0 {
			continue
		}
		var name *string
		if json.Unmarshal(r.Thread.Name, &name) != nil {
			continue
		}
		value := ""
		if name != nil {
			value = strings.TrimSpace(*name)
		}
		if len(value) > 4096 || strings.ContainsRune(value, 0) {
			continue
		}
		results[sessionTitleKey(row)] = value
	}
	if len(rows) > 0 && len(results) == 0 {
		return nil, errTitleMetadata
	}
	return results, nil
}

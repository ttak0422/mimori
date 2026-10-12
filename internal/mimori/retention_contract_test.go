package mimori

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOldDaemonCannotSilentlyServeUnboundedHistory(t *testing.T) {
	for _, unchanged := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "unchanged"}[unchanged], func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "mm-contract-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			path := filepath.Join(dir, "query.sock")
			listener, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				for {
					c, err := listener.Accept()
					if err != nil {
						return
					}
					c.SetDeadline(time.Now().Add(time.Second))
					var q Query
					if json.NewDecoder(c).Decode(&q) == nil {
						r := Response{Version: 1, Revision: "old", Complete: true}
						if !q.Health && unchanged {
							r.Complete = false
							r.Unchanged = true
						}
						json.NewEncoder(c).Encode(r)
					}
					c.Close()
				}
			}()
			defer func() { listener.Close(); <-done }()
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			// A readiness failure must not spawn this executable or replace the endpoint.
			err = Ensure(ctx, dir, "/nonexistent-mimori")
			if err == nil || !strings.Contains(err.Error(), "restart your known mimori daemon") {
				t.Fatal(err)
			}
			_, err = Fetch(dir, Query{Version: 1, Revision: "old"})
			if err == nil || !strings.Contains(err.Error(), "ended-session retention") {
				t.Fatal(err)
			}
			after, err := os.Lstat(path)
			if err != nil || !os.SameFile(before, after) {
				t.Fatal("replaced live endpoint", err)
			}
		})
	}
}

func TestRetentionPolicyOnCompleteAndUnchangedResponses(t *testing.T) {
	s := Snapshot{Revision: "epoch"}
	r := s.Query(Query{Version: 1})
	if err := requireRetention(r); err != nil {
		t.Fatal(err)
	}
	r = s.Query(Query{Version: 1, Revision: r.Revision})
	if !r.Unchanged {
		t.Fatal(r)
	}
	if err := requireRetention(r); err != nil {
		t.Fatal(err)
	}
	r.EndedRetentionSeconds++
	if requireRetention(r) == nil {
		t.Fatal("different retention policy accepted")
	}
}

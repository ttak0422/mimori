package mimori

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

type faultFile struct {
	*os.File
	stage string
}

func (f faultFile) Write(b []byte) (int, error) {
	if f.stage == "write" {
		return 0, syscall.ENOSPC
	}
	return f.File.Write(b)
}
func (f faultFile) Sync() error {
	if f.stage == "sync" {
		return syscall.ENOSPC
	}
	return f.File.Sync()
}
func TestSpoolDiskFullBeforePublish(t *testing.T) {
	for _, stage := range []string{"create", "write", "sync"} {
		t.Run(stage, func(t *testing.T) {
			d := tempState(t)
			spool := filepath.Join(d, "spool")
			err := publishSpool(spool, []byte("event"), func(d, p string) (durableFile, error) {
				if stage == "create" {
					return nil, syscall.ENOSPC
				}
				f, e := os.CreateTemp(d, p)
				return faultFile{f, stage}, e
			})
			if !errors.Is(err, syscall.ENOSPC) {
				t.Fatal(err)
			}
			entries, _ := os.ReadDir(spool)
			if len(entries) != 0 {
				t.Fatal("failed event published or temp leaked", entries)
			}
		})
	}
}
func TestSQLiteFullKeepsSpoolForRetry(t *testing.T) {
	d := tempState(t)
	s, err := OpenStore(d)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var pages int
	if err = s.db.QueryRow("PRAGMA page_count").Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("PRAGMA max_page_count = " + strconv.Itoa(pages)); err != nil {
		t.Fatal(err)
	}
	e := event("full", "s", "idle", 1)
	e.Name = strings.Repeat("n", 4096)
	e.CWD = strings.Repeat("d", 4096)
	if err = Enqueue(d, e); err != nil {
		t.Fatal(err)
	}
	var diagnostics []string
	if s.Drain(d, func(s string) { diagnostics = append(diagnostics, s) }) {
		t.Fatal("uncommitted change published")
	}
	if len(s.events) != 0 || !strings.Contains(strings.ToLower(strings.Join(diagnostics, " ")), "full") {
		t.Fatal(diagnostics, len(s.events))
	}
	files, _ := os.ReadDir(filepath.Join(d, "spool"))
	if len(files) != 1 {
		t.Fatal("lost retry event")
	}
	if _, err = s.db.Exec("PRAGMA max_page_count = 100000"); err != nil {
		t.Fatal(err)
	}
	if !s.Drain(d, func(s string) { t.Log(s) }) || len(s.events) != 1 {
		t.Fatal("failed retry")
	}
}
func TestCrashAfterCommitBeforeAcknowledge(t *testing.T) {
	d := tempState(t)
	e := event("e", "s", "running", 1)
	if err := Enqueue(d, e); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(e); err != nil {
		t.Fatal(err)
	}
	s.Close() // No spool acknowledgement, as at a crash boundary.
	s, err = OpenStore(d)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Drain(d, func(s string) { t.Log(s) }) {
		t.Fatal("replay advanced state")
	}
	if len(s.events) != 1 {
		t.Fatal("duplicate commit")
	}
	files, _ := os.ReadDir(filepath.Join(d, "spool"))
	if len(files) != 0 {
		t.Fatal("replay not acknowledged")
	}
}
func TestSpoolPermissionFailureAndBoundedLock(t *testing.T) {
	d := tempState(t)
	spool := filepath.Join(d, "spool")
	os.Chmod(spool, 0500)
	defer os.Chmod(spool, 0700)
	err := Enqueue(d, event("e", "s", "idle", 1))
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("want permission error: %v", err)
	}
	os.Chmod(spool, 0700)
	l, err := lock(d, "ingest.lock", false)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock(l)
	before := time.Now()
	err = Enqueue(d, event("e", "s", "idle", 1))
	if err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatal(err)
	}
	if time.Since(before) > 1500*time.Millisecond {
		t.Fatal("unbounded hook lock wait")
	}
}

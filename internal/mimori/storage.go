package mimori

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const MaxPayload = 64 * 1024
const MaxSpool = 4096
const MaxEvents = 100000

func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func EnsureDir(dir string) error {
	if !filepath.IsAbs(dir) {
		return errors.New("state directory must be absolute")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	for _, d := range []string{dir, filepath.Join(dir, "spool"), filepath.Join(dir, "quarantine")} {
		if err := os.MkdirAll(d, 0700); err != nil {
			return err
		}
		st, err := os.Lstat(d)
		if err != nil {
			return err
		}
		if !st.IsDir() || st.Mode().Perm()&0077 != 0 {
			return fmt.Errorf("state directory must be private (0700): %s", d)
		}
	}
	return nil
}
func lock(dir, name string, wait bool) (*os.File, error) {
	p := filepath.Join(dir, name)
	fd, err := syscall.Open(p, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), p)
	deadline := time.Now().Add(time.Second)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !wait || time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("%s locked: %w", name, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func unlock(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }
func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func Enqueue(dir string, e Event) error {
	if err := e.Validate(); err != nil {
		return err
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if len(b) > MaxPayload {
		return errors.New("payload exceeds limit")
	}
	if err = EnsureDir(dir); err != nil {
		return err
	}
	l, err := lock(dir, "ingest.lock", true)
	if err != nil {
		return err
	}
	defer unlock(l)
	spool := filepath.Join(dir, "spool")
	entries, err := os.ReadDir(spool)
	if err != nil {
		return err
	}
	if len(entries) >= MaxSpool {
		return errors.New("spool full (4096 files); start daemon or inspect quarantine")
	}
	f, err := os.CreateTemp(spool, ".pending-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, filepath.Join(spool, NewID()+".json")); err != nil {
		return err
	}
	return syncDir(spool)
}
func readBounded(path string) ([]byte, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxPayload+1))
	if len(b) > MaxPayload {
		return nil, errors.New("payload too large")
	}
	return b, err
}

var ErrEventConflict = errors.New("event conflict")

type Store struct {
	db     *sql.DB
	events []Event
	ids    map[string]string
}

func OpenStore(dir string) (*Store, error) {
	p := filepath.Join(dir, "state.db")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err == nil {
		f.Close()
	} else if !os.IsExist(err) {
		return nil, err
	}
	st, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return nil, errors.New("database must be a private regular file")
	}
	db, err := sql.Open("sqlite", p)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	fail := func(e error) (*Store, error) { db.Close(); return nil, e }
	var schema int
	if err = db.QueryRow("PRAGMA user_version").Scan(&schema); err != nil {
		return fail(err)
	}
	if schema != 0 && schema != 1 {
		return fail(errors.New("unsupported database schema"))
	}
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA busy_timeout=1000; CREATE TABLE IF NOT EXISTS events (id TEXT PRIMARY KEY, payload TEXT NOT NULL); PRAGMA user_version=1;`); err != nil {
		return fail(err)
	}
	rows, err := db.Query("SELECT id,payload FROM events ORDER BY rowid")
	if err != nil {
		return fail(err)
	}
	defer rows.Close()
	s := &Store{db: db, ids: map[string]string{}}
	for rows.Next() {
		var id, b string
		if err = rows.Scan(&id, &b); err != nil {
			return fail(err)
		}
		e, err := DecodeEvent([]byte(b))
		if err != nil {
			return fail(errors.New("invalid persisted event"))
		}
		s.events = append(s.events, e)
		s.ids[id] = b
	}
	if err = rows.Err(); err != nil {
		return fail(err)
	}
	return s, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Apply(e Event) (bool, error) {
	b, _ := json.Marshal(e)
	id := key(e.Provider, e.ID)
	if old, ok := s.ids[id]; ok {
		if old != string(b) {
			return false, fmt.Errorf("%w: event ID reused with different payload", ErrEventConflict)
		}
		return false, nil
	}
	if len(s.events) >= MaxEvents {
		return false, errors.New("event retention capacity reached; archive state offline")
	}
	if e.Seq > 0 {
		for _, old := range s.events {
			if old.Provider == e.Provider && old.Session == e.Session && old.Generation == e.Generation && old.Seq == e.Seq {
				return false, fmt.Errorf("%w: sequence reused within generation", ErrEventConflict)
			}
		}
	}
	if _, err := s.db.Exec("INSERT INTO events(id,payload) VALUES (?,?)", id, string(b)); err != nil {
		return false, err
	}
	s.events = append(s.events, e)
	s.ids[id] = string(b)
	return true, nil
}

// Drain acknowledges only committed records. Invalid files are isolated; database
// failures remain queued for retry. Never log raw payloads.
func (s *Store) Drain(dir string, diagnose func(string)) bool {
	spool := filepath.Join(dir, "spool")
	entries, err := os.ReadDir(spool)
	if err != nil {
		diagnose(err.Error())
		return false
	}
	changed := false
	n := 0
	for _, f := range entries {
		if !strings.HasSuffix(f.Name(), ".json") {
			continue
		}
		if n >= 128 {
			break
		}
		n++
		p := filepath.Join(spool, f.Name())
		b, err := readBounded(p)
		var e Event
		if err == nil {
			e, err = DecodeEvent(b)
		}
		if err != nil {
			diagnose("invalid spool event: " + err.Error())
			q, _ := os.ReadDir(filepath.Join(dir, "quarantine"))
			if len(q) >= MaxSpool {
				diagnose("quarantine full; collection paused")
				break
			}
			if err = os.Rename(p, filepath.Join(dir, "quarantine", f.Name())); err != nil {
				diagnose(err.Error())
			}
			continue
		}
		applied, err := s.Apply(e)
		if err != nil {
			diagnose(err.Error())
			if errors.Is(err, ErrEventConflict) {
				q, _ := os.ReadDir(filepath.Join(dir, "quarantine"))
				if len(q) >= MaxSpool {
					break
				}
				if moveErr := os.Rename(p, filepath.Join(dir, "quarantine", f.Name())); moveErr != nil {
					diagnose(moveErr.Error())
				}
			}
			continue
		}
		changed = changed || applied
		if err = os.Remove(p); err != nil {
			diagnose(err.Error())
		}
	}
	return changed
}

package mimori

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// Probe only accepts a complete v1 response. A connected but unresponsive or
// incompatible endpoint is never treated as an absent daemon.
func probe(ctx context.Context, dir string) (bool, error) {
	path := filepath.Join(dir, "query.sock")
	if st, err := os.Lstat(path); err == nil {
		if st.Mode()&os.ModeSocket == 0 {
			return false, errors.New("query.sock exists and is not a socket")
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}
	c, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) {
			return false, nil
		}
		return false, fmt.Errorf("readiness connection: %w", err)
	}
	defer c.Close()
	deadline := time.Now().Add(time.Second)
	if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	c.SetDeadline(deadline)
	if err := json.NewEncoder(c).Encode(Query{Version: 1, Health: true}); err != nil {
		return false, fmt.Errorf("live endpoint readiness: %w", err)
	}
	var r Response
	if err := json.NewDecoder(io.LimitReader(c, MaxResponse+1)).Decode(&r); err != nil {
		return false, fmt.Errorf("live endpoint readiness: %w", err)
	}
	if r.Version != 1 || !r.Complete || r.Revision == "" || r.Unchanged || r.Error != "" || r.ErrorCode != "" {
		return false, errors.New("live endpoint has an incompatible readiness contract")
	}
	return true, nil
}

// Ensure starts the same executable in a new session, with no inherited terminal
// or standard streams. The startup lock is inherited by the daemon, so a caller
// dying between spawn and readiness cannot trigger a second startup. No PID file
// is used as proof of ownership or health.
func Ensure(ctx context.Context, dir, executable string) error {
	if err := EnsureDir(dir); err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("readiness deadline: %w", err)
		}
		ready, err := probe(ctx, dir)
		if err != nil {
			return err
		}
		if ready {
			return nil
		}
		startup, err := lock(dir, "ensure.lock", false)
		if err != nil {
			if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
				return err
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("readiness deadline: %w", ctx.Err())
			case <-time.After(25 * time.Millisecond):
			}
			continue
		}
		// Closing without LOCK_UN is intentional: the child's inherited descriptor
		// keeps the same advisory lock even if this CLI exits or is killed.
		err = startAndWait(ctx, dir, executable, startup)
		startup.Close()
		return err
	}
}

func startAndWait(ctx context.Context, dir, executable string, startup *os.File) error {
	if ready, err := probe(ctx, dir); ready || err != nil {
		return err
	}
	// A foreground daemon may be starting before its socket has been published.
	// Let it finish instead of launching a competitor.
	for {
		daemon, err := lock(dir, "daemon.lock", false)
		if err == nil {
			unlock(daemon)
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return err
		}
		if ready, err := probe(ctx, dir); ready || err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("readiness deadline: %w", ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
	read, write, err := os.Pipe()
	if err != nil {
		return err
	}
	defer read.Close()
	defer write.Close()
	cmd := exec.Command(executable, "daemon", "--state-dir", dir, "--ready-fd", "4")
	cmd.Dir = "/"
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.ExtraFiles = []*os.File{startup, write}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}
	write.Close()
	// Reap if startup fails while this CLI is alive. Successful daemon ownership
	// belongs to the OS after this CLI exits; cancellation never kills the daemon.
	go func() { _ = cmd.Wait() }()
	result := make(chan error, 1)
	go func() {
		b, err := io.ReadAll(io.LimitReader(read, 4097))
		if err == nil && string(b) != "ready\n" {
			if len(b) == 0 {
				err = errors.New("daemon exited before readiness")
			} else {
				err = fmt.Errorf("daemon startup: %.4096s", b)
			}
		}
		result <- err
	}()
	select {
	case <-ctx.Done():
		return fmt.Errorf("readiness deadline: %w", ctx.Err())
	case err := <-result:
		if err != nil {
			return err
		}
	}
	ready, err := probe(ctx, dir)
	if err != nil {
		return err
	}
	if !ready {
		return errors.New("daemon disappeared after readiness")
	}
	return nil
}

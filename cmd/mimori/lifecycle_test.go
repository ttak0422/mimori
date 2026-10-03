package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// The test executable also acts as the real CLI child, without a second build.
// Only the child daemon entry point is intercepted; production never reads these
// environment variables. A delay makes caller death before readiness deterministic.
func TestMain(m *testing.M) {
	if os.Getenv("MIMORI_LIFECYCLE_TEST") == "1" && len(os.Args) > 1 && os.Args[1] == "daemon" {
		f, err := os.OpenFile(os.Getenv("MIMORI_TEST_PIDS"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
		if err != nil {
			os.Exit(98)
		}
		fmt.Fprintln(f, os.Getpid())
		f.Close()
		time.Sleep(250 * time.Millisecond)
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func lifecycleState(t *testing.T) (string, string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "mml-")
	if err != nil {
		t.Fatal(err)
	}
	pids := filepath.Join(dir, "test-pids")
	t.Setenv("MIMORI_LIFECYCLE_TEST", "1")
	t.Setenv("MIMORI_TEST_PIDS", pids)
	t.Cleanup(func() {
		for _, pid := range readPIDs(pids) {
			_ = syscall.Kill(pid, syscall.SIGTERM)
		}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(filepath.Join(dir, "query.sock")); os.IsNotExist(err) {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		os.RemoveAll(dir)
	})
	return dir, pids
}
func readPIDs(path string) []int {
	b, _ := os.ReadFile(path)
	var out []int
	for _, s := range strings.Fields(string(b)) {
		n, _ := strconv.Atoi(s)
		if n > 1 {
			out = append(out, n)
		}
	}
	return out
}
func cliJSON(t *testing.T, dir string, args ...string) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	argv := append([]string{args[0], "--state-dir", dir}, args[1:]...)
	b, err := helper(t, ctx, argv...).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v: %s", args, err, b)
	}
	var r map[string]any
	if err = json.Unmarshal(b, &r); err != nil {
		t.Fatal(err, string(b))
	}
	return r
}
func TestEnsureConcurrentReuseAndRestart(t *testing.T) {
	dir, pids := lifecycleState(t)
	// Queries are read-only and do not even create a state directory.
	absent := filepath.Join(dir, "absent")
	if err := helper(t, context.Background(), "query", "--state-dir", absent).Run(); err == nil {
		t.Fatal("query unexpectedly succeeded")
	}
	if _, err := os.Stat(absent); !os.IsNotExist(err) {
		t.Fatal("query created state")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := cliJSON(t, dir, "ensure")
			if r["ready"] != true {
				t.Error(r)
			}
		}()
	}
	wg.Wait()
	ids := readPIDs(pids)
	if len(ids) != 1 {
		t.Fatalf("started %v", ids)
	}
	before := cliJSON(t, dir, "query")["revision"]
	cliJSON(t, dir, "ensure")
	if after := cliJSON(t, dir, "query")["revision"]; after != before {
		t.Fatal("ensure restarted daemon")
	}
	if len(readPIDs(pids)) != 1 {
		t.Fatal("reuse spawned a daemon")
	}
	if err := syscall.Kill(ids[0], syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	cliJSON(t, dir, "ensure")
	if after := cliJSON(t, dir, "query")["revision"]; after == before {
		t.Fatal("restart retained epoch")
	}
	if len(readPIDs(pids)) != 2 {
		t.Fatal("restart did not spawn exactly one new daemon")
	}
}
func TestEnsureCallerDiesBeforeReadiness(t *testing.T) {
	dir, pids := lifecycleState(t)
	cmd := helper(t, context.Background(), "ensure", "--state-dir", dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	deadline := time.Now().Add(4 * time.Second)
	for len(readPIDs(pids)) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(readPIDs(pids)) != 1 {
		t.Fatal("daemon was not spawned")
	}
	// Kill the entire initiating process group, as an editor/terminal teardown can.
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	cliJSON(t, dir, "ensure")
	cliJSON(t, dir, "query")
	if len(readPIDs(pids)) != 1 {
		t.Fatal("caller death caused duplicate daemon")
	}
}
func TestEnsureTimeoutDoesNotKillChild(t *testing.T) {
	dir, pids := lifecycleState(t)
	b, err := helper(t, context.Background(), "ensure", "--state-dir", dir, "--timeout", "100ms").CombinedOutput()
	if err == nil || !strings.Contains(string(b), "deadline") {
		t.Fatal(err, string(b))
	}
	cliJSON(t, dir, "ensure")
	cliJSON(t, dir, "query")
	if len(readPIDs(pids)) != 1 {
		t.Fatal("timeout replaced child")
	}
}
func TestEnsureRefusesLiveIncompatibleAndPrivateFailures(t *testing.T) {
	dir, pids := lifecycleState(t)
	listener, err := net.Listen("unix", filepath.Join(dir, "query.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			c, e := listener.Accept()
			if e != nil {
				return
			}
			go func() {
				defer c.Close()
				var q any
				_ = json.NewDecoder(c).Decode(&q)
				fmt.Fprintln(c, `{"version":2,"complete":true,"revision":"foreign"}`)
			}()
		}
	}()
	before, _ := os.Lstat(filepath.Join(dir, "query.sock"))
	b, err := helper(t, context.Background(), "ensure", "--state-dir", dir).CombinedOutput()
	if err == nil || !strings.Contains(string(b), "incompatible") {
		t.Fatal(err, string(b))
	}
	after, _ := os.Lstat(filepath.Join(dir, "query.sock"))
	if !os.SameFile(before, after) {
		t.Fatal("live endpoint replaced")
	}
	if len(readPIDs(pids)) != 0 {
		t.Fatal("incompatible endpoint spawned daemon")
	}
	b, err = helper(t, context.Background(), "daemon", "--state-dir", dir).CombinedOutput()
	if err == nil || !strings.Contains(string(b), "live listener") {
		t.Fatal(err, string(b))
	}
	after, _ = os.Lstat(filepath.Join(dir, "query.sock"))
	if !os.SameFile(before, after) {
		t.Fatal("foreground daemon replaced live endpoint")
	}
	private := filepath.Join(dir, "public")
	os.Mkdir(private, 0755)
	b, err = helper(t, context.Background(), "ensure", "--state-dir", private).CombinedOutput()
	if err == nil || !strings.Contains(string(b), "private") {
		t.Fatal(err, string(b))
	}
}
func TestEnsureReportsStartupFailure(t *testing.T) {
	dir, _ := lifecycleState(t)
	// Never replace unrelated regular files at the socket path.
	path := filepath.Join(dir, "query.sock")
	os.WriteFile(path, []byte("keep"), 0600)
	b, err := helper(t, context.Background(), "ensure", "--state-dir", dir).CombinedOutput()
	if err == nil || !strings.Contains(string(b), "not a socket") {
		t.Fatal(err, string(b))
	}
	content, _ := os.ReadFile(path)
	if string(content) != "keep" {
		t.Fatal("unrelated file changed")
	}
}

func TestEnsureRelaysChildStorageFailure(t *testing.T) {
	dir, _ := lifecycleState(t)
	if err := os.WriteFile(filepath.Join(dir, "state.db"), []byte("not a database"), 0600); err != nil {
		t.Fatal(err)
	}
	b, err := helper(t, context.Background(), "ensure", "--state-dir", dir).CombinedOutput()
	if err == nil || !strings.Contains(string(b), "daemon startup:") {
		t.Fatal(err, string(b))
	}
}

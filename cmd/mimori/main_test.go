package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCLIHelper(t *testing.T) {
	if os.Getenv("MIMORI_CLI_TEST") != "1" {
		return
	}
	for i, a := range os.Args {
		if a == "--" {
			os.Args = append([]string{"mimori"}, os.Args[i+1:]...)
			main()
			os.Exit(0)
		}
	}
	os.Exit(99)
}
func helper(t *testing.T, ctx context.Context, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.CommandContext(ctx, os.Args[0], append([]string{"-test.run=^TestCLIHelper$", "--"}, args...)...)
	cmd.Env = append(os.Environ(), "MIMORI_CLI_TEST=1")
	return cmd
}
func TestCLIStdinTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	cmd := helper(t, ctx, "ingest", "--state-dir", filepath.Join(t.TempDir(), "state"))
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()
	cmd.Stdin = read
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	before := time.Now()
	err = cmd.Run()
	if err == nil || !strings.Contains(stderr.String(), "stdin timeout") {
		t.Fatal(err, stderr.String())
	}
	if time.Since(before) > 2500*time.Millisecond {
		t.Fatal("hook waited too long")
	}
}
func TestCLIFixtureOfflineIngestAndDiagnostics(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	state := filepath.Join(t.TempDir(), "state")
	cmd := helper(t, ctx, "hook", "--state-dir", state, "--provider", "codex")
	cmd.Stdin = strings.NewReader(`{"session_id":"s","hook_event_name":"PermissionRequest","tool_input":{"secret":"DO_NOT_STORE"}}`)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatal(err, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatal("hook emitted a decision")
	}
	files, err := os.ReadDir(filepath.Join(state, "spool"))
	if err != nil || len(files) != 1 {
		t.Fatal(files, err)
	}
	b, err := os.ReadFile(filepath.Join(state, "spool", files[0].Name()))
	if err != nil || strings.Contains(string(b), "DO_NOT_STORE") || !strings.Contains(string(b), "attention_unknown") {
		t.Fatal(string(b), err)
	}
	bad := helper(t, ctx, "ingest", "--state-dir", state)
	bad.Stdin = strings.NewReader("not json")
	bad.Stdout = &stdout
	bad.Stderr = &stderr
	if err = bad.Run(); err == nil || stdout.Len() != 0 || stderr.Len() == 0 {
		t.Fatal("missing nonzero stderr diagnosis")
	}
}

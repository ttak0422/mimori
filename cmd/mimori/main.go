package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/ttak0422/mimori/internal/mimori"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	syscall.Umask(0077)
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "mimori:", err)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: mimori {daemon|ingest|hook|query} [flags]")
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		base = filepath.Join(home, ".local", "state")
	}
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	dir := f.String("state-dir", filepath.Join(base, "mimori"), "absolute private state directory")
	provider := f.String("provider", "", "provider filter or hook provider")
	session := f.String("session", "", "session detail ID")
	rev := f.String("revision", "", "previous revision (same query only)")
	generation := f.Uint64("generation", 1, "authoritative process incarnation number for hook")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	switch args[0] {
	case "daemon":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return mimori.Serve(ctx, *dir, func(s string) { fmt.Fprintln(os.Stderr, "mimori:", s) })
	case "ingest", "hook":
		// Bound waiting for a producer that never closes stdin. Filesystem stalls still
		// require an outer provider hook timeout; no daemon/network access is needed.
		type result struct {
			b   []byte
			err error
		}
		ch := make(chan result, 1)
		go func() { b, e := io.ReadAll(io.LimitReader(os.Stdin, mimori.MaxPayload+1)); ch <- result{b, e} }()
		var r result
		select {
		case r = <-ch:
		case <-time.After(time.Second):
			return errors.New("stdin timeout")
		}
		if r.err != nil {
			return r.err
		}
		if len(r.b) > mimori.MaxPayload {
			return errors.New("payload too large")
		}
		var e mimori.Event
		var err error
		if args[0] == "hook" {
			e, err = mimori.NormalizeHook(*provider, r.b, *generation)
		} else {
			e, err = mimori.DecodeEvent(r.b)
		}
		if err != nil {
			return err
		}
		return mimori.Enqueue(*dir, e)
	case "query":
		r, err := mimori.Fetch(*dir, mimori.Query{Version: 1, Revision: *rev, Provider: *provider, Session: *session})
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(r)
	default:
		return errors.New("unknown command")
	}
}

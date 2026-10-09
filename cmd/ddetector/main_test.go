package main

import (
	"bufio"
	"context"
	"ddetector-go/internal/bridge"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"testing"
	"time"
)

func TestRunHelperProcess(t *testing.T) {
	if os.Getenv("DDETECTOR_RUN_HELPER") != "1" {
		return
	}
	flag.CommandLine = flag.NewFlagSet("ddetector", flag.ContinueOnError)
	os.Args = []string{"ddetector", "--serve", "--session-file", os.Getenv("DDETECTOR_TEST_SESSION")}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestRunSignalCancelsMaintenance(t *testing.T) {
	entered := make(chan struct{}, 1)
	canceled := make(chan struct{}, 1)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-r.Context().Done()
		canceled <- struct{}{}
	}))
	defer remote.Close()
	path := filepath.Join(t.TempDir(), "private", "session.json")
	if err := bridge.SaveSession(path, &bridge.Session{Access: "a", Refresh: "r", ExpiresAt: time.Now().Add(time.Hour).Unix(), User: bridge.User{ID: "u"}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunHelperProcess$")
	child.Env = append(os.Environ(), "DDETECTOR_RUN_HELPER=1", "DDETECTOR_TEST_SESSION="+path, "DDETECTOR_SUPABASE_URL="+remote.URL, "DDETECTOR_SUPABASE_ANON_KEY=public")
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	child.Stderr = os.Stderr
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill() }()
	var connection map[string]string
	if err = json.NewDecoder(stdout).Decode(&connection); err != nil {
		t.Fatal(err)
	}
	if connection["backend_url"] == "" || connection["bridge_key"] == "" {
		t.Fatal("missing bridge configuration")
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("maintenance not started")
	}
	if err = child.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if err = child.Wait(); err != nil {
		t.Fatal("shutdown failed", err)
	}
	select {
	case <-canceled:
	case <-ctx.Done():
		t.Fatal("maintenance request survived shutdown")
	}
}

func TestChildHelperProcess(t *testing.T) {
	if os.Getenv("DDETECTOR_CHILD_HELPER") != "1" {
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	fmt.Println("ready")
	<-ctx.Done()
	os.Exit(0)
}

func TestStopChildReapsProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestChildHelperProcess$")
	child.Env = append(os.Environ(), "DDETECTOR_CHILD_HELPER=1")
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill() }()
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "ready" {
		t.Fatal("child not ready")
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	stopChild(child, done)
	if child.ProcessState == nil || !child.ProcessState.Exited() {
		t.Fatal("child not reaped")
	}
}

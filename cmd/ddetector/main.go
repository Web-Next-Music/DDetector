package main

import (
	"context"
	"ddetector-go/internal/bridge"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	serve := flag.Bool("serve", false, "run without launching Qt; print bridge connection JSON to stdout")

	qt := flag.String("qt", os.Getenv("DDETECTOR_QT_EXECUTABLE"), "Qt executable to launch")
	cache, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	session := flag.String("session-file", filepath.Join(cache, "ddetector-go", "session.json"), "private session file")
	flag.Parse()
	if !*serve && *qt == "" {
		return fmt.Errorf("provide --qt executable, DDETECTOR_QT_EXECUTABLE, or --serve")
	}
	values, err := loadConfig()
	if err != nil {
		return err
	}
	baseURL := configValue(values, "DDETECTOR_SUPABASE_URL", "NEXT_PUBLIC_SUPABASE_URL")
	anonKey := configValue(values, "DDETECTOR_SUPABASE_ANON_KEY", "NEXT_PUBLIC_SUPABASE_ANONKEY", "NEXT_PUBLIC_SUPABASE_ANON_KEY")
	if baseURL == "" || anonKey == "" {
		return fmt.Errorf("Supabase configuration missing: check cmd/ddetector/public_config.json or override DDETECTOR_SUPABASE_URL and DDETECTOR_SUPABASE_ANON_KEY")
	}
	client, err := bridge.NewClient(baseURL, anonKey)
	if err != nil {
		return err
	}
	listener, err := bridge.Listen()
	if err != nil {
		return err
	}
	defer listener.Close()
	base := "http://" + listener.Addr().String()
	key := bridge.RandomKey()
	backend, err := bridge.NewServer(client, key, base, *session)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	backend.Start(ctx)
	server := &http.Server{Handler: backend.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 100 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	var child *exec.Cmd
	childDone := make(chan error, 1)
	if *serve {
		if err = json.NewEncoder(os.Stdout).Encode(map[string]string{"backend_url": base, "bridge_key": key}); err != nil {
			return err
		}
	} else {
		args := append([]string{}, flag.Args()...)
		args = append(args, "--backend-url", base, "--bridge-key", key)
		child = exec.Command(*qt, args...)
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err = child.Start(); err != nil {
			_ = server.Close()
			return fmt.Errorf("launch Qt: %w", err)
		}
		go func() { childDone <- child.Wait() }()
	}
	select {
	case <-ctx.Done():
		if child != nil {
			_ = child.Process.Signal(os.Interrupt)
			select {
			case <-childDone:
			case <-time.After(3 * time.Second):
				_ = child.Process.Kill()
				<-childDone
			}
		}
	case err = <-childDone:
		stop()
	case err = <-done:
		stop()
		if err == http.ErrServerClosed {
			err = nil
		}
	}
	stop()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdown)
	return err
}

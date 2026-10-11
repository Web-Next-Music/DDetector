package censor

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func pcm(seconds float64, rate int, signal func(float64) float64) []byte {
	b := make([]byte, int(seconds*float64(rate))*2)
	for i := 0; i < len(b)/2; i++ {
		v := signal(float64(i) / float64(rate))
		binary.LittleEndian.PutUint16(b[2*i:], uint16(int16(math.Max(-1, math.Min(1, v))*32767)))
	}
	return b
}
func testFeatures(t *testing.T, b []byte) features {
	t.Helper()
	f, err := extract(context.Background(), bytes.NewReader(b), 16000, 16000*60, nil)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestExtractLimits(t *testing.T) {
	_, err := extract(context.Background(), bytes.NewReader(make([]byte, 4000)), 16000, 100, nil)
	if err == nil {
		t.Fatal("sample limit ignored")
	}
	_, err = extract(context.Background(), bytes.NewReader([]byte{1}), 16000, 100, nil)
	if err == nil {
		t.Fatal("odd PCM accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = extract(ctx, bytes.NewReader(make([]byte, 2048)), 16000, 10000, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestCompareOffset(t *testing.T) {
	reference := features{rate: 16000}
	source := features{rate: 16000}
	for i := 0; i < 400; i++ {
		v := .001 + float64((i*7919+i*i*37)%997)/1000
		fr := frame{energy: v}
		fr.spectrum[(i*7)%spectralBands] = 1
		reference.frames = append(reference.frames, fr)
	}
	source.frames = append(source.frames, make([]frame, 32)...)
	source.frames = append(source.frames, reference.frames...)
	c, err := compare(context.Background(), source, reference)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Comparable || c.Correlation < .999 || math.Abs(c.OffsetSeconds-2.048) > .01 {
		t.Fatalf("offset %+v", c)
	}
	c, err = compare(context.Background(), features{rate: 16000, frames: make([]frame, 100)}, reference)
	if err != nil || c.Comparable {
		t.Fatalf("silence comparable %+v %v", c, err)
	}
}
func makeWAV(t *testing.T, b []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audio.wav")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.Write(make([]byte, 44))
	f.Write(b)
	if err = writeWAVHeader(f, int64(len(b)/2), 16000); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestFFmpegEngine(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	path := makeWAV(t, pcm(4, 16000, func(s float64) float64 { return .3 * math.Sin(2*math.Pi*440*s) }))
	e, err := NewEngine(Config{MaxDuration: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	result, err := e.Analyze(context.Background(), Request{Source: Source{Path: path}, Reference: &Source{Path: path}})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(result.DurationSeconds-4) > .01 || result.Comparison == nil || len(result.Warnings) < 1 {
		t.Fatalf("result %+v", result)
	}
	e, _ = NewEngine(Config{MaxDuration: time.Second})
	if _, err = e.Analyze(context.Background(), Request{Source: Source{Path: path}}); err == nil {
		t.Fatal("duration limit ignored")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = e.Analyze(ctx, Request{Source: Source{Path: path}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}
func TestHTTPValidation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		switch r.URL.Path {
		case "/status":
			w.WriteHeader(403)
		case "/large":
			w.Write(make([]byte, 2049))
		case "/redirect":
			http.Redirect(w, r, "/ok", http.StatusFound)
		case "/html":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<html>error</html>"))
		case "/encoding":
			w.Header().Set("Content-Encoding", "gzip")
			w.Write([]byte("abc"))
		default:
			w.Write([]byte("audio"))
		}
	}))
	defer server.Close()
	e, _ := NewEngine(Config{MaxBytes: 1024})
	if _, err := e.acquire(context.Background(), Source{URL: server.URL}, t.TempDir(), "input"); err == nil {
		t.Fatal("private HTTP accepted")
	}
	e, _ = NewEngine(Config{AllowPrivateHTTP: true, MaxBytes: 1024})
	for _, path := range []string{"/status", "/large", "/encoding", "/html"} {
		if _, err := e.acquire(context.Background(), Source{URL: server.URL + path}, t.TempDir(), "input"); err == nil {
			t.Fatalf("accepted %s", path)
		}
	}
	if _, err := e.acquire(context.Background(), Source{URL: server.URL + "/redirect"}, t.TempDir(), "input"); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"file:///etc/passwd", "http://user:secret@host/path"} {
		if _, err := e.acquire(context.Background(), Source{URL: raw}, t.TempDir(), "input"); err == nil {
			t.Fatal("unsafe URL accepted")
		}
	}
}

func TestHTTPDecode(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	path := makeWAV(t, pcm(1, 16000, func(float64) float64 { return 0 }))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/wav")
		w.Write(data)
	}))
	defer server.Close()
	e, _ := NewEngine(Config{AllowPrivateHTTP: true})
	result, err := e.Analyze(context.Background(), Request{Source: Source{URL: server.URL}})
	if err != nil || result.DurationSeconds != 1 {
		t.Fatalf("HTTP decode %+v %v", result, err)
	}
}

func TestQueueWorkersBounded(t *testing.T) {
	var active, peak atomic.Int32
	started := make(chan struct{}, 8)
	p := processorFunc(func(ctx context.Context, r Request) (Result, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		started <- struct{}{}
		<-ctx.Done()
		return Result{}, ctx.Err()
	})
	q, err := OpenQueue(Config{StateDir: t.TempDir(), Workers: 2}, p)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	for i := 0; i < 8; i++ {
		if _, err = q.Submit(Request{Source: Source{Path: "audio"}}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("workers not started")
		}
	}
	if err = q.Close(); err != nil {
		t.Fatal(err)
	}
	if peak.Load() != 2 {
		t.Fatalf("worker bound: %d", peak.Load())
	}
}

type processorFunc func(context.Context, Request) (Result, error)

func (f processorFunc) Analyze(ctx context.Context, r Request) (Result, error) { return f(ctx, r) }
func waitJob(t *testing.T, q *Queue, id string) Job {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	j, err := q.Wait(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return j
}
func TestQueuePersistenceAndLimits(t *testing.T) {
	c := Config{StateDir: t.TempDir(), Workers: 1, MaxJobs: 1}
	p := processorFunc(func(context.Context, Request) (Result, error) { return Result{DurationSeconds: 5}, nil })
	q, err := OpenQueue(c, p)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if other, err := OpenQueue(c, p); err == nil {
		other.Close()
		t.Fatal("concurrent owner accepted")
	}
	j, err := q.Submit(Request{Source: Source{Path: "track.wav"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.Submit(j.Request); !errors.Is(err, ErrQueueFull) {
		t.Fatal(err)
	}
	j = waitJob(t, q, j.ID)
	if j.Status != Succeeded || j.Result.DurationSeconds != 5 {
		t.Fatalf("job %+v", j)
	}
	j.Result.DurationSeconds = 999
	got, _ := q.Get(j.ID)
	if got.Result.DurationSeconds != 5 {
		t.Fatal("mutable result escaped")
	}
	if err = q.Close(); err != nil {
		t.Fatal(err)
	}
	q, err = OpenQueue(c, p)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	got, _ = q.Get(j.ID)
	if got.Status != Succeeded {
		t.Fatal(got)
	}
	if err = q.Delete(j.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = q.Get(j.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}
func TestQueueCancelAndRecovery(t *testing.T) {
	started := make(chan struct{}, 4)
	var count atomic.Int32
	p := processorFunc(func(ctx context.Context, r Request) (Result, error) {
		count.Add(1)
		started <- struct{}{}
		<-ctx.Done()
		return Result{}, ctx.Err()
	})
	c := Config{StateDir: t.TempDir(), Workers: 1}
	q, err := OpenQueue(c, p)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	j, _ := q.Submit(Request{Source: Source{Path: "track.wav"}})
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("not started")
	}
	if err = q.Cancel(j.ID); err != nil {
		t.Fatal(err)
	}
	if got := waitJob(t, q, j.ID); got.Status != Cancelled {
		t.Fatal(got)
	}
	next, _ := q.Submit(j.Request)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("not started")
	}
	if err = q.Close(); err != nil {
		t.Fatal(err)
	}
	q, err = OpenQueue(c, processorFunc(func(context.Context, Request) (Result, error) { return Result{}, nil }))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if got := waitJob(t, q, next.ID); got.Status != Succeeded {
		t.Fatal(got)
	}
	got, _ := q.Get(j.ID)
	if got.Status != Cancelled {
		t.Fatal("cancel not persisted")
	}
}
func TestQueueCrashRecovery(t *testing.T) {
	dir := t.TempDir()
	j := Job{ID: "crashed", Status: Running, Request: Request{Source: Source{Path: "/tmp/audio"}}, CreatedAt: time.Now()}
	q := &Queue{config: Config{StateDir: dir}, jobs: map[string]Job{j.ID: j}}
	if err := q.persist(); err != nil {
		t.Fatal(err)
	}
	live, err := OpenQueue(Config{StateDir: dir}, processorFunc(func(context.Context, Request) (Result, error) { return Result{}, nil }))
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	if got := waitJob(t, live, j.ID); got.Status != Succeeded {
		t.Fatal(got)
	}
}
func TestQueueProcessorPanicAndDeadline(t *testing.T) {
	for _, panicMode := range []bool{true, false} {
		p := processorFunc(func(ctx context.Context, r Request) (Result, error) {
			if panicMode {
				panic("synthetic")
			}
			<-ctx.Done()
			return Result{}, ctx.Err()
		})
		q, err := OpenQueue(Config{StateDir: t.TempDir(), JobTimeout: 20 * time.Millisecond}, p)
		if err != nil {
			t.Fatal(err)
		}
		j, _ := q.Submit(Request{Source: Source{Path: "track"}})
		got := waitJob(t, q, j.ID)
		q.Close()
		if got.Status != Failed || got.Error == "" {
			t.Fatal(got)
		}
	}
}

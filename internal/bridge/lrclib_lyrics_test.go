package bridge

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestLRCLIBPrimary(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		timed      bool
		count      int
	}{
		{"synced", `{"id":1,"syncedLyrics":"[ar:Artist]\n[offset:100]\n[00:02][00:01]weed","plainLyrics":"other"}`, true, 2},
		{"plain", `{"id":1,"syncedLyrics":null,"plainLyrics":"weed\nordinary"}`, false, 2},
		{"unusable sync", `{"id":1,"syncedLyrics":"[ti:Title]\n[00:01]","plainLyrics":"weed"}`, false, 1},
		{"instrumental", `{"id":1,"instrumental":true,"plainLyrics":null}`, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newYandexClient("private-token")
			c.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Error("unexpected Yandex fallback")
				return response(404, ""), nil
			})
			c.lrclib.transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				q := r.URL.Query()
				if r.URL.Scheme != "https" || r.URL.Host != "lrclib.net" || r.URL.Path != "/api/get" || r.Method != "GET" || len(q) != 2 || q.Get("track_name") != "Песня & song" || q.Get("artist_name") != "A+B" {
					t.Errorf("wrong request: %s", r.URL)
				}
				if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("User-Agent") != lrclibUserAgent {
					t.Error("unsafe headers", r.Header)
				}
				return response(200, strings.Replace(tc.body, `"id":1`, `"id":1,"trackName":"Песня & song","artistName":"A+B"`, 1)), nil
			})
			lines, err := c.lyricsForTrack(context.Background(), Track{ID: 1, Title: " Песня & song ", Artist: "A+B"})
			if err != nil || len(lines) != tc.count || hasTimedLyrics(lines) != tc.timed {
				t.Fatal(lines, err)
			}
			if tc.timed && (*lines[0].TS != "00:01.100" || *lines[1].TS != "00:02.100") {
				t.Fatal(lines)
			}
		})
	}
}

func TestLRCLIBFallback(t *testing.T) {
	for _, status := range []int{200, 404, 503} {
		for _, fallback := range []string{"weed", ""} {
			t.Run(http.StatusText(status)+fallback, func(t *testing.T) {
				c := newYandexClient("secret")
				c.lrclib.transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
					return response(status, `{"id":1,"plainLyrics":null,"syncedLyrics":null}`), nil
				})
				calls := 0
				c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					if strings.HasSuffix(r.URL.Path, "/supplement") {
						return response(200, `{"result":{"lyrics":{"fullLyrics":"`+fallback+`"}}}`), nil
					}
					return response(404, ""), nil
				})
				lines, err := c.lyricsForTrack(context.Background(), Track{ID: 1, Title: "Song", Artist: "Artist"})
				if calls != 3 {
					t.Fatal("fallback calls", calls)
				}
				if status == 503 && fallback == "" {
					if err == nil {
						t.Fatal("operational failure cached as absence")
					}
					return
				}
				if err != nil || (len(lines) > 0) != (fallback != "") {
					t.Fatal(lines, err)
				}
			})
		}
	}
}

func TestLRCLIBInvalidAndSafeResponses(t *testing.T) {
	for _, body := range []string{"null", `{}`, `{"id":1,"plainLyrics":42}`, "secret invalid JSON", string([]byte{255}), strings.Repeat("x", (2<<20)+1)} {
		c := newLRCLIBClient()
		c.transport = roundTripFunc(func(*http.Request) (*http.Response, error) { return response(200, body), nil })
		_, _, err := c.get(context.Background(), Track{Title: "secret title", Artist: "artist"})
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal("unsafe or absent error", err)
		}
	}
	for _, redirect := range []bool{false, true} {
		c := newLRCLIBClient()
		calls := 0
		c.transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			if !redirect {
				return nil, errors.New("secret transport URL")
			}
			r := response(302, "secret body")
			r.Header.Set("Location", "https://other.example/secret")
			return r, nil
		})
		_, _, err := c.get(context.Background(), Track{Title: "Song", Artist: "Artist"})
		if err == nil || strings.Contains(err.Error(), "secret") || calls != 1 {
			t.Fatal(err, calls)
		}
	}
}

func TestLRCLIBSpacesRequests(t *testing.T) {
	c := newLRCLIBClient()
	arrivals := make(chan time.Time, 2)
	c.transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		arrivals <- time.Now()
		return response(404, ""), nil
	})
	done := make(chan error, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for i := 0; i < 2; i++ {
		go func() {
			_, _, err := c.get(ctx, Track{Title: "Song", Artist: "Artist"})
			done <- err
		}()
	}
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	first, second := <-arrivals, <-arrivals
	if second.Sub(first) < lrclibRequestInterval {
		t.Fatal("requests were not spaced", second.Sub(first))
	}
}

func TestLRCLIBOverlapsSlowRequests(t *testing.T) {
	c := newLRCLIBClient()
	arrivals := make(chan time.Time, 2)
	release := make(chan struct{})
	c.transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		arrivals <- time.Now()
		select {
		case <-release:
			return response(404, ""), nil
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, _, err := c.get(ctx, Track{Title: "Song", Artist: "Artist"})
			done <- err
		}()
	}
	var times [2]time.Time
	for i := range times {
		select {
		case times[i] = <-arrivals:
		case <-ctx.Done():
			t.Fatal("slow request blocked the next request")
		}
	}
	close(release)
	if times[1].Sub(times[0]) < lrclibRequestInterval {
		t.Fatal("request spacing lost")
	}
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestLRCLIBCancellationAndRetryAfter(t *testing.T) {
	c := newLRCLIBClient()
	calls := 0
	c.transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		r := response(429, "")
		r.Header.Set("Retry-After", "60")
		return r, nil
	})
	track := Track{Title: "Song", Artist: "Artist"}
	if _, _, err := c.get(context.Background(), track); err == nil {
		t.Fatal("missing rate limit error")
	}
	if time.Until(c.next) < 59*time.Second {
		t.Fatal("Retry-After not honored")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, _, err := c.get(ctx, track); !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
		t.Fatal(err, calls)
	}
	c.gate <- struct{}{}
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if _, _, err := c.get(ctx2, track); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	<-c.gate
	yc := newYandexClient("secret")

	yc.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Error("fallback after cancellation")
		return response(404, ""), nil
	})
	active, stop := context.WithCancel(context.Background())
	defer stop()
	yc.lrclib.transport = roundTripFunc(func(r *http.Request) (*http.Response, error) { stop(); return nil, r.Context().Err() })
	if _, err := yc.lyricsForTrack(active, track); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

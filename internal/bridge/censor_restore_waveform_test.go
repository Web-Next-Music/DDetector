package bridge

import (
	"context"
	"ddetector-go/internal/censor"
	"errors"
	"reflect"
	"testing"
)

type waveformProcessor struct {
	orchestrationProcessor
	analyze func(censor.Request) (censor.Result, error)
}

func (p *waveformProcessor) Analyze(_ context.Context, r censor.Request) (censor.Result, error) {
	return p.analyze(r)
}

type waveformPlaybackBackend struct{ *playbackCensorBackend }

func (b *waveformPlaybackBackend) PlaybackCandidate(ctx context.Context, i CensorIdentity, id string) (CensorCandidate, error) {
	return b.resolve(ctx, i, id)
}
func (b *waveformPlaybackBackend) Candidate(context.Context, CensorIdentity, string) (CensorCandidate, error) {
	return CensorCandidate{}, errors.New("comparison resolver must not be used")
}

func TestRestoreWaveformSkipsFailuresAndStoresOnlyVisualization(t *testing.T) {
	for _, playback := range []bool{false, true} {
		name := "candidate fallback"
		if playback {
			name = "playback preferred"
		}
		t.Run(name, func(t *testing.T) {
			_, b, rt := playbackFixture(t)
			identity := CensorIdentity{UserID: "user-1", AccessToken: "secret-access"}
			var resolved, analyzed []string
			b.resolve = func(_ context.Context, got CensorIdentity, id string) (CensorCandidate, error) {
				if got != identity {
					t.Fatalf("resolver identity: %+v", got)
				}
				resolved = append(resolved, id)
				if id == "resolve-failure" {
					return CensorCandidate{}, errors.New("unavailable")
				}
				c := CensorCandidate{ID: id, URL: "https://cdn.example/" + id + ".mp3", FullAudio: true, Downloadable: true}
				if id == "resolved-page" {
					c.Metadata = map[string]string{"kind": "page"}
				}
				return c, nil
			}
			if playback {
				rt.backend = &waveformPlaybackBackend{b}
			}
			rich := strongFixture()
			rich.Waveform = []censor.WaveformPoint{{}}
			rich.Transcript = "must not become evidence"
			rich.Warnings = []string{"must not be retained"}
			rt.processor = &waveformProcessor{analyze: func(r censor.Request) (censor.Result, error) {
				analyzed = append(analyzed, r.Source.URL)
				if r.Reference != nil || r.Source.Path != "" {
					t.Fatalf("unexpected comparison request: %+v", r)
				}
				switch r.Source.URL {
				case "https://cdn.example/analyze-failure.mp3":
					return censor.Result{}, errors.New("decode failed")
				case "https://cdn.example/empty.mp3":
					return strongFixture(), nil
				}
				return rich, nil
			}}
			job := rt.snapshot().Jobs[0]
			job.Result = nil
			job.Candidates = nil
			for _, id := range []string{"page", "resolve-failure", "resolved-page", "analyze-failure", "empty", "success", "unused"} {
				c := CensorCandidate{ID: id, URL: "https://cdn.example/stale.mp3", FullAudio: true, Downloadable: true}
				if id == "page" {
					c.Metadata = map[string]string{"kind": "page"}
				}
				job.Candidates = append(job.Candidates, c)
			}
			if err := rt.restoreWaveform(context.Background(), identity, &job); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(resolved, []string{"resolve-failure", "resolved-page", "analyze-failure", "empty", "success"}) {
				t.Fatalf("resolved: %v", resolved)
			}
			if !reflect.DeepEqual(analyzed, []string{"https://cdn.example/analyze-failure.mp3", "https://cdn.example/empty.mp3", "https://cdn.example/success.mp3"}) {
				t.Fatalf("analyzed: %v", analyzed)
			}
			want := &censor.Result{Waveform: rich.Waveform, DurationSeconds: rich.DurationSeconds}
			if !reflect.DeepEqual(job.Result, want) {
				t.Fatalf("unexpected visualization/evidence: %+v", job.Result)
			}
			if !reflect.DeepEqual(rt.snapshot().Jobs[0].Result, want) {
				t.Fatal("waveform was not stored")
			}
			for _, c := range job.Candidates {
				if c.Result != nil {
					t.Fatalf("candidate evidence: %+v", c)
				}
			}
		})
	}
}

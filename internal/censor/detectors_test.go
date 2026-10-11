package censor

import (
	"bytes"
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCutAtSupportedSampleRates(t *testing.T) {
	for _, rate := range []int{8000, 16000, 48000} {
		data := pcm(5, rate, func(s float64) float64 {
			if s >= 2 && s < 2.2 {
				return 0
			}
			return musicSignal(s)
		})
		f, err := extract(context.Background(), bytes.NewReader(data), rate, int64(rate*6), nil)
		if err != nil {
			t.Fatal(err)
		}
		intervals, _ := detectIntervals(f, 128)
		if len(intervals) != 1 || intervals[0].Reason != "abrupt_short_silence" || math.Abs(intervals[0].StartSeconds-2) > .02 {
			t.Fatalf("rate %d: %+v", rate, intervals)
		}
	}
}

func TestShortCutResolution(t *testing.T) {
	f := testFeatures(t, pcm(5, 16000, func(s float64) float64 {
		if s >= 2.013 && s < 2.113 {
			return 0
		}
		return musicSignal(s)
	}))
	intervals, _ := detectIntervals(f, 128)
	if len(intervals) != 1 || math.Abs(intervals[0].StartSeconds-2.013) > .016 || math.Abs(intervals[0].EndSeconds-2.113) > .016 {
		t.Fatalf("100ms cut %+v", intervals)
	}
}

func TestRealFPcalcWhenInstalled(t *testing.T) {
	if _, err := exec.LookPath("fpcalc"); err != nil {
		t.Skip("optional fpcalc not installed")
	}
	path := makeWAV(t, pcm(12, 16000, musicSignal))
	e, _ := NewEngine(Config{FPcalcCommand: "fpcalc"})
	fp, err := e.fingerprint(context.Background(), path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(fp.Values) < 24 {
		t.Fatal("empty Chromaprint")
	}
}

func musicSignal(s float64) float64 {
	envelope := .16 + .06*math.Sin(s*3.19) + .03*math.Sin(s*7.37)
	return envelope * (math.Sin(2*math.Pi*220*s) + .4*math.Sin(2*math.Pi*570*s) + .3*math.Sin(2*math.Pi*1700*s))
}
func TestSuspiciousSilenceAndNaturalPauses(t *testing.T) {
	tests := []struct {
		name   string
		signal func(float64) float64
		want   bool
	}{
		{"hard_cut", func(s float64) float64 {
			if s >= 3 && s < 3.4 {
				return 0
			}
			return musicSignal(s)
		}, true},
		{"long_pause", func(s float64) float64 {
			if s >= 2 && s < 5 {
				return 0
			}
			return musicSignal(s)
		}, false},
		{"slow_fades", func(s float64) float64 {
			gain := 1.0
			if s >= 2 && s < 3 {
				gain = 3 - s
			}
			if s >= 3 && s < 3.4 {
				gain = 0
			}
			if s >= 3.4 && s < 4.4 {
				gain = s - 3.4
			}
			return gain * musicSignal(s)
		}, false},
		{"normal_music", musicSignal, false},
		{"whole_silence", func(float64) float64 { return 0 }, false},
		{"track_edges", func(s float64) float64 {
			if s < 1 || s >= 6 {
				return 0
			}
			return musicSignal(s)
		}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := testFeatures(t, pcm(7, 16000, tc.signal))
			intervals, truncated := detectIntervals(f, 128)
			if truncated {
				t.Fatal("unexpected truncation")
			}
			silences := 0
			for _, v := range intervals {
				if v.Reason == "abrupt_short_silence" {
					silences++
					if math.Abs(v.StartSeconds-3) > .13 || math.Abs(v.EndSeconds-3.4) > .13 || v.Confidence <= 0 || v.Status != "suspicious" {
						t.Fatalf("interval %+v", v)
					}
				}
			}
			if (silences > 0) != tc.want {
				t.Fatalf("intervals %+v", intervals)
			}
		})
	}
}
func TestBeepSuddenOnsetAndRepetition(t *testing.T) {
	f := testFeatures(t, pcm(8, 16000, func(s float64) float64 {
		if (s >= 2 && s < 2.4) || (s >= 5 && s < 5.4) {
			return .3 * math.Sin(2*math.Pi*1000*s)
		}
		return musicSignal(s)
	}))
	intervals, _ := detectIntervals(f, 128)
	beeps := 0
	for _, v := range intervals {
		if v.Reason == "repeated_narrowband_beep" {
			beeps++
			if v.Confidence < .7 || v.EndSeconds-v.StartSeconds > .5 {
				t.Fatalf("beep %+v", v)
			}
		}
	}
	if beeps != 2 {
		t.Fatalf("beeps %+v", intervals)
	}
	for _, signal := range []func(float64) float64{func(s float64) float64 { return .3 * math.Sin(2*math.Pi*1000*s) }, func(s float64) float64 {
		gain := 0.0
		if s >= 2 && s < 2.5 {
			gain = (s - 2) * 2
		}
		if s >= 2.5 && s < 3 {
			gain = 1
		}
		if s >= 3 && s < 3.5 {
			gain = (3.5 - s) * 2
		}
		return gain * .3 * math.Sin(2*math.Pi*1000*s)
	}} {
		normal := testFeatures(t, pcm(7, 16000, signal))
		got, _ := detectIntervals(normal, 128)
		if len(got) != 0 {
			t.Fatalf("steady/slow tone flagged %+v", got)
		}
	}
}
func TestBoundedWaveformAndIntervals(t *testing.T) {
	f := testFeatures(t, pcm(20.03, 16000, func(s float64) float64 {
		phase := math.Mod(s, 1)
		if s > 1 && phase >= .35 && phase < .65 {
			return 0
		}
		return musicSignal(s)
	}))
	points := waveform(f, 31)
	if len(points) > 31 || len(points) == 0 {
		t.Fatal(len(points))
	}
	for i, v := range points {
		if v.Min < -1 || v.Max > 1 || v.RMS < 0 || v.Min > v.Max || v.EndSeconds <= v.StartSeconds {
			t.Fatalf("point %+v", v)
		}
		if i > 0 && points[i-1].EndSeconds != v.StartSeconds {
			t.Fatal("waveform gap")
		}
	}
	if math.Abs(points[len(points)-1].EndSeconds-20.03) > 1.0/16000 {
		t.Fatal("missing tail")
	}
	intervals, truncated := detectIntervals(f, 2)
	if !truncated || len(intervals) != 2 {
		t.Fatalf("limit %+v %v", intervals, truncated)
	}
}
func TestSpectralComparisonRejectsSameEnvelopeDifferentRecording(t *testing.T) {
	signal := func(hz float64) func(float64) float64 {
		return func(s float64) float64 {
			i := int(s * 16000 / frameSamples)
			a := .1 + float64((i*7919+i*i*37)%997)/6000
			return a * math.Sin(2*math.Pi*hz*s)
		}
	}
	a := testFeatures(t, pcm(8, 16000, signal(500)))
	b := testFeatures(t, pcm(8, 16000, signal(1500)))
	c, err := compare(context.Background(), a, b)
	if err != nil {
		t.Fatal(err)
	}
	if c.Correlation < .98 || c.Comparable || c.ConfidenceClass != "different_recording" {
		t.Fatalf("energy-only false match %+v", c)
	}
	same, err := compare(context.Background(), a, a)
	if err != nil || !same.Comparable || same.ConfidenceClass != "strong_match" || same.SpectralSimilarity < .99 {
		t.Fatalf("same %+v %v", same, err)
	}
}
func TestLocalizedReferenceRestoration(t *testing.T) {
	ref := testFeatures(t, pcm(8, 16000, musicSignal))
	source := testFeatures(t, pcm(8, 16000, func(s float64) float64 {
		if s >= 3 && s < 3.4 {
			return 0
		}
		return musicSignal(s)
	}))
	intervals, _ := detectIntervals(source, 128)
	if len(intervals) != 1 {
		t.Fatal(intervals)
	}
	c, err := compare(context.Background(), source, ref)
	if err != nil {
		t.Fatal(err)
	}
	localRestoration(source, ref, c, intervals)
	if !c.Comparable || intervals[0].Restoration == nil || intervals[0].Status != "reference_restoration_candidate" {
		t.Fatalf("restoration %+v %+v", c, intervals)
	}
	unchanged, _ := detectIntervals(source, 128)
	same, _ := compare(context.Background(), source, source)
	localRestoration(source, source, same, unchanged)
	if unchanged[0].Restoration != nil {
		t.Fatal("same censored region claimed restored")
	}
	rejected, _ := detectIntervals(source, 128)
	c.Comparable = false
	c.ConfidenceClass = "different_recording"
	localRestoration(source, ref, c, rejected)
	if rejected[0].Restoration != nil {
		t.Fatal("different recording claimed restored")
	}
}
func TestOptionalChromaprint(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fpcalc")
	words := strings.Repeat("123456,", 47) + "123456"
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' '{\"duration\":10,\"fingerprint\":["+words+"]}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(Config{FPcalcCommand: script})
	if err != nil {
		t.Fatal(err)
	}
	fp, err := e.fingerprint(context.Background(), "unused", dir)
	if err != nil {
		t.Fatal(err)
	}
	if fp.Algorithm != "chromaprint-2" || len(fp.Values) != 48 {
		t.Fatal(fp)
	}
	c := Comparison{Comparable: true, ConfidenceClass: "strong_match", Confidence: .9}
	applyFingerprint(&c, fp, fp)
	if c.FingerprintSimilarity == nil || *c.FingerprintSimilarity != 1 || !c.Comparable {
		t.Fatal(c)
	}
	different := &Fingerprint{Algorithm: fp.Algorithm, Values: make([]uint32, len(fp.Values))}
	for i := range different.Values {
		different.Values[i] = ^fp.Values[i]
	}
	applyFingerprint(&c, fp, different)
	if c.Comparable || c.ConfidenceClass != "different_recording" {
		t.Fatal(c)
	}
	malformed := filepath.Join(dir, "bad-fpcalc")
	os.WriteFile(malformed, []byte("#!/bin/sh\nprintf 'not JSON'\n"), 0700)
	e, _ = NewEngine(Config{FPcalcCommand: malformed})
	if _, err = e.fingerprint(context.Background(), "unused", dir); err == nil {
		t.Fatal("invalid fingerprint accepted")
	}
}

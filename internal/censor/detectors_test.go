package censor

import (
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestBoundedWaveform(t *testing.T) {
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

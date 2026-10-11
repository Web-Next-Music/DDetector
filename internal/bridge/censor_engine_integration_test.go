package bridge

import (
	"context"
	"ddetector-go/internal/censor"
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The protected API uses HTTPS fixture URLs. Map those exact URLs to generated
// WAV fixtures so this test runs the real FFmpeg/DSP engine without public audio
// downloads or weakening the production engine's TLS/private-address checks.
type fixtureAudioProcessor struct {
	engine *censor.Engine
	files  map[string]string
}

func (p *fixtureAudioProcessor) Analyze(ctx context.Context, r censor.Request) (censor.Result, error) {
	source := p.files[r.Source.URL]
	if source == "" || r.Source.Path != "" {
		return censor.Result{}, ErrCensorNotFound
	}
	r.Source = censor.Source{Path: source}
	if r.Reference != nil {
		reference := p.files[r.Reference.URL]
		if reference == "" || r.Reference.Path != "" {
			return censor.Result{}, ErrCensorNotFound
		}
		r.Reference = &censor.Source{Path: reference}
	}
	return p.engine.Analyze(ctx, r)
}
func censorFixtureWAV(t *testing.T, path string, gap bool) {
	t.Helper()
	const rate = 16000
	const samples = 8 * rate
	data := make([]byte, 44+2*samples)
	copy(data, "RIFF")
	binary.LittleEndian.PutUint32(data[4:], uint32(len(data)-8))
	copy(data[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[16:], 16)
	binary.LittleEndian.PutUint16(data[20:], 1)
	binary.LittleEndian.PutUint16(data[22:], 1)
	binary.LittleEndian.PutUint32(data[24:], rate)
	binary.LittleEndian.PutUint32(data[28:], rate*2)
	binary.LittleEndian.PutUint16(data[32:], 2)
	binary.LittleEndian.PutUint16(data[34:], 16)
	copy(data[36:], "data")
	binary.LittleEndian.PutUint32(data[40:], samples*2)
	for n := 0; n < samples; n++ {
		s := float64(n) / rate
		envelope := .16 + .06*math.Sin(s*3.19) + .03*math.Sin(s*7.37)
		v := envelope * (math.Sin(2*math.Pi*220*s) + .4*math.Sin(2*math.Pi*570*s) + .3*math.Sin(2*math.Pi*1700*s))
		if gap && s >= 3 && s < 3.4 {
			v = 0
		}
		binary.LittleEndian.PutUint16(data[44+n*2:], uint16(int16(v*32767)))
	}
	if e := os.WriteFile(path, data, 0600); e != nil {
		t.Fatal(e)
	}
}
func TestCensorHTTPRealEngineReviewAndRecheck(t *testing.T) {
	if _, e := exec.LookPath("ffmpeg"); e != nil {
		t.Skip("FFmpeg is unavailable")
	}
	dir := t.TempDir()
	original := filepath.Join(dir, "original.wav")
	reference := filepath.Join(dir, "reference.wav")
	censorFixtureWAV(t, original, true)
	censorFixtureWAV(t, reference, false)
	engine, e := censor.NewEngine(censor.Config{StateDir: filepath.Join(dir, "engine")})
	if e != nil {
		t.Fatal(e)
	}
	s, f, _ := integrationCensor(t)
	s.censorProcessor = &fixtureAudioProcessor{engine: engine, files: map[string]string{"https://audio.example/original.mp3": original, "https://audio.example/full.mp3": reference}}
	j := fetchStart(t, s)
	if len(j.Candidates) != 1 || !strongCensorComparison(j.Candidates[0].Result) {
		t.Fatalf("real engine missing comparison/restoration evidence %+v", j.Result)
	}
	controlAPI(t, s, map[string]any{"action": "approve", "id": j.ID, "candidate_id": "candidate-1"}, 200)
	waitCensor(t, s, "succeeded")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mutations != 1 {
		t.Fatal("manual real-engine publication", f.mutations)
	}
}

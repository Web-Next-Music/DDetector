package censor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
)

func (e *Engine) fingerprint(ctx context.Context, path, dir string) (*Fingerprint, error) {
	seconds := math.Min(120, e.config.MaxDuration.Seconds())
	cmd := exec.CommandContext(ctx, e.config.FPcalcCommand, "-json", "-raw", "-algorithm", "2", "-length", strconv.FormatFloat(seconds, 'f', 3, 64), path)
	cmd.Dir = dir
	boundCommand(cmd)
	out, stderr := &cappedBuffer{limit: e.config.MaxFingerprintBytes}, &cappedBuffer{limit: 2048}
	cmd.Stdout = out
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("fpcalc: %w: %s", err, strings.TrimSpace(string(stderr.data)))
	}
	if out.overflow {
		return nil, errors.New("fpcalc output exceeds limit")
	}
	var response struct {
		Duration    float64  `json:"duration"`
		Fingerprint []uint32 `json:"fingerprint"`
	}
	if err := json.Unmarshal(out.data, &response); err != nil {
		return nil, fmt.Errorf("fpcalc JSON: %w", err)
	}
	if len(response.Fingerprint) < 24 || len(response.Fingerprint) > int(seconds*9)+64 || response.Duration <= 0 || math.IsNaN(response.Duration) || math.IsInf(response.Duration, 0) {
		return nil, errors.New("invalid or too short Chromaprint fingerprint")
	}
	return &Fingerprint{Algorithm: "chromaprint-2", DurationSeconds: response.Duration, AnalyzedSeconds: math.Min(seconds, response.Duration), Values: response.Fingerprint}, nil
}

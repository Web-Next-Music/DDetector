package censor

import (
	"math"
)

func waveform(f features, limit int) []WaveformPoint {
	out := make([]WaveformPoint, 0, min(limit, len(f.frames)))
	if len(f.frames) == 0 || limit < 1 {
		return out
	}
	group := (len(f.frames) + limit - 1) / limit
	var sample int64
	for start := 0; start < len(f.frames); start += group {
		point := WaveformPoint{StartSeconds: float64(sample) / float64(f.rate), Min: 1, Max: -1}
		sum := 0.0
		count := 0
		for i := start; i < min(start+group, len(f.frames)); i++ {
			v := f.frames[i]
			n := v.count
			if n == 0 {
				n = frameSamples
			}
			point.Min = math.Min(point.Min, v.min)
			point.Max = math.Max(point.Max, v.max)
			sum += v.energy * float64(n)
			count += n
			sample += int64(n)
		}
		point.EndSeconds = math.Min(float64(sample)/float64(f.rate), float64(f.samples)/float64(f.rate))
		point.RMS = math.Sqrt(sum / float64(count))
		out = append(out, point)
	}
	return out
}

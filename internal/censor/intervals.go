package censor

import (
	"math"
	"sort"
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

func detectIntervals(f features, limit int) ([]SuspiciousInterval, bool) {
	out := make([]SuspiciousInterval, 0)
	if len(f.frames) < 8 {
		return out, false
	}
	step := float64(frameSamples) / float64(f.rate)
	energies := make([]float64, 0, len(f.frames))
	for _, v := range f.frames {
		if v.energy > 1e-7 {
			energies = append(energies, v.energy)
		}
	}
	if len(energies) == 0 {
		return out, false
	}
	sort.Float64s(energies)
	active := energies[len(energies)/2]
	quiet := math.Max(1e-9, math.Min(1e-5, active*.001))
	out = append(out, detectSilences(f, active, quiet)...)
	interval := func(start, end int, reason string, confidence float64) SuspiciousInterval {
		return SuspiciousInterval{StartSeconds: float64(start) * step, EndSeconds: math.Min(float64(end)*step, float64(f.samples)/float64(f.rate)), Reason: reason, Confidence: math.Min(.9, confidence), Status: "suspicious"}
	}

	type toneEvent struct {
		start, end int
		hz, ratio  float64
	}
	tones := make([]toneEvent, 0)
	isTone := func(v frame) bool {
		return v.energy > math.Max(1e-6, active*.1) && v.toneRatio >= .86 && v.toneHz >= 650 && v.toneHz <= 4000
	}
	for i := 0; i < len(f.frames); {
		if !isTone(f.frames[i]) {
			i++
			continue
		}
		start := i
		hz := f.frames[i].toneHz
		ratio := 0.0
		for i < len(f.frames) && isTone(f.frames[i]) && math.Abs(f.frames[i].toneHz-hz) <= float64(f.rate)/frameSamples*2 {
			ratio += f.frames[i].toneRatio
			i++
		}
		end := i
		duration := float64(end-start) * step
		if duration < .10 || duration > 2 || start < 2 || end+2 >= len(f.frames) {
			continue
		}
		prev := f.frames[start-2]
		next := f.frames[end+1]
		// A continuous instrument/hum, including amplitude steps, has no new tone.
		newBefore := prev.toneRatio < .5 || math.Abs(prev.toneHz-hz) > 100 || prev.energy < f.frames[start].energy*.02
		newAfter := next.toneRatio < .5 || math.Abs(next.toneHz-hz) > 100 || next.energy < f.frames[end-1].energy*.02
		if !newBefore || !newAfter {
			continue
		}
		tones = append(tones, toneEvent{start, end, hz, ratio / float64(end-start)})
	}
	for i, tone := range tones {
		repeated := false
		for j := i - 1; j >= 0; j-- {
			other := tones[j]
			if float64(tone.start-other.start)*step > 15 {
				break
			}
			if math.Abs(other.hz-tone.hz) < 50 {
				repeated = true
				break
			}
		}
		if !repeated {
			for j := i + 1; j < len(tones); j++ {
				other := tones[j]
				if float64(other.start-tone.start)*step > 15 {
					break
				}
				if math.Abs(other.hz-tone.hz) < 50 {
					repeated = true
					break
				}
			}
		}
		reason := "narrowband_beep"
		confidence := .55 + .15*clamp((tone.ratio-.86)/.14)
		if repeated {
			reason = "repeated_narrowband_beep"
			confidence += .15
		}
		out = append(out, interval(tone.start, tone.end, reason, confidence))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartSeconds < out[j].StartSeconds })
	truncated := len(out) > limit
	if truncated {
		out = out[:limit]
	}
	return out, truncated
}

// Use 128-sample energy blocks for cut localization without retaining PCM.
func detectSilences(f features, active, quiet float64) []SuspiciousInterval {
	out := make([]SuspiciousInterval, 0)
	const chunk = 128
	count := min(int((f.samples+chunk-1)/chunk), len(f.frames)*8)
	step := float64(chunk) / float64(f.rate)
	neighbor := max(3, int(math.Ceil(.2/step)))
	energy := func(i int) float64 { return float64(f.frames[i/8].fineEnergy[i%8]) }
	mean := func(start, end int) float64 {
		if start < 0 || end > count || end <= start {
			return 0
		}
		sum := 0.0
		for i := start; i < end; i++ {
			sum += energy(i)
		}
		return sum / float64(end-start)
	}
	for i := 0; i < count; {
		if energy(i) > quiet {
			i++
			continue
		}
		start := i
		for i < count && energy(i) <= quiet {
			i++
		}
		end := i
		duration := float64(end-start) * step
		if duration < .08 || duration > 1.2 || start < neighbor+2 || end+neighbor+2 > count {
			continue
		}
		before, after := mean(start-neighbor, start), mean(end, end+neighbor)
		nearBefore := math.Max(energy(start-1), energy(start-2))
		nearAfter := math.Max(energy(end), energy(end+1))
		farBefore, farAfter := mean(start-neighbor-2, start-2), mean(end+2, end+neighbor+2)
		if before < active*.12 || after < active*.12 || nearBefore < farBefore*.35 || nearAfter < farAfter*.35 || math.Min(before, after)/math.Max(before, after) < .12 {
			continue
		}
		contrast := math.Min(before, after) / math.Max(quiet, mean(start, end))
		out = append(out, SuspiciousInterval{StartSeconds: float64(start) * step, EndSeconds: math.Min(float64(end)*step, float64(f.samples)/float64(f.rate)), Reason: "abrupt_short_silence", Confidence: .55 + .25*clamp(math.Log10(contrast)/4), Status: "suspicious"})
	}
	return out
}

func localRestoration(a, b features, c Comparison, intervals []SuspiciousInterval) {
	if !c.Comparable || c.ConfidenceClass != "strong_match" {
		return
	}
	step := float64(frameSamples) / float64(a.rate)
	lag := int(math.Round(c.OffsetSeconds / step))
	for i := range intervals {
		candidate := &intervals[i]
		start := int(math.Round(candidate.StartSeconds / step))
		end := int(math.Round(candidate.EndSeconds / step))
		rs, re := start-lag, end-lag
		if rs < 0 || re > len(b.frames) || re <= rs {
			continue
		}
		// Agreement must hold on BOTH sides locally, not just somewhere in the song.
		valid, total := 0, 0
		sideGood := [2]int{}
		for side := 0; side < 2; side++ {
			lo, hi := start-8, start
			if side == 1 {
				lo, hi = end, end+8
			}
			for j := lo; j < hi; j++ {
				k := j - lag
				if j < 0 || j >= len(a.frames) || k < 0 || k >= len(b.frames) {
					continue
				}
				if a.frames[j].energy < 1e-6 || b.frames[k].energy < 1e-6 {
					continue
				}
				total++
				if spectralCosine(a.frames[j], b.frames[k]) > .92 {
					valid++
					sideGood[side]++
				}
			}
		}
		if total < 8 || sideGood[0] < 3 || sideGood[1] < 3 || float64(valid)/float64(total) < .85 {
			continue
		}
		restored := 0
		for j := start; j < end && j < len(a.frames); j++ {
			k := j - lag
			if k < 0 || k >= len(b.frames) {
				continue
			}
			source, ref := a.frames[j], b.frames[k]
			if ref.energy < 1e-5 {
				continue
			}
			if candidate.Reason == "abrupt_short_silence" {
				if ref.energy > math.Max(source.energy*20, 1e-5) {
					restored++
				}
			} else if ref.toneRatio < .65 && spectralCosine(source, ref) < .75 {
				restored++
			}
		}
		fraction := float64(restored) / float64(end-start)
		if fraction < .7 {
			continue
		}
		candidate.Status = "reference_restoration_candidate"
		candidate.Restoration = &RestorationEvidence{ReferenceStartSeconds: math.Max(0, candidate.StartSeconds-c.OffsetSeconds), ReferenceEndSeconds: math.Min(candidate.EndSeconds-c.OffsetSeconds, float64(b.samples)/float64(b.rate)), Confidence: math.Min(.9, fraction*c.Confidence), Reason: "matching_local_neighbors_with_reference_content_inside_candidate"}
	}
}

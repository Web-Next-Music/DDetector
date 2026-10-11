package censor

import (
	"context"
	"math"
	"math/bits"
)

func spectralCosine(a, b frame) float64 {
	dot, aa, bb := 0.0, 0.0, 0.0
	for i, u := range a.spectrum {
		v := b.spectrum[i]
		dot += float64(u) * float64(v)
		aa += float64(u) * float64(u)
		bb += float64(v) * float64(v)
	}
	if aa*bb < 1e-12 {
		return 0
	}
	return clamp(dot / math.Sqrt(aa*bb))
}
func compare(ctx context.Context, a, b features) (Comparison, error) {
	c, err := correlateEnergy(ctx, a, b)
	if err != nil {
		return c, err
	}
	c.Comparable = false
	c.ConfidenceClass = "insufficient_evidence"
	c.Confidence = 0
	if a.rate != b.rate || a.rate <= 0 {
		return c, nil
	}
	step := float64(frameSamples) / float64(a.rate)
	center := int(math.Round(c.OffsetSeconds / step))
	best, match, lag, overlap := -1.0, 0.0, center, 0
	for shift := center - 4; shift <= center+4; shift++ {
		if err := ctx.Err(); err != nil {
			return c, err
		}
		start, end := max(0, shift), min(len(a.frames), len(b.frames)+shift)
		if end-start < int(3/step) {
			continue
		}
		stride := max(1, (end-start)/2048)
		sum := 0.0
		count, matched := 0, 0
		for i := start; i < end; i += stride {
			x, y := a.frames[i], b.frames[i-shift]
			if x.energy < 1e-6 || y.energy < 1e-6 {
				continue
			}
			v := spectralCosine(x, y)
			sum += v
			count++
			if v >= .90 {
				matched++
			}
		}
		if count < max(8, int(2/step)/stride) {
			continue
		}
		score := sum / float64(count)
		// Prefer the energy-derived alignment unless structural evidence improves.
		if score > best+.0001 || (math.Abs(score-best) <= .0001 && absInt(shift-center) < absInt(lag-center)) {
			best, match, lag, overlap = score, float64(matched)/float64(count), shift, end-start
		}
	}
	if best < 0 {
		return c, nil
	}
	c.SpectralSimilarity, c.SpectralMatchFraction = best, match
	c.OffsetSeconds = float64(lag) * step
	c.OverlapSeconds = float64(overlap) * step
	if best < .82 || match < .5 {
		c.ConfidenceClass = "different_recording"
		c.Confidence = clamp(1 - best)
		return c, nil
	}
	if best < .93 || match < .8 || c.Correlation < .5 {
		c.ConfidenceClass = "possible_match"
		c.Confidence = math.Min(.65, best*match)
		return c, nil
	}
	c.Comparable = true
	c.ConfidenceClass = "strong_match"
	c.Confidence = math.Min(.95, best*match*clamp(c.OverlapSeconds/6)*(.7+.3*c.Correlation))
	return c, nil
}
func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// Raw Chromaprint words are aligned at their native ~0.124s hop. Hashes alone
// cannot establish censoring: local DSP restoration evidence is still required.
func fingerprintSimilarity(a, b *Fingerprint, offset float64) (float64, bool) {
	if a == nil || b == nil || a.Algorithm != b.Algorithm {
		return 0, false
	}
	const hop = 4096.0 / (11025.0 * 3)
	center := int(math.Round(offset / hop))
	best := -1.0
	for lag := center - 3; lag <= center+3; lag++ {
		start, end := max(0, lag), min(len(a.Values), len(b.Values)+lag)
		if end-start < 24 {
			continue
		}
		sum := 0.0
		for i := start; i < end; i++ {
			sum += 1 - float64(bits.OnesCount32(a.Values[i]^b.Values[i-lag]))/32
		}
		score := sum / float64(end-start)
		if score > best {
			best = score
		}
	}
	return math.Max(0, best), best >= 0
}
func applyFingerprint(c *Comparison, a, b *Fingerprint) {
	score, ok := fingerprintSimilarity(a, b, c.OffsetSeconds)
	if !ok {
		return
	}
	c.FingerprintSimilarity = &score
	if score < .72 {
		c.Comparable = false
		c.ConfidenceClass = "different_recording"
		c.Confidence = math.Max(c.Confidence, 1-score)
	} else if score < .85 && c.Comparable {
		c.Comparable = false
		c.ConfidenceClass = "possible_match"
		c.Confidence = math.Min(c.Confidence, .65)
	}
}

package censor

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"math/cmplx"
)

const spectralBands = 32
const frameSamples = 1024

type frame struct {
	energy            float64
	min, max          float64
	count             int
	spectrum          [spectralBands]float32
	fineEnergy        [8]float32
	toneRatio, toneHz float64
}
type features struct {
	frames  []frame
	samples int64
	rate    int
}

func fft(x []complex128) {
	n := len(x)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			x[i], x[j] = x[j], x[i]
		}
	}
	for size := 2; size <= n; size <<= 1 {
		w := cmplx.Rect(1, -2*math.Pi/float64(size))
		for start := 0; start < n; start += size {
			z := complex(1, 0)
			for j := 0; j < size/2; j++ {
				a, b := x[start+j], z*x[start+j+size/2]
				x[start+j], x[start+j+size/2] = a+b, a-b
				z *= w
			}
		}
	}
}

// DSP consumes PCM incrementally; retained features are one frame per 64ms at 16kHz.
func extract(ctx context.Context, r io.Reader, rate int, maxSamples int64, wav io.Writer) (features, error) {
	const n = frameSamples
	f := features{rate: rate}
	buf := make([]byte, n*2)
	x := make([]float64, n)
	spec := make([]complex128, n)
	powers := make([]float64, n/2)
	var window [n]float64
	var bandForBin [n / 2]int
	bandScale := math.Log2(float64(rate) / 2 / 60)
	for i := range window {
		window[i] = .5 - .5*math.Cos(2*math.Pi*float64(i)/float64(n-1))
	}
	for k := 1; k < n/2; k++ {
		hz := float64(k*rate) / n
		bin := int(math.Log2(math.Max(60, hz)/60) / bandScale * spectralBands)
		bandForBin[k] = max(0, min(spectralBands-1, bin))
	}

	for {
		if err := ctx.Err(); err != nil {
			return f, err
		}
		count, err := io.ReadFull(r, buf)
		if count%2 != 0 {
			return f, errors.New("censor: truncated PCM sample")
		}
		if count == 0 && err == io.EOF {
			break
		}
		if err != nil && err != io.ErrUnexpectedEOF {
			return f, err
		}
		if f.samples+int64(count/2) > maxSamples {
			return f, errors.New("censor: decoded duration exceeds limit")
		}
		if wav != nil {
			if _, e := wav.Write(buf[:count]); e != nil {
				return f, e
			}
		}
		for i := range x {
			x[i] = 0
		}
		energy := 0.0
		fr := frame{count: count / 2, min: 1, max: -1}
		for i := 0; i < count/2; i++ {
			v := float64(int16(binary.LittleEndian.Uint16(buf[2*i:]))) / 32768
			x[i] = v
			energy += v * v
			fr.fineEnergy[i/128] += float32(v * v)
			fr.min = math.Min(fr.min, v)
			fr.max = math.Max(fr.max, v)
		}
		for j := range fr.fineEnergy {
			samples := min(128, fr.count-j*128)
			if samples > 0 {
				fr.fineEnergy[j] /= float32(samples)
			}
		}
		f.samples += int64(count / 2)

		energy /= float64(count / 2)
		for i, v := range x {
			spec[i] = complex(v*window[i], 0)
		}
		fft(spec)
		total := 0.0
		peak, peakBin := 0.0, 0
		var bands [spectralBands]float64
		for k := 1; k < n/2; k++ {
			p := cmplx.Abs(spec[k])
			p *= p
			total += p
			powers[k] = p
			if p > peak {
				peak, peakBin = p, k
			}
			// Log-frequency bands distinguish recordings even with identical envelopes.
			bands[bandForBin[k]] += p
		}
		fr.energy = energy
		if total > 1e-12 {
			for i, p := range bands {
				fr.spectrum[i] = float32(math.Sqrt(p / total))
			}
			for k := peakBin - 1; k <= peakBin+1; k++ {
				if k > 0 && k < len(powers) {
					fr.toneRatio += powers[k] / total
				}
			}
			fr.toneHz = float64(peakBin*rate) / n
		}
		f.frames = append(f.frames, fr)
		if err == io.ErrUnexpectedEOF {
			break
		}
	}
	if f.samples == 0 {
		return f, errors.New("censor: no decoded audio")
	}
	return f, nil
}
func clamp(v float64) float64 { return math.Max(0, math.Min(1, v)) }

func correlateEnergy(ctx context.Context, a, b features) (Comparison, error) {
	// Downsample energy envelopes to 256ms at 16kHz, bounding correlation cost.
	envelope := func(f features) []float64 {
		out := make([]float64, 0, (len(f.frames)+3)/4)
		for i := 0; i < len(f.frames); i += 4 {
			v := 0.0
			for j := i; j < i+4 && j < len(f.frames); j++ {
				v += f.frames[j].energy
			}
			out = append(out, math.Log1p(v*1000))
		}
		return out
	}
	x, y := envelope(a), envelope(b)
	step := 4096 / float64(a.rate)
	minOverlap := int(math.Ceil(3 / step))
	best, second := -1.0, -1.0
	bestLag, bestN := 0, 0
	// Search +/- 120s; coarse stride limits work, then refine around the best offset.
	limit := int(120 / step)
	stride := 1
	if len(x)+len(y) > 10000 {
		stride = 4
	}
	eval := func(lag int) (float64, int) {
		start := 0
		if lag > 0 {
			start = lag
		}
		end := len(x)
		if len(y)+lag < end {
			end = len(y) + lag
		}
		n := end - start
		if n < minOverlap {
			return -1, 0
		}
		sx, sy, sxx, syy, sxy := 0.0, 0.0, 0.0, 0.0, 0.0
		for i := start; i < end; i += stride {
			u, v := x[i], y[i-lag]
			sx += u
			sy += v
			sxx += u * u
			syy += v * v
			sxy += u * v
		}
		m := float64((n + stride - 1) / stride)
		den := (sxx - sx*sx/m) * (syy - sy*sy/m)
		if den < 1e-12 {
			return -1, 0
		}
		return clamp((sxy - sx*sy/m) / math.Sqrt(den)), n
	}
	for lag := -limit; lag <= limit; lag += stride {
		if err := ctx.Err(); err != nil {
			return Comparison{}, err
		}
		v, n := eval(lag)
		if v > best {
			second = best
			best, bestLag, bestN = v, lag, n
		} else if v > second {
			second = v
		}
	}
	coarse := bestLag
	for lag := coarse - stride + 1; lag < coarse+stride; lag++ {
		v, n := eval(lag)
		if v > best {
			best, bestLag, bestN = v, lag, n
		}
	}
	if best < 0 {
		return Comparison{}, nil
	}
	confidence := clamp(best) * clamp(float64(bestN)*step/10) * clamp((best-second)*5+.1)
	return Comparison{OffsetSeconds: float64(bestLag) * step, Correlation: best, Confidence: confidence, OverlapSeconds: float64(bestN) * step, Comparable: best >= .5 && bestN >= minOverlap}, nil
}

#pragma once

#include <algorithm>
#include <array>
#include <cmath>
#include <deque>

// Port of spotifast/src/vis.rs Fft, bands and WideAnalyser. Float arithmetic,
// fractional band edges and peak hold ordering intentionally match the source.
namespace Spectrum {
constexpr int Samples = 512, Bins = 256, Bands = 75;
constexpr float Step = 0.016667f;
constexpr float Fall = (12.0f / 16.0f) / 15.0f / Step;
constexpr float Tau = 6.28318530717958647692f;
using Wave = std::array<float, Samples>;
using Levels = std::array<float, Bands>;

class Fft {
    std::array<int, Samples> reversed{};
    Wave envelope{}, real{}, imaginary{};
    std::array<std::array<float, 2>, 9> twiddles{};
public:
    Fft() {
        for (int i = 0; i < Samples; ++i) reversed[i] = i;
        int j = 0;
        for (int i = 0; i < Samples; ++i) {
            if (j > i) std::swap(reversed[i], reversed[j]);
            int m = Samples >> 1;
            while (m >= 1 && j >= m) { j -= m; m >>= 1; }
            j += m;
            const float phase = float(i) / float(Samples) * Tau;
            envelope[i] = 0.5f + 0.5f * std::sin(phase - Tau / 4.0f);
        }
        int stage = 0;
        for (int size = 2; size <= Samples; size <<= 1) {
            const float theta = -Tau / float(size);
            twiddles[stage++] = {std::cos(theta), std::sin(theta)};
        }
    }
    std::array<float, Bins> spectrum(const Wave &wave) {
        for (int i = 0; i < Samples; ++i) {
            const int from = reversed[i];
            real[i] = wave[from] * envelope[from];
            imaginary[i] = 0.0f;
        }
        int stage = 0;
        for (int size = 2; size <= Samples; size <<= 1) {
            const auto w = twiddles[stage++];
            float wr = 1.0f, wi = 0.0f;
            const int half = size >> 1;
            for (int m = 0; m < half; ++m) {
                for (int i = m; i < Samples; i += size) {
                    const int j = i + half;
                    const float tr = wr * real[j] - wi * imaginary[j];
                    const float ti = wr * imaginary[j] + wi * real[j];
                    real[j] = real[i] - tr;
                    imaginary[j] = imaginary[i] - ti;
                    real[i] += tr;
                    imaginary[i] += ti;
                }
                const float previous = wr;
                wr = wr * w[0] - wi * w[1];
                wi = wi * w[0] + previous * w[1];
            }
        }
        std::array<float, Bins> out{};
        for (int i = 0; i < Bins; ++i)
            out[i] = std::sqrt(real[i] * real[i] + imaginary[i] * imaginary[i]) * 0.5f;
        return out;
    }
};

inline Levels bands(const std::array<float, Bins> &spectrum) {
    const float bla = 255.0f / std::pow(2.0f, 75.0f / 12.0f);
    const auto warp = [bla](float x) { return (std::pow(2.0f, x / 12.0f) - 1.0f) * bla; };
    const auto sample = [&](int i) { return i < Bins ? spectrum[i] : 0.0f; };
    const auto hermite = [](float x, float y0, float y1, float y2, float y3) {
        const float c1 = 0.5f * (y2 - y0);
        const float c3 = 1.5f * (y1 - y2) + 0.5f * (y3 - y0);
        const float c2 = y0 - y1 + c1 - c3;
        return ((c3 * x + c2) * x + c1) * x + y1;
    };
    Levels columns{};
    float next = warp(0.0f) + 1.0f;
    for (int x = 0; x < Bands; ++x) {
        const float low = next;
        next = warp(float(x) + 1.0f) + 1.0f;
        float value = 0.0f;
        int bin = int(std::floor(low));
        const int end = std::min(int(std::floor(next)), Bins - 1);
        float fraction = low, mult = float(bin) + 1.0f - low;
        bool herm = true;
        for (;;) {
            if (bin == end) { mult = next - fraction; herm = true; }
            if (herm)
                value += hermite(fraction - float(bin), sample(std::max(bin - 1, 0)),
                                 sample(bin), sample(bin + 1), sample(bin + 2)) * mult;
            else value += sample(bin);
            herm = false;
            if (++bin > end) break;
            fraction = float(bin);
        }
        columns[x] = std::min(value, 255.0f);
    }
    return columns;
}

class WideAnalyser {
    Fft fft;
    Levels held{}, speed{};
    double last = -1.0;
public:
    Levels levels{}, peaks{};
    void reset() { levels = {}; peaks = {}; held = {}; speed = {}; last = -1.0; }
    void step(const Wave &samples, double now) {
        const float elapsed = float(std::min(last < 0.0 ? double(Step) : std::max(now - last, 0.0), 0.25));
        last = now;
        Wave wave{};
        for (int i = 0; i < Samples; ++i) wave[i] = samples[i] * 2.0f;
        const auto columns = bands(fft.spectrum(wave));
        for (int i = 0; i < Bands; ++i) {
            const float target = std::min(columns[i], 15.0f) / 15.0f;
            levels[i] = std::max(levels[i] - Fall * elapsed, target);
            if (levels[i] < 0.01f) levels[i] = 0.0f;
        }
        for (int i = 0; i < Bands; ++i) {
            if (levels[i] >= peaks[i]) { peaks[i] = levels[i]; held[i] = speed[i] = 0.0f; }
            else if (held[i] < 0.35f) held[i] += elapsed;
            else {
                speed[i] += 2.4f * elapsed;
                peaks[i] = std::max(peaks[i] - speed[i] * elapsed, levels[i]);
            }
            if (peaks[i] < 0.01f) peaks[i] = 0.0f;
        }
    }
    bool settled() const {
        return std::all_of(levels.begin(), levels.end(), [](float x) { return x == 0.0f; })
            && std::all_of(peaks.begin(), peaks.end(), [](float x) { return x == 0.0f; });
    }
};

class AudioTap {
    std::deque<float> samples;
    int rate = 0;
public:
    void clear() { samples.clear(); rate = 0; }
    void push(float mono, int sampleRate) {
        if (rate != sampleRate) { clear(); rate = sampleRate; }
        if (rate <= 0) return;
        if (samples.size() >= size_t(std::max(rate / 2, 1))) samples.pop_front();
        samples.push_back(mono);
    }
    Wave window() const {
        Wave out{};
        const size_t lag = size_t(rate) * 3 / 20;
        const size_t end = samples.size() > lag ? samples.size() - lag : 0;
        const size_t start = end > Samples ? end - Samples : 0;
        for (size_t i = start; i < end; ++i) out[Samples - (end - start) + i - start] = samples[i];
        return out;
    }
};
}

pragma ComponentBehavior: Bound
import QtQuick

// Port of spotifast's src/ui/player_bar.rs spectrum renderer (MIT).
// The panel stays untouched; only the palette source changes to the app accent.
Canvas {
    id: visualizer
    property color accent: "#2bfef5"
    property var levels: []
    property var peaks: []
    readonly property var spectrumPalette: spectrumColours(accent)
    Accessible.ignored: true
    renderStrategy: Canvas.Cooperative
    onLevelsChanged: requestPaint()
    onPeaksChanged: requestPaint()
    onSpectrumPaletteChanged: requestPaint()
    onVisibleChanged: if (visible) requestPaint()
    onWidthChanged: requestPaint()
    onHeightChanged: requestPaint()

    function spectrumColours(base) {
        const maximum = Math.max(base.r, base.g, base.b);
        const minimum = Math.min(base.r, base.g, base.b);
        const delta = maximum - minimum;
        let hue = 0;
        if (delta > 0) {
            if (maximum === base.r) hue = ((base.g - base.b) / delta) % 6;
            else if (maximum === base.g) hue = (base.b - base.r) / delta + 2;
            else hue = (base.r - base.g) / delta + 4;
            hue = (hue / 6 + 1) % 1;
        }
        const saturation = Math.max(maximum > 0 ? delta / maximum : 0, 0.55);
        function colour(h) {
            const sector = h * 6;
            const x = saturation * (1 - Math.abs(sector % 2 - 1));
            const m = 1 - saturation;
            const rgb = sector < 1 ? [saturation, x, 0] : sector < 2 ? [x, saturation, 0]
                      : sector < 3 ? [0, saturation, x] : sector < 4 ? [0, x, saturation]
                      : sector < 5 ? [x, 0, saturation] : [saturation, 0, x];
            function linear(v) { return v <= 0.04045 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); }
            function srgb(v) { return v <= 0.0031308 ? v * 12.92 : 1.055 * Math.pow(v, 1 / 2.4) - 0.055; }
            const values = rgb.map(v => linear(v + m));
            const luminance = 0.2126 * values[0] + 0.7152 * values[1] + 0.0722 * values[2];
            const scale = luminance > 0.35 ? 0.35 / luminance : 1;
            return values.map(v => Math.round(srgb(v * scale) * 255));
        }
        return [colour(hue), colour((hue + 1 / 6) % 1)];
    }
    function rgba(rgb, alpha) {
        return "rgba(" + rgb[0] + "," + rgb[1] + "," + rgb[2] + "," + alpha + ")";
    }
    function shaded(ctx, x, y, w, h, rgb, top, bottom) {
        if (w <= 0 || h <= 0) return;
        const gradient = ctx.createLinearGradient(0, y, 0, y + h);
        gradient.addColorStop(0, rgba(rgb, top));
        gradient.addColorStop(1, rgba(rgb, bottom));
        ctx.fillStyle = gradient;
        ctx.fillRect(x, y, w, h);
    }
    onPaint: {
        const ctx = getContext("2d");
        ctx.clearRect(0, 0, width, height);
        if (!visible || levels.length < 2) return;
        const bands = levels.length;
        const gap = 2;
        const bandWidth = (width - gap * (bands - 1)) / bands;
        const reach = height - 5;
        const bassBands = Math.max(1, Math.floor(bands / 8));
        let bass = 0;
        for (let b = 0; b < bassBands; ++b) bass += levels[b];
        bass /= bassBands;
        shaded(ctx, 0, height / 2, width, height / 2, spectrumPalette[0], 0, 0.35 * bass * bass);
        for (let i = 0; i < bands; ++i) {
            const t = i / (bands - 1);
            const rgb = spectrumPalette[0].map((v, channel) => Math.round(v + (spectrumPalette[1][channel] - v) * t));
            const left = i * (bandWidth + gap);
            const level = levels[i];
            const barHeight = level * reach;
            if (barHeight >= 1) {
                shaded(ctx, left - gap, height - barHeight - 3, bandWidth + gap * 2,
                       barHeight + 6, rgb, 0.12 * 0.3, 0.12);
                shaded(ctx, left, height - barHeight, bandWidth, barHeight,
                       rgb, 0.28 + (0.08 - 0.28) * level, 0.28);
            }
            const cap = (peaks[i] || 0) * reach;
            if (cap >= 2) {
                ctx.fillStyle = rgba(rgb, 0.7);
                ctx.fillRect(left, height - cap - 4, bandWidth, 2);
            }
        }
    }
}

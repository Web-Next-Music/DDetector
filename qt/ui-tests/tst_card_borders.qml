import QtQuick
import QtTest
import "qrc:/" as UI

Item {
    width: 600
    height: 400

    Component { id: settingsFactory; UI.SiteSettingsCard {} }
    Component { id: sectionFactory; UI.SiteSectionCard {} }
    Component { id: headerFactory; UI.DialogHeader { closeVisible: false } }
    Component { id: detectorFactory; UI.CensorDetector {} }

    TestCase {
        name: "CardBorders"
        when: windowShown

        function test_analysisContainer() {
            const detector = createTemporaryObject(detectorFactory, parent, {width: 600, height: 400});
            verify(detector);
            const analysis = findChild(detector, "censorAnalysisProgress");
            verify(analysis);
            compare(analysis.border.width, 1);
            compare(analysis.border.pixelAligned, false);
            compare(analysis.antialiasing, true);
            compare(analysis.layer.enabled, false);
        }

        function test_corners_data() {
            return [
                {tag: "settings", factory: settingsFactory},
                {tag: "section", factory: sectionFactory},
                {tag: "header", factory: headerFactory}
            ];
        }

        function test_corners(data) {
            // Integral physical bounds at 100/125/150/200% isolate stroke
            // rasterization from unequal coverage at fractional item edges.
            const card = createTemporaryObject(data.factory, parent, {width: 240, height: 120});
            verify(card);
            verify(waitForRendering(card));
            const background = data.tag === "header" ? card.children[0] : card;
            compare(background.border.width, 1);
            compare(background.border.pixelAligned, false);
            compare(background.antialiasing, true);
            compare(card.layer.enabled, false);
            const image = grabImage(card);
            // At fractional DPR, the default snapping must visibly differ from
            // the requested 1-DIP stroke; at integer DPR it must be identical.
            background.border.pixelAligned = true;
            verify(waitForRendering(card));
            const snapped = grabImage(card);
            const dpr = card.Screen.devicePixelRatio;
            compare(image.equals(snapped), Math.abs(dpr - Math.round(dpr)) < 0.001);
            background.border.pixelAligned = false;
            // Compare mirrored corner coverage, not a platform-specific golden image.
            for (let y = 0; y < 18; ++y) {
                for (let x = 0; x < 18; ++x) {
                    const a = image.pixel(x, y);
                    const b = image.pixel(image.width - 1 - x, y);
                    const c = image.pixel(x, image.height - 1 - y);
                    verify(Math.abs(a.r - b.r) < 0.025 && Math.abs(a.r - c.r) < 0.025,
                           data.tag + " asymmetric corner at " + x + "," + y
                           + ": " + a + " / " + b + " / " + c);
                }
            }
        }
    }
}

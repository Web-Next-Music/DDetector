pragma ComponentBehavior: Bound
import QtQuick
import QtQuick.Effects

Image {
    id: icon

    property string glyph: ""
    property url assetRoot: "qrc:/assets/"
    property url iconSource: ""
    property color tintColor: "#999999"
    property bool tinted: true
    readonly property bool hasIconSource: iconSource.toString().length > 0

    source: hasIconSource ? iconSource : (glyph ? assetRoot + "lucide-" + glyph + ".svg" : "")
    layer.enabled: tinted
    layer.effect: MultiEffect {
        brightness: 1
        colorization: 1
        colorizationColor: icon.tintColor
    }
    Accessible.ignored: true
}

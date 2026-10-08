import QtQuick
import QtQuick.Controls
import QtQuick.Effects

Button {
    id: control
    property string glyph: ""
    property bool accented: false
        property bool iconOnly: false
    implicitHeight: 30
    implicitWidth: contentItem.implicitWidth + leftPadding + rightPadding
    leftPadding: 10
    rightPadding: 10
    font.family: "DDetector Nunito"
    font.pixelSize: 12
    opacity: enabled ? 1 : 0.45
    Accessible.name: text
    HoverHandler { cursorShape: control.enabled ? Qt.PointingHandCursor : Qt.ArrowCursor }
    contentItem: Row {
        spacing: control.glyph && !control.iconOnly ? 6 : 0
        Image {
            visible: !!control.glyph
            width: visible ? 14 : 0; height: 14
            anchors.verticalCenter: parent.verticalCenter
            source: control.glyph ? "qrc:/assets/lucide-" + control.glyph + ".svg" : ""
            layer.enabled: true
            layer.effect: MultiEffect { brightness: 1; colorization: 1; colorizationColor: control.hovered || control.accented ? "#2bfef5" : "#999999" }
        }
        Text {
            visible: !control.iconOnly
            text: control.text
            font: control.font
            color: control.hovered || control.accented ? "#2bfef5" : "#999999"
            anchors.verticalCenter: parent.verticalCenter
        }
    }
    background: Rectangle {
        radius: 8
        color: control.hovered || control.accented ? "#142bfef5" : "#0c0c0e"
        border.color: control.hovered || control.activeFocus || control.accented ? "#332bfef5" : "#3397979f"
    }
}

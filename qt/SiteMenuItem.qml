pragma ComponentBehavior: Bound
import QtQuick
import QtQuick.Controls
import QtQuick.Layouts
import QtQuick.Effects

MenuItem {
    id: control
    property string glyph: ""
    property bool danger: false
    property bool green: false
    property color textColor: "#f0f0f4"
    property color mutedColor: "#999999"
    property color accentColor: "#2bfef5"
    property color accentBackground: "#142bfef5"
    property color dangerColor: "#dc5050"
    property color dangerBackground: "#20dc5050"
    property color greenColor: "#35d078"
    property color greenBackground: "#2035d078"

    opacity: enabled ? 1 : 0.45
    HoverHandler { cursorShape: control.enabled ? Qt.PointingHandCursor : Qt.ArrowCursor }
    Accessible.name: text
    font.family: "DDetector Nunito"
    font.pixelSize: 12
    font.weight: Font.Bold
    implicitHeight: 32
    height: visible ? implicitHeight : 0
    contentItem: RowLayout {
        spacing: 8
        Image {
            visible: control.glyph !== ""
            Layout.preferredWidth: 16
            Layout.preferredHeight: 16
            source: control.glyph ? "qrc:/assets/lucide-" + control.glyph + ".svg" : ""
            layer.enabled: true
            layer.effect: MultiEffect {
                brightness: 1
                colorization: 1
                colorizationColor: control.green ? control.greenColor : control.danger ? control.dangerColor : control.highlighted ? control.accentColor : control.mutedColor
            }
        }
        Text {
            textFormat: Text.PlainText
            Layout.fillWidth: true
            text: control.text
            font: control.font
            color: control.green ? control.greenColor : control.danger ? control.dangerColor : control.highlighted ? control.accentColor : control.textColor
            verticalAlignment: Text.AlignVCenter
            elide: Text.ElideRight
        }
    }
    background: Rectangle {
        radius: 6
        color: control.highlighted ? (control.green ? control.greenBackground : control.danger ? control.dangerBackground : control.accentBackground) : "transparent"
    }
}

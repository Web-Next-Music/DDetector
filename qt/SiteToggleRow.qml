pragma ComponentBehavior: Bound
import QtQuick
import QtQuick.Controls
import QtQuick.Layouts
import QtQuick.Effects

CheckBox {
    id: control
    property string glyph: ""
    implicitHeight: 56
    padding: 16
    Accessible.name: text
    HoverHandler {
        cursorShape: control.enabled ? Qt.PointingHandCursor : Qt.ArrowCursor
    }
    indicator: Rectangle {
        implicitWidth: 40
        implicitHeight: 22
        x: control.width - width - control.rightPadding
        y: (control.height - height) / 2
        radius: 11
        color: control.checked ? "#142bfef5" : "#0c0c0e"
        border.color: control.checked ? "#332bfef5" : "#3397979f"
        Rectangle {
            width: 16
            height: 16
            radius: 8
            x: control.checked ? 21 : 3
            y: 3
            color: control.checked ? "#2bfef5" : "#999999"
            Behavior on x {
                NumberAnimation {
                    duration: 160
                }
            }
        }
    }
    contentItem: RowLayout {
        spacing: 12
        Image {
            visible: !!control.glyph
            Layout.preferredWidth: visible ? 18 : 0
            Layout.preferredHeight: 18
            source: control.glyph ? "qrc:/assets/lucide-" + control.glyph + ".svg" : ""
            layer.enabled: true
            layer.effect: MultiEffect {
                brightness: 1
                colorization: 1
                colorizationColor: "#999999"
            }
            Accessible.ignored: true
        }
        Text {
            Layout.fillWidth: true
            Layout.rightMargin: 52
            text: control.text
            textFormat: Text.PlainText
            font: control.font
            color: "#f0f0f4"
            elide: Text.ElideRight
            verticalAlignment: Text.AlignVCenter
        }
    }
    background: Item {}
    SiteFocusRing {
        active: control.visualFocus
    }
}

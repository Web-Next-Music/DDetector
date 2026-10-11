pragma ComponentBehavior: Bound
import QtQuick
import QtQuick.Controls
import QtQuick.Layouts
import QtQuick.Effects

Rectangle {
    id: card
    property string title: ""
    property string glyph: ""
    property string leadingGlyph: ""
    readonly property color leadingGlyphColor: "#999999"
    property bool compact: false
    readonly property int contentPadding: compact ? 12 : 16
    default property alias content: body.data
    implicitHeight: body.implicitHeight + contentPadding * 2
    radius: 14
    color: enabled ? "#171719" : "#101012"
    border.color: enabled ? "#3397979f" : "#2097979f"
    Image {
        id: leadingIcon
        objectName: card.objectName ? card.objectName + "LeadingGlyph" : ""
        visible: !!card.leadingGlyph
        anchors.left: parent.left
        anchors.leftMargin: card.contentPadding
        anchors.top: parent.top
        anchors.topMargin: card.contentPadding + 9
        width: 18
        height: 18
        source: card.leadingGlyph ? "qrc:/assets/lucide-" + card.leadingGlyph + ".svg" : ""
        sourceSize: Qt.size(18, 18)
        layer.enabled: true
        layer.effect: MultiEffect {
            brightness: 1
            colorization: 1
            colorizationColor: card.leadingGlyphColor
        }
        Accessible.ignored: true
    }
    ColumnLayout {
        id: body
        anchors.fill: parent
        anchors.topMargin: card.contentPadding
        anchors.rightMargin: card.contentPadding
        anchors.bottomMargin: card.contentPadding
        anchors.leftMargin: card.contentPadding + (card.leadingGlyph ? 30 : 0)
        spacing: card.compact ? 8 : 12
        opacity: card.enabled ? 1 : 0.45
        RowLayout {
            visible: !!card.title
            Layout.fillWidth: true
            spacing: 8
            Image {
                visible: !!card.glyph
                Layout.preferredWidth: 16
                Layout.preferredHeight: 16
                source: card.glyph ? "qrc:/assets/lucide-" + card.glyph + ".svg" : ""
                sourceSize: Qt.size(16, 16)
                layer.enabled: true
                layer.effect: MultiEffect { brightness: 1; colorization: 1; colorizationColor: "#999999" }
                Accessible.ignored: true
            }
            Label {
                Layout.fillWidth: true
                text: card.title
                textFormat: Text.PlainText
                color: "#f0f0f4"
                font.pixelSize: 13
                wrapMode: Text.WordWrap
            }
        }
    }
}

pragma ComponentBehavior: Bound
import QtQuick
import QtQuick.Controls
import QtQuick.Layouts

Rectangle {
    id: subtitle

    property string title: ""
    property string glyph: ""
    property bool compact: false
    readonly property int contentPadding: compact ? 12 : 16
    readonly property color glyphColor: "#999999"

    implicitHeight: heading.implicitHeight + contentPadding * 2
    color: "transparent"
    border.width: 0
    border.color: "transparent"
    radius: 0

    RowLayout {
        id: heading
        anchors.fill: parent
        anchors.margins: subtitle.contentPadding
        spacing: 8

        SiteIcon {
            objectName: subtitle.objectName ? subtitle.objectName + "Glyph" : ""
            visible: !!subtitle.glyph
            Layout.preferredWidth: 16
            Layout.preferredHeight: 16
            glyph: subtitle.glyph
            sourceSize: Qt.size(16, 16)
            tintColor: subtitle.glyphColor
        }
        Label {
            Layout.fillWidth: true
            text: subtitle.title
            textFormat: Text.PlainText
            color: "#f0f0f4"
            font.pixelSize: 13
            wrapMode: Text.WordWrap
        }
    }
}

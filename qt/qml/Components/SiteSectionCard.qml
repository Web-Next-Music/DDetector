pragma ComponentBehavior: Bound
import QtQuick
import QtQuick.Layouts

Rectangle {
    id: card

    property bool compact: false
    readonly property int contentPadding: compact ? 10 : 12
    default property alias content: body.data

    implicitHeight: body.implicitHeight + contentPadding * 2
    radius: 16
    color: "#0c0c0e"
    border.color: "#242426"
    border.pixelAligned: false

    ColumnLayout {
        id: body
        anchors.fill: parent
        anchors.margins: card.contentPadding
        spacing: 8
    }
}

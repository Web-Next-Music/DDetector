import QtQuick
import QtQuick.Layouts

Rectangle {
    id: root
    required property var ui
    required property var row
    required property int number
    height: 58
    radius: 8
    property bool active: ui.selectedId === row.id
    property bool ignored: !!row.ignored
    color: ignored ? (active ? "#47a01e1e" : hover.hovered ? "#3da01e1e" : "#2ea01e1e") : active ? "#0d2bfef5" : hover.hovered ? ui.surface : "transparent"
    border.color: active ? (ignored ? "#59c83c3c" : ui.accentBorder) : "transparent"
    HoverHandler {
        id: hover
        cursorShape: parent.enabled ? Qt.PointingHandCursor : Qt.ArrowCursor
    }
    TapHandler {
        acceptedButtons: Qt.LeftButton
        onTapped: root.ui.choose(root.row)
    }
    TapHandler {
        acceptedButtons: Qt.RightButton
        onTapped: point => root.ui.context(root.row, root, point.position.x, point.position.y, false)
    }
    RowLayout {
        anchors.fill: parent
        anchors.margins: 10
        spacing: 10
        Text {
            Layout.preferredWidth: 26
            text: root.number
            color: root.ui.muted
            opacity: 0.5
            horizontalAlignment: Text.AlignRight
            font.pixelSize: 12
        }
        Rectangle {
            Layout.preferredWidth: 38
            Layout.preferredHeight: 38
            radius: 6
            color: root.ui.surface2
            clip: true
            Image {
                anchors.centerIn: parent
                width: 15; height: 15
                source: "qrc:/assets/track-placeholder.svg"
                visible: cover.status !== Image.Ready
            }
            Image {
                id: cover
                anchors.fill: parent
                source: root.row.cover ? "image://trackcovers/" + encodeURIComponent(root.row.cover) : ""
                                sourceSize: Qt.size(76, 76)
                asynchronous: true
                fillMode: Image.PreserveAspectCrop
            }
        }
        ColumnLayout {
            Layout.fillWidth: true
            spacing: 2
            Text {
                Layout.fillWidth: true
                text: root.row.title || ""
                color: root.ui.ink
                font.pixelSize: 13
                font.weight: Font.ExtraBold
                elide: Text.ElideRight
            }
            Text {
                Layout.fillWidth: true
                text: root.row.artist || ""
                color: root.ui.muted
                font.pixelSize: 12
                elide: Text.ElideRight
            }
        }
        Text {
            visible: text !== ""
            text: root.row.date || ""
            color: root.ui.muted
            opacity: 0.5
            font.pixelSize: 12
        }
        Rectangle {
            Layout.preferredWidth: 64
            Layout.preferredHeight: 22
            radius: 20
            property bool found: root.row.status === "found"
            color: found ? (root.ignored ? "#1fa01e1e" : root.ui.accentBg) : root.row.status === "pending" ? root.ui.surface2 : "transparent"
            border.color: found ? (root.ignored ? "#4dc83c3c" : root.ui.accentBorder) : root.row.status === "pending" ? root.ui.borderColor : "transparent"
            opacity: root.row.status === "pending" ? 0.5 : 1
            Text {
                anchors.centerIn: parent
                text: ({
                        pending: "…",
                        found: "found",
                        none: "clean",
                        error: "no lyrics"
                    })[root.row.status] || "…"
                color: parent.found ? (root.ignored ? root.ui.ignoredColor : root.ui.accent) : root.ui.muted
                font.pixelSize: root.row.status === "pending" ? 10 : 12
            }
        }
    }
}

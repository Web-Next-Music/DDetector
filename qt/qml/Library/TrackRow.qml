pragma ComponentBehavior: Bound
import QtQuick
import QtQuick.Layouts
import QtQuick.Effects

Rectangle {
    id: root
    required property var ui
    required property var row
    required property int number
    property url placeholderSource: "qrc:/assets/track-placeholder.svg"
        property bool pooled: false
        readonly property var viewport: ListView.view
        readonly property bool inViewport: visible && !pooled && (!viewport ||
            (viewport.visible && y + height > viewport.contentY && y < viewport.contentY + viewport.height))
        ListView.onPooled: pooled = true
        ListView.onReused: pooled = false
    height: 58
    radius: 8
    activeFocusOnTab: true
    Accessible.role: Accessible.ListItem
    Accessible.name: (row.title || "") + ", " + (row.artist || "") + ", " + (row.status || "")
    Accessible.selected: active
    Accessible.onPressAction: ui.choose(row)
    Keys.onReturnPressed: ui.choose(row)
    Keys.onEnterPressed: ui.choose(row)
    Keys.onSpacePressed: ui.choose(row)
    Keys.onMenuPressed: ui.context(row, root, width / 2, height / 2, false)
    Keys.onPressed: function(event) {
        if (event.key === Qt.Key_F10 && (event.modifiers & Qt.ShiftModifier)) {
            ui.context(row, root, width / 2, height / 2, false);
            event.accepted = true;
        } else if ((event.key === Qt.Key_Up || event.key === Qt.Key_Down) && ListView.view) {
            const view = ListView.view;
            view.currentIndex = Math.max(0, Math.min(view.count - 1, number - 1 + (event.key === Qt.Key_Down ? 1 : -1)));
            view.positionViewAtIndex(view.currentIndex, ListView.Contain);
            if (view.currentItem) view.currentItem.forceActiveFocus(Qt.TabFocusReason);
            event.accepted = true;
        }
    }
    SiteFocusRing { active: root.activeFocus }
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
        exclusiveSignals: TapHandler.SingleTap | TapHandler.DoubleTap
        onSingleTapped: root.ui.choose(root.row)
        onDoubleTapped: root.ui.playTrack(root.row)
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
            textFormat: Text.PlainText
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
            layer.enabled: GraphicsInfo.api !== GraphicsInfo.Software
            layer.effect: MultiEffect {
                maskEnabled: true
                maskSource: coverMask
            }
            Rectangle {
                id: coverMask
                anchors.fill: parent
                radius: parent.radius
                color: "white"
                visible: false
                layer.enabled: true
            }
            Image {
                anchors.centerIn: parent
                width: 15; height: 15
                source: root.placeholderSource
                visible: cover.status !== Image.Ready
            }
            Image {
                id: cover
                anchors.fill: parent
                source: root.inViewport && root.row.cover ? "image://trackcovers/" + encodeURIComponent(root.row.cover) : ""
                sourceSize: Qt.size(Math.ceil(Math.max(76, width * Screen.devicePixelRatio)),
                                    Math.ceil(Math.max(76, height * Screen.devicePixelRatio)))
                asynchronous: true
                fillMode: Image.PreserveAspectCrop
            }
        }
        ColumnLayout {
            Layout.fillWidth: true
            spacing: 2
            Text {
                textFormat: Text.PlainText
                Layout.fillWidth: true
                text: root.row.title || ""
                color: root.ui.ink
                font.pixelSize: 13
                font.weight: Font.ExtraBold
                elide: Text.ElideRight
            }
            Text {
                textFormat: Text.PlainText
                Layout.fillWidth: true
                text: root.row.artist || ""
                color: root.ui.muted
                font.pixelSize: 12
                elide: Text.ElideRight
            }
        }
        Text {
            textFormat: Text.PlainText
            visible: text !== ""
            text: root.row.date || ""
            color: root.ui.muted
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
                textFormat: Text.PlainText
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

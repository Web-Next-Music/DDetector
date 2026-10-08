import QtQuick
import QtQuick.Controls
import QtQuick.Layouts
import QtQuick.Effects

Rectangle {
    id: card
    required property var ui
    required property var row
    property bool ignored: !!row.ignored
    property bool unsynced: !!row.allLines && row.allLines.length > 0
    property bool expanded: !!ui.expanded[row.id]
    property var displayLines: expanded && unsynced ? row.allLines : (row.lines || [])
    color: ui.surface
    border.color: ignored ? "#80c83737" : ui.borderColor
    radius: 10
    clip: true

    height: head.height + linesColumn.height + 2
    HoverHandler {
        cursorShape: parent.enabled ? Qt.PointingHandCursor : Qt.ArrowCursor
    }
    TapHandler {
        acceptedButtons: Qt.RightButton
        onTapped: point => card.ui.context(card.row, card, point.position.x, point.position.y, card.unsynced)
    }
    component CardButton: Button {
        HoverHandler {
            cursorShape: parent.enabled ? Qt.PointingHandCursor : Qt.ArrowCursor
        }
        property bool accented: false
        property bool expandAction: false
        implicitHeight: 22
        padding: 4
        leftPadding: 10
        rightPadding: 10
        font.pixelSize: 10
        font.weight: Font.Bold
        font.family: "DDetector Nunito"
        contentItem: Text {
            renderType: Text.NativeRendering
            text: parent.text
            font: parent.font
            color: parent.accented || (parent.expandAction && parent.hovered) ? (card.ignored ? card.ui.ignoredColor : card.ui.accent) : parent.hovered ? card.ui.ink : card.ui.muted
            horizontalAlignment: Text.AlignHCenter
            verticalAlignment: Text.AlignVCenter
        }
        background: Rectangle {
            radius: 6
            color: parent.accented ? (card.ignored ? (parent.hovered ? "#38a01e1e" : "#1fa01e1e") : card.ui.accentBg) : parent.hovered ? (parent.expandAction && card.ignored ? "#1aa01e1e" : card.ui.bg) : card.ui.surface
            border.color: parent.accented || (parent.expandAction && parent.hovered) ? (card.ignored ? "#4dc83c3c" : card.ui.accentBorder) : card.ui.borderColor
        }
    }
    Rectangle {
        id: head
        x: 1
        y: 1
        width: parent.width - 2
        height: 53
        radius: card.radius - 1
        color: card.ignored ? "#388c1919" : card.ui.surface2
        Rectangle {
            anchors.bottom: parent.bottom
            width: parent.width
            height: parent.radius
            color: parent.color
        }
        RowLayout {
            anchors.fill: parent
            anchors.leftMargin: 10
            anchors.rightMargin: 16
            spacing: 10
            Rectangle {
                Layout.preferredWidth: 32
                Layout.preferredHeight: 32
                radius: 4
                color: card.ui.bg
                clip: true
                layer.enabled: true
                layer.effect: MultiEffect {
                    maskEnabled: true
                    maskSource: coverMask
                }
                Rectangle {
                    id: coverMask
                    anchors.fill: parent
                    radius: 4
                    color: "white"
                    visible: false
                    layer.enabled: true
                }
                Image {
                    anchors.centerIn: parent
                    width: 15; height: 15
                    source: "qrc:/assets/track-placeholder.svg"
                    visible: cover.status !== Image.Ready
                }
                Image {
                    id: cover
                    anchors.fill: parent
                    source: card.row.cover ? "image://trackcovers/" + encodeURIComponent(card.row.cover) : ""
                                        sourceSize: Qt.size(64, 64)
                    fillMode: Image.PreserveAspectCrop
                    asynchronous: true
                }
            }
            ColumnLayout {
                Layout.fillWidth: true
                spacing: 1
                Text {
                    Layout.fillWidth: true
                    renderType: Text.NativeRendering
                    font.hintingPreference: Font.PreferFullHinting
                    text: card.row.title || ""
                    color: card.ui.ink
                    font.family: "DDetector Nunito"
                    font.pixelSize: 12
                    font.weight: Font.ExtraBold
                    elide: Text.ElideRight
                }
                Text {
                    Layout.fillWidth: true
                    renderType: Text.NativeRendering
                    font.hintingPreference: Font.PreferFullHinting
                    text: card.row.artist || ""
                    color: card.ui.muted
                    font.family: "DDetector Nunito"
                    font.pixelSize: 11
                    font.weight: Font.Bold
                    elide: Text.ElideRight
                }
            }
            Row {
                spacing: 6
                CardButton {
                    visible: card.unsynced
                    expandAction: true
                    text: card.expanded ? "Collapse" : "Full lyrics"
                    accented: card.expanded
                    onClicked: card.ui.toggleExpanded(card.row.id)
                }
                CardButton {
                    text: "Copy ID"
                    onClicked: backend.copyId(card.row.id)
                }
                CardButton {
                    text: "Yandex"
                    accented: true
                    onClicked: backend.searchYandex(card.row.title, card.row.artist)
                }
            }
        }
        Rectangle {
            anchors.bottom: parent.bottom
            width: parent.width
            height: 1
            color: card.ignored ? "#4dc83c3c" : card.ui.borderColor
        }
    }
    Rectangle {
        x: linesColumn.x
        y: linesColumn.y
        width: linesColumn.width
        height: linesColumn.height
        color: card.ignored ? "#33640b0b" : "transparent"
        radius: card.radius - 1
        Rectangle {
            width: parent.width
            height: parent.radius
            color: parent.color
        }
    }
    Column {
        id: linesColumn
        x: 1
        y: head.height + 1
        width: parent.width - 2
        Repeater {
            model: card.displayLines
            delegate: Item {
                id: line
                required property var modelData
                required property int index
                readonly property bool canPlay: !card.unsynced && musicPlayer.enabled
                    && typeof modelData.ts === "string"
                    && /^[0-9]+:[0-5][0-9](?::[0-5][0-9])?(?:\.[0-9]+)?$/.test(modelData.ts)
                    && /^[0-9]{1,20}$/.test(String(card.row.id))
                width: linesColumn.width
                height: Math.max(19, words.contentHeight) + 8
                opacity: modelData.isDrug === false ? (lineHover.hovered ? 0.7 : 0.45) : 1
                Rectangle {
                    anchors.fill: parent
                    color: card.ignored ? "#1aa01e1e" : card.ui.accentBg
                    opacity: lineHover.hovered && line.modelData.isDrug !== false ? 0.5 : 0
                }
                HoverHandler {
                    id: lineHover
                    cursorShape: line.canPlay ? Qt.PointingHandCursor : Qt.ArrowCursor
                }
                Text {
                    id: timestamp
                    renderType: Text.NativeRendering
                    visible: !card.unsynced
                    x: 12
                    anchors.verticalCenter: parent.verticalCenter
                    width: 46
                    text: (line.modelData.ts || "—:——").replace(/\.[0-9]+$/, "")
                    color: card.ignored ? "#c86464" : card.ui.accent
                    opacity: 0.8
                    font.family: "monospace"
                    font.weight: Font.Bold
                    font.pixelSize: 10
                }
                Item {
                    x: words.x
                    y: words.y
                    width: words.width
                    height: words.height
                    opacity: 0.85
                    Repeater {
                        model: words.markRects
                        delegate: Rectangle {
                            required property var modelData
                            x: modelData.x - 3
                            y: modelData.y
                            width: modelData.width + 6
                            height: modelData.height
                            radius: 2
                            color: card.ignored ? "#38a01e1e" : "#1f2bfef5"
                        }
                    }
                }
                TextEdit {
                    id: words
                    renderType: Text.NativeRendering
                    font.hintingPreference: Font.PreferFullHinting
                    readOnly: true
                    selectByMouse: false
                    activeFocusOnPress: false
                    padding: 0
                    height: contentHeight
                    property var markRects: []
                    FontMetrics {
                        id: markMetrics
                        font.family: words.font.family
                        font.pixelSize: words.font.pixelSize
                        font.weight: Font.DemiBold
                    }
                    function updateMarks() {
                        const rects = [];
                        let offset = 0;
                        for (const part of line.modelData.parts || []) {
                            const value = String(part.text);
                            let run = null;
                            if (part.marked) {
                                for (let i = 0; i < value.length; ++i) {
                                    const a = positionToRectangle(offset + i);
                                    const b = positionToRectangle(offset + i + 1);
                                    if (value[i] === "\n" || b.y !== a.y || b.x <= a.x) {
                                        if (run) rects.push(run);
                                        run = null;
                                        if (value[i] !== "\n") {
                                            rects.push({x: a.x, y: a.y, width: markMetrics.advanceWidth(value[i]), height: a.height});
                                        }
                                        continue;
                                    }
                                    if (run && run.y === a.y)
                                        run.width = b.x - run.x;
                                    else {
                                        if (run) rects.push(run);
                                        run = {x: a.x, y: a.y, width: b.x - a.x, height: a.height};
                                    }
                                }
                                if (run) rects.push(run);
                            }
                            offset += value.length;
                        }
                        markRects = rects;
                    }
                    onTextChanged: Qt.callLater(updateMarks)
                    onWidthChanged: Qt.callLater(updateMarks)
                    onContentHeightChanged: Qt.callLater(updateMarks)
                    Component.onCompleted: Qt.callLater(updateMarks)
                    x: card.unsynced ? 12 : 68
                    anchors.verticalCenter: parent.verticalCenter
                    width: parent.width - x - 12
                    font.family: "DDetector Nunito"
                    font.pixelSize: 12
                    font.weight: Font.Bold
                    color: card.ui.ink
                    opacity: 0.85
                    wrapMode: TextEdit.Wrap
                    textFormat: TextEdit.RichText
                    function escapeText(s) {
                        return String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/\n/g, "<br>");
                    }
                    text: "<p style='margin:0;line-height:130%'>" + (line.modelData.parts || []).map(p => p.marked ? "<span style='color:" + (card.ignored ? "#e65a5a" : card.ui.accent) + ";font-weight:600'>" + escapeText(p.text) + "</span>" : escapeText(p.text)).join("") + "</p>"
                }
                MouseArea {
                    anchors.fill: parent
                    enabled: line.canPlay
                    acceptedButtons: Qt.LeftButton
                    cursorShape: Qt.PointingHandCursor
                    onClicked: musicPlayer.playAt(String(card.row.id), line.modelData.ts)
                }
                Rectangle {
                    visible: line.index < card.displayLines.length - 1
                    anchors.bottom: parent.bottom
                    width: parent.width
                    height: 1
                    color: card.ignored ? "#4dc83c3c" : card.ui.borderColor
                }
            }
        }
    }
}

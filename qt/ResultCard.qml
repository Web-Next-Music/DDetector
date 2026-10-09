pragma ComponentBehavior: Bound
import QtQuick
import QtQuick.Controls
import QtQuick.Layouts
import QtQuick.Effects
import DDetector.Lyrics 1.0

Rectangle {
    id: card
    required property var ui
    required property var row
    property var playbackBackend: typeof musicPlayer !== "undefined" ? musicPlayer : null
    property var actionBackend: typeof backend !== "undefined" ? backend : null
    property url placeholderSource: "qrc:/assets/track-placeholder.svg"
    property bool ignored: !!row.ignored
    property bool unsynced: !!row.allLines && row.allLines.length > 0
    property bool expanded: !!ui.expanded[row.id]
    property var displayLines: expanded && unsynced ? row.allLines : (row.lines || [])
    property var lyricsViewport: ListView.view
        readonly property bool inViewport: visible && (!lyricsViewport ||
            (lyricsViewport.visible && y + height > lyricsViewport.contentY
                && y < lyricsViewport.contentY + lyricsViewport.height))
    property var lineOffsets: [0]
    readonly property int instantiatedEditors: lyricRows.count
    property int measurementPasses: 0
    property bool layoutReady: false
    readonly property LyricsLayout layoutBackend: LyricsLayout {}
    property var measuredLines: null
    property real measuredWidth: -1
    property bool measuredIgnored: false
    function escapeText(value) {
        return String(value).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/\n/g, "<br>");
    }
    function lineHtml(value) {
        return "<p style='margin:0;line-height:130%'>" + (value.parts || []).map(p => p.marked
            ? "<span style='color:" + (ignored ? "#e65a5a" : ui.accent)
                + ";font-weight:600'>" + escapeText(p.text) + "</span>"
            : escapeText(p.text)).join("") + "</p>";
    }
    function measureLines() {
        if (!layoutReady || (measuredLines === displayLines
                && measuredWidth === measurement.width && measuredIgnored === ignored)) return;
        // One reusable native QTextDocument layout, never one editor per row.
        // Retain only numeric offsets; rich text/documents for hidden rows are released.
        const offsets = layoutBackend.measure(measurement.textDocument, displayLines,
            measurement.width, ignored ? "#e65a5a" : ui.accent);
        measuredLines = displayLines;
        measuredWidth = measurement.width;
        measuredIgnored = ignored;
        lineOffsets = offsets;
        measurementPasses++;
        if (activeFocus) Qt.callLater(revealKeyboardLine);
    }
    function lineAtOffset(offset) {
        let low = 0;
        let high = lineOffsets.length - 1;
        while (low < high) {
            const mid = Math.ceil((low + high) / 2);
            if (lineOffsets[mid] <= offset) low = mid;
            else high = mid - 1;
        }
        return low;
    }
    Component.onCompleted: {

        layoutReady = true;
        measureLines();
    }

    onUnsyncedChanged: Qt.callLater(measureLines)
    onIgnoredChanged: Qt.callLater(measureLines)
    color: ui.surface
    border.color: ignored ? "#80c83737" : ui.borderColor
    radius: 10
    clip: true
    activeFocusOnTab: true
    Accessible.role: Accessible.Pane
    Accessible.name: "Lyrics for " + (row.title || "") + ", " + (row.artist || "")
    Accessible.description: "Use Up and Down to choose a line, Enter to play, and Menu for track actions."
    property int keyboardLine: 0
    onDisplayLinesChanged: {
        keyboardLine = Math.max(0, Math.min(keyboardLine, displayLines.length - 1));
        Qt.callLater(measureLines);
    }
    onKeyboardLineChanged: Qt.callLater(revealKeyboardLine)
    onActiveFocusChanged: if (activeFocus) Qt.callLater(revealKeyboardLine)
    function revealKeyboardLine() {
        const view = lyricsViewport;
        if (!view || !activeFocus || keyboardLine + 1 >= lineOffsets.length) return;
        const y = card.mapToItem(view.contentItem, 0, linesColumn.y).y + lineOffsets[keyboardLine];
        const bottom = y + lineOffsets[keyboardLine + 1] - lineOffsets[keyboardLine];
        if (y < view.contentY)
            view.contentY = Math.max(view.originY, y);
        else if (bottom > view.contentY + view.height)
            view.contentY = Math.min(view.originY + Math.max(0, view.contentHeight - view.height), bottom - view.height);
    }
    function canPlayLine(value) {
        return !unsynced && playbackBackend && playbackBackend.enabled && value
            && typeof value.ts === "string"
            && /^[0-9]+:[0-5][0-9](?::[0-5][0-9])?(?:\.[0-9]+)?$/.test(value.ts)
            && /^[0-9]{1,20}$/.test(String(row.id));
    }
    function playKeyboardLine() {
        const value = displayLines[keyboardLine];
        if (canPlayLine(value)) playbackBackend.playAt(String(row.id), value.ts);
    }
    Keys.onReturnPressed: playKeyboardLine()
    Keys.onEnterPressed: playKeyboardLine()
    Keys.onSpacePressed: playKeyboardLine()
    Keys.onUpPressed: keyboardLine = Math.max(0, keyboardLine - 1)
    Keys.onDownPressed: keyboardLine = Math.max(0, Math.min(displayLines.length - 1, keyboardLine + 1))
    Keys.onMenuPressed: ui.context(row, card, width / 2, 24, unsynced)
    Keys.onPressed: function(event) {
        if (event.key === Qt.Key_F10 && (event.modifiers & Qt.ShiftModifier)) {
            ui.context(row, card, width / 2, 24, unsynced);
            event.accepted = true;
        }
    }


    height: head.height + linesColumn.height + 2
    HoverHandler {
        cursorShape: parent.enabled ? Qt.PointingHandCursor : Qt.ArrowCursor
    }
    TapHandler {
        acceptedButtons: Qt.RightButton
        onTapped: point => card.ui.context(card.row, card, point.position.x, point.position.y, card.unsynced)
    }
    component CardButton: SiteButton {
        property bool expandAction: false
        implicitHeight: 22
        padding: 4
        leftPadding: 10
        rightPadding: 10
        cornerRadius: 6
        font.pixelSize: 10
        textRenderType: Text.NativeRendering
        textColor: card.ui.muted
        hoverTextColor: expandAction ? accentColor : card.ui.ink
        accentColor: card.ignored ? card.ui.ignoredColor : card.ui.accent
        surfaceColor: card.ui.surface
        hoverColor: expandAction && card.ignored ? "#1aa01e1e" : card.ui.bg
        accentedColor: card.ignored ? (hovered ? "#38a01e1e" : "#1fa01e1e") : card.ui.accentBg
        borderColor: card.ui.borderColor
        hoverBorderColor: expandAction ? accentBorder : borderColor
        accentBorder: card.ignored ? "#4dc83c3c" : card.ui.accentBorder
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
                layer.enabled: GraphicsInfo.api !== GraphicsInfo.Software
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
                    source: card.placeholderSource
                    visible: cover.status !== Image.Ready
                }
                Image {
                    id: cover
                    anchors.fill: parent
                    source: card.inViewport && card.row.cover ? "image://trackcovers/" + encodeURIComponent(card.row.cover) : ""
                                        sourceSize: Qt.size(64, 64)
                    fillMode: Image.PreserveAspectCrop
                    asynchronous: true
                }
            }
            ColumnLayout {
                Layout.fillWidth: true
                spacing: 1
                Text {
                    textFormat: Text.PlainText
                    Layout.fillWidth: true
                    renderType: Text.NativeRendering
                    font.hintingPreference: Font.PreferFullHinting
                    objectName: "resultTitle"
                                        text: card.row.title || ""
                    color: card.ui.ink
                    font.family: "DDetector Nunito"
                    font.pixelSize: 12
                    font.weight: Font.ExtraBold
                    elide: Text.ElideRight
                }
                Text {
                    textFormat: Text.PlainText
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
                    enabled: !!card.actionBackend
                    onClicked: card.actionBackend.copyId(card.row.id)
                }
                CardButton {
                    text: "Yandex"
                    accented: true
                    enabled: !!card.actionBackend
                    onClicked: card.actionBackend.searchYandex(card.row.title, card.row.artist)
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
    // Settings-only document template for LyricsLayout; never populated with lyrics.
    TextEdit {
        id: measurement
        objectName: "lyricsMeasurement"
        visible: false
        Accessible.ignored: true
        readOnly: true
        activeFocusOnPress: false
        padding: 0
        width: Math.max(0, linesColumn.width - (card.unsynced ? 12 : 68) - 12)
        renderType: Text.NativeRendering
        font.family: "DDetector Nunito"
        font.pixelSize: 12
        font.weight: Font.Bold
        font.hintingPreference: Font.PreferFullHinting
        wrapMode: TextEdit.Wrap
        textFormat: TextEdit.RichText
        onWidthChanged: Qt.callLater(card.measureLines)
        onFontChanged: {
            card.measuredLines = null;
            Qt.callLater(card.measureLines);
        }
    }
    Item {
        id: linesColumn
        x: 1
        y: head.height + 1
        width: parent.width - 2
        height: card.lineOffsets[card.lineOffsets.length - 1]
        // Mapping explicitly depends on card.y, since mapToItem is not reactive.
        readonly property real viewportTop: {
            const view = card.lyricsViewport;
            if (view) {
                const layoutY = card.y;
                return view.contentY - card.mapToItem(view.contentItem, 0, y).y;
            }
            const layoutY = card.y;
            return -card.mapToItem(null, 0, y).y;
        }
        readonly property real viewportHeight: card.lyricsViewport ? card.lyricsViewport.height
            : (card.Window.window ? card.Window.window.height : 0)
        // Publish both bounds atomically. Separate first/end bindings can briefly
        // expose a 0..N range when jumping back from the bottom of a long card.
        readonly property var visibleRange: {
            if (!card.inViewport) return {first: 0, count: 0};
            const first = card.lineAtOffset(Math.max(0, viewportTop - 64));
            const end = Math.min(card.displayLines.length,
                card.lineAtOffset(viewportTop + viewportHeight + 64) + 1);
            return {first: first, count: Math.max(0, end - first)};
        }
        Repeater {
            id: lyricRows
            model: linesColumn.visibleRange.count
            delegate: Item {
                id: line
                objectName: "lyricLine" + lineIndex
                required property int index
                readonly property int lineIndex: linesColumn.visibleRange.first + index
                readonly property var modelData: card.displayLines[lineIndex]
                readonly property bool canPlay: card.canPlayLine(modelData)
                y: card.lineOffsets[lineIndex] || 0
                width: linesColumn.width
                height: (card.lineOffsets[lineIndex + 1] || 0) - y
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
                    textFormat: Text.PlainText
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
                    objectName: "lyricWords" + line.lineIndex
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
                    text: card.lineHtml(line.modelData)
                }
                Button {
                    anchors.fill: parent
                    enabled: line.canPlay
                    focusPolicy: Qt.NoFocus
                    Accessible.name: "Play " + (card.row.title || "") + " at " + (line.modelData.ts || "")
                    contentItem: Item {}
                    background: Rectangle {
                        color: "transparent"
                        border.color: "transparent"
                    }
                    onClicked: {
                        card.keyboardLine = line.lineIndex;
                        card.forceActiveFocus(Qt.MouseFocusReason);
                        card.playbackBackend.playAt(String(card.row.id), line.modelData.ts);
                    }
                }
                Rectangle {
                    visible: line.lineIndex < card.displayLines.length - 1
                    anchors.bottom: parent.bottom
                    width: parent.width
                    height: 1
                    color: card.ignored ? "#4dc83c3c" : card.ui.borderColor
                }
            }
        }
    }
}

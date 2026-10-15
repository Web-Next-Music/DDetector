pragma ComponentBehavior: Bound
import QtQuick
import QtQuick.Controls

Item {
    id: root

    property string diffText: ""
    property color bg: "#000000"
    property real headerHeight: 30
    readonly property color removedColor: "#cf4f2a"
    readonly property color addedColor: "#00a682"
    readonly property int rowHeight: Math.ceil(metrics.height) + 8
    readonly property var parsed: parseDiff(diffText)
    readonly property real gutterWidth: Math.max(44, metrics.advanceWidth(String(parsed.maxLine)) + 20)
    readonly property real sideWidth: Math.max(view.width / 2, gutterWidth + parsed.textWidth + 24)

    FontMetrics {
        id: metrics
        font.family: "monospace"
        font.pixelSize: 13
    }

    // Only consume line prefixes inside a hunk. File metadata (including binary
    // diffs and quoted paths) stays literal, never interpreted as markup.
    function parseDiff(input) {
        const rows = [];
        const lines = input.split("\n");
        if (lines.length && lines[lines.length - 1] === "")
            lines.pop();
        let oldLine = 0;
        let newLine = 0;
        let oldRemaining = 0;
        let newRemaining = 0;
        let maxLine = 1;
        let textWidth = 0;
        let removed = [];
        let added = [];

        function measure(text) {
            textWidth = Math.max(textWidth, metrics.advanceWidth(text));
            return text;
        }
        function flush() {
            for (let j = 0; j < Math.max(removed.length, added.length); ++j) {
                const left = removed[j];
                const right = added[j];
                rows.push({kind: "line", oldNumber: left ? left.number : "",
                    newNumber: right ? right.number : "",
                    oldText: left ? left.text : "", newText: right ? right.text : "",
                    removed: !!left, added: !!right, label: ""});
            }
            removed = [];
            added = [];
        }
        function heading(kind, text) {
            rows.push({kind: kind, label: text, oldNumber: "", newNumber: "",
                oldText: "", newText: "", removed: false, added: false});
        }

        for (let i = 0; i < lines.length; ++i) {
            // Strip transport CR only; retain all other source whitespace.
            const line = lines[i].endsWith("\r") ? lines[i].slice(0, -1) : lines[i];
            const hunk = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(?:.*)$/.exec(line);
            if (hunk) {
                flush();
                oldLine = Number(hunk[1]);
                newLine = Number(hunk[3]);
                oldRemaining = hunk[2] === undefined ? 1 : Number(hunk[2]);
                newRemaining = hunk[4] === undefined ? 1 : Number(hunk[4]);
                heading("hunk", line);
                continue;
            }
            if (line.startsWith("diff --git ")) {
                flush();
                oldRemaining = 0;
                newRemaining = 0;
                continue;
            }
            // Metadata is not source content; never filter prefixes within a hunk.
            if (oldRemaining === 0 && newRemaining === 0) {
                if (line.startsWith("+++ ")) {
                    const path = line.slice(4);
                    if (path !== "/dev/null")
                        heading("file", path.startsWith("b/") ? path.slice(2) : path);
                    continue;
                }
                if (line.startsWith("index ") || line.startsWith("--- ")
                        || line.startsWith("new file mode ") || line.startsWith("deleted file mode "))
                    continue;
            }
            if (line.startsWith("\\ No newline at end of file")) {
                // Annotate the affected side without splitting a replacement block.
                if (added.length)
                    added[added.length - 1].text += "  [No newline at end of file]";
                else if (removed.length)
                    removed[removed.length - 1].text += "  [No newline at end of file]";
                else
                    heading("note", line);
                measure((added.length ? added[added.length - 1] : removed.length ? removed[removed.length - 1] : {text: ""}).text);
                continue;
            }
            if (line.startsWith("-") && oldRemaining > 0) {
                // A removal after additions begins a separate change block.
                if (added.length)
                    flush();
                removed.push({number: oldLine++, text: measure(line.slice(1))});
                --oldRemaining;
            } else if (line.startsWith("+") && newRemaining > 0) {
                added.push({number: newLine++, text: measure(line.slice(1))});
                --newRemaining;
            } else if (line.startsWith(" ") && oldRemaining > 0 && newRemaining > 0) {
                flush();
                const text = measure(line.slice(1));
                rows.push({kind: "line", oldNumber: oldLine++, newNumber: newLine++,
                    oldText: text, newText: text, removed: false, added: false, label: ""});
                --oldRemaining;
                --newRemaining;
            } else {
                flush();
                oldRemaining = 0;
                newRemaining = 0;
                heading(line.startsWith("--- ") || line.startsWith("+++ ") ? "file" : "note", line);
            }
            maxLine = Math.max(maxLine, oldLine, newLine);
        }
        flush();
        return {rows: rows, maxLine: maxLine, textWidth: textWidth};
    }

    Rectangle {
        anchors.fill: parent
        color: root.bg
    }

    ListView {
        id: view
        anchors.fill: parent
        clip: true
        model: root.parsed.rows
        contentWidth: root.sideWidth * 2
        flickableDirection: Flickable.AutoFlickDirection
        boundsBehavior: Flickable.StopAtBounds
        reuseItems: true
        activeFocusOnTab: true
        Accessible.name: "Side-by-side Git diff"
        ScrollBar.vertical: ScrollBar {}
        ScrollBar.horizontal: ScrollBar {
                    policy: view.contentWidth > view.width ? ScrollBar.AlwaysOn : ScrollBar.AsNeeded
                }
                WheelHandler {
                    acceptedModifiers: Qt.ShiftModifier
                    onWheel: event => {
                        const delta = event.pixelDelta.x || event.pixelDelta.y
                            || (event.angleDelta.x || event.angleDelta.y) / 3;
                        view.contentX = Math.max(0, Math.min(view.contentWidth - view.width, view.contentX - delta));
                        event.accepted = true;
                    }
                }

        onModelChanged: {
            contentX = 0;
            contentY = 0;
        }
        Keys.onLeftPressed: contentX = Math.max(0, contentX - 40)
        Keys.onRightPressed: contentX = Math.max(0, Math.min(contentWidth - width, contentX + 40))
        Keys.onUpPressed: contentY = Math.max(0, contentY - root.rowHeight)
        Keys.onDownPressed: contentY = Math.max(0, Math.min(contentHeight - height, contentY + root.rowHeight))

        delegate: Item {
            id: diffRow
            required property var modelData
            width: view.contentWidth
            height: modelData.kind === "file" ? root.headerHeight : root.rowHeight

            Rectangle {
                anchors.fill: parent
                visible: diffRow.modelData.kind !== "line"
                color: diffRow.modelData.kind === "hunk" ? "#191e25" : "#151518"
            }
            Text {
                visible: diffRow.modelData.kind !== "line"
                // Keep metadata visible when the source is horizontally scrolled.
                x: view.contentX + 12
                width: Math.max(0, view.width - 24)
                height: parent.height
                text: diffRow.modelData.label
                textFormat: Text.PlainText
                elide: Text.ElideRight
                font.family: metrics.font.family
                font.pixelSize: metrics.font.pixelSize
                font.bold: diffRow.modelData.kind === "file"
                color: diffRow.modelData.kind === "hunk" ? "#a9b9cd" : "#b0b0b8"
                verticalAlignment: Text.AlignVCenter
            }
            Repeater {
                model: diffRow.modelData.kind === "line" ? 2 : 0
                delegate: Item {
                    id: cell
                    required property int index
                    readonly property bool oldSide: index === 0
                    readonly property bool changed: oldSide ? diffRow.modelData.removed : diffRow.modelData.added
                    x: index * root.sideWidth
                    width: root.sideWidth
                    height: diffRow.height

                    Rectangle {
                        anchors.fill: parent
                        color: cell.oldSide ? root.removedColor : root.addedColor
                        opacity: cell.changed ? 0.18 : 0
                    }
                    Text {
                        width: root.gutterWidth - 10
                        height: parent.height
                        text: cell.oldSide ? diffRow.modelData.oldNumber : diffRow.modelData.newNumber
                        textFormat: Text.PlainText
                        font: metrics.font
                        color: "#797982"
                        horizontalAlignment: Text.AlignRight
                        verticalAlignment: Text.AlignVCenter
                        Accessible.ignored: true
                    }
                    Text {
                        x: root.gutterWidth + 8
                        width: parent.width - x - 12
                        height: parent.height
                        text: cell.oldSide ? diffRow.modelData.oldText : diffRow.modelData.newText
                        textFormat: Text.PlainText
                        wrapMode: Text.NoWrap
                        font: metrics.font
                        color: "#f0f0f4"
                        verticalAlignment: Text.AlignVCenter
                    }
                }
            }
            Rectangle {
                x: root.sideWidth
                width: 1
                height: parent.height
                color: "#3397979f"
                visible: diffRow.modelData.kind === "line"
            }
        }
    }
}

pragma ComponentBehavior: Bound
import QtQuick
import QtQuick.Controls

Control {
    id: block

    property alias text: code.text
    property color surface: "#0c0c0e"
    property color borderColor: "#3397979f"
    property color textColor: "#f0f0f4"
    readonly property var lineStarts: {
        const starts = [0];
        for (let i = 0; i < text.length; ++i) {
            if (text[i] === "\n") starts.push(i + 1);
        }
        return starts;
    }

    function copyAll() {
        const start = code.selectionStart;
        const end = code.selectionEnd;
        const cursor = code.cursorPosition;
        code.selectAll();
        code.copy();
        copyFeedback.restart();
        code.cursorPosition = cursor;
        if (start !== end) code.select(cursor === start ? end : start, cursor === start ? start : end);
    }

    Timer {
        id: copyFeedback
        interval: 2000
    }

    padding: 12
    leftPadding: 20
    rightPadding: 12
    implicitHeight: contentItem.implicitHeight + topPadding + bottomPadding

    background: Rectangle {
        color: block.surface
        border.color: block.borderColor
        radius: 10
    }
    contentItem: Item {
        implicitHeight: Math.max(code.implicitHeight, copyButton.height)

        FontMetrics { id: codeMetrics; font: code.font }
        Item {
            id: lineNumbers
            objectName: "codeBlockLineNumbers"
            width: Math.max(24, codeMetrics.averageCharacterWidth * String(block.lineStarts.length).length)
            height: code.height
            Repeater {
                model: block.lineStarts
                delegate: Text {
                    required property int index
                    required property int modelData
                    readonly property rect lineRect: {
                        // Wrapped lines follow the text layout rather than a fixed row height.
                        const layoutWidth = code.width;
                        const layoutHeight = code.contentHeight;
                        const document = code.text;
                        return code.positionToRectangle(modelData);
                    }
                    y: lineRect.y
                    width: lineNumbers.width
                    height: lineRect.height
                    text: index + 1
                    textFormat: Text.PlainText
                    font: code.font
                    color: "#999999"
                    horizontalAlignment: Text.AlignRight
                    verticalAlignment: Text.AlignVCenter
                    Accessible.ignored: true
                }
            }
        }
        TextArea {
            id: code
            objectName: "codeBlockText"
            anchors.left: lineNumbers.right
            anchors.leftMargin: 16
            anchors.right: copyButton.left
            anchors.rightMargin: 12
            padding: 0
            textFormat: TextEdit.PlainText
            readOnly: true
            selectByMouse: true
            persistentSelection: true
            wrapMode: TextEdit.Wrap
            color: block.textColor
            selectionColor: "#2bfef5"
            selectedTextColor: block.surface
            font.family: "monospace"
            font.pixelSize: 13
            Accessible.name: "Code block"
            background: null
            ContextMenu.menu: SiteEditMenu { textControl: code }
        }
        SiteButton {
            id: copyButton
            objectName: "codeBlockCopyButton"
            anchors.right: parent.right
            anchors.top: parent.top
            iconOnly: true
            glyph: copyFeedback.running ? "check" : "copy"
            text: copyFeedback.running ? "Copied!" : "Copy code"
            enabled: code.length > 0
            onClicked: block.copyAll()
            SiteToolTip { text: copyFeedback.running ? "Copied!" : "Copy all text" }
        }
    }
}

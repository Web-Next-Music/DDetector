pragma ComponentBehavior: Bound
import QtQuick
import QtQuick.Controls

Control {
    id: quote
    property alias text: label.text
    property color textColor: "#999999"
    property color borderColor: "#3397979f"
    padding: 8
    leftPadding: 16
    rightPadding: 16
    implicitHeight: contentItem.implicitHeight + topPadding + bottomPadding
    background: Rectangle {
        color: "#0af0f0f4"
        radius: 6
        Rectangle {
            x: 0
            y: 8
            width: 3
            height: Math.max(0, parent.height - 16)
            radius: 1.5
            color: quote.borderColor
        }
    }
    contentItem: Label {
        id: label
        textFormat: Text.PlainText
        wrapMode: Text.Wrap
        color: quote.textColor
        font.family: "DDetector Nunito"
        font.pixelSize: 14
        font.weight: Font.Medium
        lineHeight: 1.8
    }
}

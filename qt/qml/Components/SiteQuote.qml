pragma ComponentBehavior: Bound
import QtQuick
import QtQuick.Controls

Control {
    id: quote
    property alias text: label.text
    property alias textFormat: label.textFormat
    property alias linkColor: label.linkColor
    property string keyboardLink: ""
    signal linkActivated(string link)
    activeFocusOnTab: textFormat === Text.RichText
    Accessible.role: textFormat === Text.RichText ? Accessible.Link : Accessible.StaticText
    Accessible.name: label.text.replace(/<[^>]*>/g, "")
    Accessible.onPressAction: if (keyboardLink) linkActivated(keyboardLink)
    Keys.onReturnPressed: if (keyboardLink) linkActivated(keyboardLink)
    Keys.onSpacePressed: if (keyboardLink) linkActivated(keyboardLink)
    property color textColor: "#999999"
    property color borderColor: "#3397979f"
    property real backgroundRadius: 6
    padding: 8
    leftPadding: 16
    rightPadding: 16
    implicitHeight: contentItem.implicitHeight + topPadding + bottomPadding
    background: Rectangle {
        color: "#0af0f0f4"
        radius: quote.backgroundRadius
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
        onLinkActivated: function(link) { quote.linkActivated(link); }
        HoverHandler {
            cursorShape: label.hoveredLink.length > 0 ? Qt.PointingHandCursor : Qt.ArrowCursor
        }
        wrapMode: Text.Wrap
        color: quote.textColor
        font.family: "DDetector Nunito"
        font.pixelSize: 14
        font.weight: Font.Medium
        lineHeight: 1.0
    }
}

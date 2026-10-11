pragma ComponentBehavior: Bound
import QtQuick
import QtQuick.Controls

ToolTip {
    id: control

    property color surfaceColor: "#171719"
    property color borderColor: "#3397979f"
    property color textColor: "#f0f0f4"
    property real maximumWidth: 320
    property Control owner: parent as Control

    visible: owner && owner.enabled && (owner.hovered || owner.activeFocus) && text.length > 0
    delay: 500
    timeout: -1
    margins: 8
    padding: 8
    horizontalPadding: 10
    implicitWidth: Math.min(maximumWidth, implicitContentWidth + leftPadding + rightPadding)
    font.family: "DDetector Nunito"
    font.pixelSize: 12

    contentItem: Text {
        text: control.text
        font: control.font
        color: control.textColor
        textFormat: Text.PlainText
        wrapMode: Text.Wrap
    }

    background: Rectangle {
        color: control.surfaceColor
        border.color: control.borderColor
        radius: 8
    }

    enter: Transition {
        NumberAnimation { property: "opacity"; from: 0; to: 1; duration: 120 }
    }
    exit: Transition {
        NumberAnimation { property: "opacity"; from: 1; to: 0; duration: 80 }
    }
}

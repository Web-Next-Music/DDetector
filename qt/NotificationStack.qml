pragma ComponentBehavior: Bound
import QtQuick

Column {
    id: toastStack
    required property var ui
    property var notificationModel
    signal expired(int toastId)
    objectName: "notificationStack"
    spacing: 8
    width: Math.min(toastStack.ui.width - 32, 380)
    // Update height and row positions together so inserting at the top
    // keeps existing cards stationary relative to the player.
    Repeater {
        model: toastStack.notificationModel
        delegate: Rectangle {
            id: toastCard
            required property int toastId
            required property string message
            property bool open: false
            objectName: "notificationToast"
            // Column owns item positions; animate only the visual offset.
            transform: Translate {
                x: toastCard.open ? 0 : -toastCard.width - 16
                Behavior on x {
                    NumberAnimation {
                        duration: 220
                        easing.type: Easing.OutCubic
                    }
                }
            }
            width: Math.min(toastStack.width, toastLabel.implicitWidth + 32)
            height: toastLabel.implicitHeight + 24
            radius: 8
            opacity: open ? 1 : 0
            color: toastStack.ui.surface2
            border.color: toastStack.ui.borderColor
            Accessible.role: Accessible.AlertMessage
            Accessible.name: message
            Behavior on opacity {
                NumberAnimation {
                    duration: 180
                }
            }
            Component.onCompleted: open = true
            Timer {
                interval: 3500
                running: true
                onTriggered: {
                    toastCard.open = false;
                    exitTimer.start();
                }
            }
            Timer {
                id: exitTimer
                interval: 220
                onTriggered: toastStack.expired(toastCard.toastId)
            }
            Text {
                id: toastLabel
                textFormat: Text.PlainText
                anchors.centerIn: parent
                width: Math.min(toastStack.width - 32, implicitWidth)
                wrapMode: Text.Wrap
                text: toastCard.message
                color: toastStack.ui.ink
                font.pixelSize: 12
            }
        }
    }
}

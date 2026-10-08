import QtQuick
import QtQuick.Controls
import QtQuick.Effects

Rectangle {
    id: header

    property url icon: ""
    property string title: ""
    property color surface: "#0c0c0e"
    property color surface2: "#171719"
    property color borderColor: "#3397979f"
    property color ink: "#f0f0f4"
    property color muted: "#666666"
    property color accentBg: "#142bfef5"
    property color accentBorder: "#332bfef5"
    property alias closeButtonObjectName: closeButton.objectName
    property string closeButtonAccessibleName: "Close"
    property alias closeEnabled: closeButton.enabled
    signal closeRequested()

    implicitHeight: 64
    color: "transparent"

    Rectangle {
        anchors.fill: parent
        anchors.margins: 8
        color: header.surface2
        radius: 12
        border.color: header.borderColor
    }
    Image {
        id: headerIcon
        anchors.left: parent.left
        anchors.leftMargin: 24
        anchors.verticalCenter: parent.verticalCenter
        width: 18
        height: 18
        source: header.icon
                visible: header.icon.toString().length > 0
        layer.enabled: true
        layer.effect: MultiEffect { brightness: 1; colorization: 1; colorizationColor: header.muted }
    }
    Text {
        anchors.left: headerIcon.visible ? headerIcon.right : parent.left
        anchors.leftMargin: headerIcon.visible ? 12 : 24
        anchors.right: closeButton.left
        anchors.rightMargin: 12
        anchors.verticalCenter: parent.verticalCenter
        text: header.title
        color: header.ink
        font.family: "DDetector Nunito"
        font.pixelSize: 16
        font.weight: Font.Bold
        elide: Text.ElideRight
    }
    Button {
        id: closeButton
        anchors.right: parent.right
        anchors.rightMargin: 24
        anchors.verticalCenter: parent.verticalCenter
        width: 30
        height: 30
        padding: 0
        opacity: enabled ? 1 : 0.5
        Accessible.name: header.closeButtonAccessibleName
        HoverHandler { cursorShape: parent.enabled ? Qt.PointingHandCursor : Qt.ArrowCursor }
        contentItem: Item {
            Image {
                anchors.centerIn: parent
                width: 16
                height: 16
                source: "qrc:/assets/lucide-x.svg"
                layer.enabled: true
                layer.effect: MultiEffect { brightness: 1; colorization: 1; colorizationColor: closeButton.hovered ? header.ink : header.muted }
            }
        }
        background: Rectangle {
            radius: 8
            color: closeButton.hovered ? header.surface : "transparent"
            border.color: closeButton.visualFocus ? header.accentBorder : "transparent"
            Behavior on color { ColorAnimation { duration: 150 } }
            Rectangle {
                anchors.fill: parent
                anchors.margins: -3
                radius: 11
                color: "transparent"
                border.width: 2
                border.color: "#2bfef5"
                visible: closeButton.visualFocus
            }
        }
        onClicked: header.closeRequested()
    }
}

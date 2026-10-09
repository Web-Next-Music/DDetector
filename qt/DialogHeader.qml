pragma ComponentBehavior: Bound
import QtQuick
import QtQuick.Controls
import QtQuick.Effects

Rectangle {
    id: header

    property url icon: ""
    property url assetRoot: "qrc:/assets/"
    property string title: ""
    property color surface: "#0c0c0e"
    property color surface2: "#171719"
    property color borderColor: "#3397979f"
    property color ink: "#f0f0f4"
    property color muted: "#999999"
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
        textFormat: Text.PlainText
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
    SiteButton {
        id: closeButton
        anchors.right: parent.right
        anchors.rightMargin: 24
        anchors.verticalCenter: parent.verticalCenter
        width: 30
        height: 30
        padding: 0
        text: header.closeButtonAccessibleName
        assetRoot: header.assetRoot
        iconOnly: true
        glyph: "x"
        glyphSize: 16
        textColor: header.muted
        hoverTextColor: header.ink
        surfaceColor: "transparent"
        hoverColor: header.surface
        borderColor: "transparent"
        accentBorder: "transparent"
        onClicked: header.closeRequested()
    }
}

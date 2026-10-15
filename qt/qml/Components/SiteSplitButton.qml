pragma ComponentBehavior: Bound
import QtQuick

Item {
    id: control

    property alias text: mainButton.text
    property alias glyph: mainButton.glyph
    property bool accented: false
    property bool menuEnabled: true
    property string toolTip: ""
    property string menuToolTip: ""
    property color borderColor: "#3397979f"
    property color accentBorder: "#332bfef5"

    signal clicked()
    signal menuRequested()

    implicitWidth: mainButton.implicitWidth + menuButton.implicitWidth + 2
    implicitHeight: 26

    Rectangle {
        anchors.fill: parent
        radius: 4
        color: mainButton.surfaceColor
        border.color: control.accented || mainButton.visualFocus || menuButton.visualFocus
            ? control.accentBorder
            : mainButton.hovered || menuButton.hovered ? control.accentBorder : control.borderColor
        opacity: control.enabled ? 1 : 0.45
        Accessible.ignored: true
    }

    SiteButton {
        id: mainButton
        anchors.left: parent.left
        anchors.top: parent.top
        anchors.bottom: parent.bottom
        anchors.right: menuButton.left
        anchors.leftMargin: 1
        anchors.topMargin: 1
        anchors.bottomMargin: 1
        implicitHeight: control.implicitHeight - 2
        cornerRadius: 3
        accented: control.accented
        borderColor: "transparent"
        accentBorder: "transparent"
        hoverBorderColor: "transparent"
        onClicked: control.clicked()

        SiteToolTip {
            text: control.toolTip
        }
    }

    SiteButton {
        id: menuButton
        anchors.right: parent.right
        anchors.top: parent.top
        anchors.bottom: parent.bottom
        anchors.margins: 1
        width: implicitWidth
        implicitHeight: control.implicitHeight - 2
        iconOnly: true
        glyph: "chevron-down"
        cornerRadius: 3
        accented: control.accented
        enabled: control.menuEnabled
        borderColor: "transparent"
        accentBorder: "transparent"
        hoverBorderColor: "transparent"
        Accessible.name: control.text ? control.text + " menu" : "Open menu"
        onClicked: control.menuRequested()

        SiteToolTip {
            text: control.menuToolTip
        }
    }

    Rectangle {
        x: menuButton.x
        y: 1
        width: 1
        height: Math.max(0, control.height - 2)
        color: control.accented ? control.accentBorder : control.borderColor
        opacity: control.enabled ? 1 : 0.45
        Accessible.ignored: true
    }
}

pragma ComponentBehavior: Bound
import QtQuick
import QtQuick.Controls

CheckBox {
    id: control

    property color accentColor: "#2bfef5"
    property color surfaceColor: "#0c0c0e"
    property color borderColor: "#242426"
    property color hoverBorderColor: "#332bfef5"
    property color hoverColor: "#142bfef5"

    implicitWidth: 28
    implicitHeight: 28
    padding: 6
    opacity: enabled ? 1 : 0.45
    Accessible.name: text

    HoverHandler {
        cursorShape: control.enabled ? Qt.PointingHandCursor : Qt.ArrowCursor
    }

    indicator: Rectangle {
        implicitWidth: 16
        implicitHeight: 16
        x: (control.width - width) / 2
        y: (control.height - height) / 2
        radius: 4
        color: control.checked ? control.accentColor : control.hovered || control.down ? control.hoverColor : control.surfaceColor
        border.color: control.checked ? control.accentColor : control.hovered || control.visualFocus ? control.hoverBorderColor : control.borderColor

        SiteIcon {
            anchors.centerIn: parent
            width: 12
            height: 12
            glyph: "check"
            tintColor: control.surfaceColor
            visible: control.checked
        }
    }

    contentItem: Item {}
    background: Item {}

    SiteFocusRing {
        active: control.visualFocus
        focusColor: control.accentColor
        radius: 4
    }
}

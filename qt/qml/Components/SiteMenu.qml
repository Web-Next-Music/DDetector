pragma ComponentBehavior: Bound
import QtQuick
import QtQuick.Controls

Menu {
    id: control
    property color surfaceColor: "#171719"
    property color borderColor: "#242426"
    property real cornerRadius: 10

    // Native platform menus bypass our shared delegates and rounded surface.
    popupType: Popup.Item
    width: 190
    padding: 8
    spacing: 2
    background: Rectangle {
        color: control.surfaceColor
        radius: control.cornerRadius
        border.color: control.borderColor
    }
}

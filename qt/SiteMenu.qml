pragma ComponentBehavior: Bound
import QtQuick
import QtQuick.Controls

Menu {
    id: control
    property color surfaceColor: "#171719"
    property color borderColor: "#3397979f"
    property real cornerRadius: 10

    width: 190
    padding: 8
    spacing: 2
    background: Rectangle {
        color: control.surfaceColor
        radius: control.cornerRadius
        border.color: control.borderColor
    }
}

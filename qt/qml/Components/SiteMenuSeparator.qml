pragma ComponentBehavior: Bound
import QtQuick
import QtQuick.Controls

MenuSeparator {
    id: control
    property color borderColor: "#3397979f"
    contentItem: Rectangle {
        implicitHeight: 1
        color: control.borderColor
    }
}

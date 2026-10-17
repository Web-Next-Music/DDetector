pragma ComponentBehavior: Bound
import QtQuick
import QtQuick.Controls

MenuSeparator {
    id: control
    property color borderColor: "#242426"
    contentItem: Rectangle {
        implicitHeight: 1
        color: control.borderColor
    }
}

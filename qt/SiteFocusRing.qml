pragma ComponentBehavior: Bound
import QtQuick

Rectangle {
    property bool active: false
    property color focusColor: "#2bfef5"
    anchors.fill: parent
    anchors.margins: 2
    radius: 6
    color: "transparent"
    border.width: 2
    border.color: focusColor
    visible: active
    z: 10
    Accessible.ignored: true
}

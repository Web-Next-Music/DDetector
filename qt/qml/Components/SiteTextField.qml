pragma ComponentBehavior: Bound
import QtQuick
import QtQuick.Controls

TextField {
    id: control
    property bool focusRingEnabled: true
    implicitHeight: 36
    padding: 10
    leftPadding: 12
    rightPadding: 12
    selectByMouse: true
    ContextMenu.menu: SiteEditMenu { textControl: control }
    color: "#f0f0f4"
    placeholderTextColor: "#999999"
    selectionColor: "#142bfef5"
    selectedTextColor: "#f0f0f4"
    font.family: "DDetector Nunito"
    font.pixelSize: 14
    background: Rectangle {
        radius: 8
        color: "#0c0c0e"
        border.color: control.activeFocus ? "#332bfef5" : "#3397979f"
        opacity: control.enabled ? 1 : 0.55
    }
    SiteFocusRing {
        active: control.focusRingEnabled && control.activeFocus
    }
}

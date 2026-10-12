pragma ComponentBehavior: Bound
import QtQuick
import QtQuick.Controls

SiteButton {
    id: control
    required property var targetWindow
    required property int index
    readonly property string actionName: ["Minimize", "Maximize", "Close"][index]
    objectName: "window" + actionName + "Button"
    text: index === 1 && targetWindow.visibility === Window.Maximized ? "Restore" : actionName
    width: 48
    height: 48
    padding: 0
    iconOnly: true
    glyph: ["window-minus", "square", "window-x"][index]
    glyphSize: index === 1 ? 12 : 16
    textColor: hovered && index === 2 ? "white" : "#f0f0f4"
    hoverTextColor: textColor
    surfaceColor: "transparent"
    hoverColor: index === 2 ? "#f23f42" : "#171719"
    borderColor: "transparent"
    accentBorder: "transparent"
    cornerRadius: 0
    background: Item {
        Rectangle {
            anchors.fill: parent
            color: control.hovered ? control.hoverColor : "transparent"
        }
    }
    onClicked: {
        if (index === 0)
            targetWindow.showMinimized();
        else if (index === 1) {
            if (targetWindow.visibility === Window.Maximized)
                targetWindow.showNormal();
            else
                targetWindow.showMaximized();
        } else
            targetWindow.close();
    }
}

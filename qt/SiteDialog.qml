import QtQuick
import QtQuick.Controls

Dialog {
    id: dialog

    property url icon: ""
    property color surface: "#0c0c0e"
    property color surface2: "#171719"
    property color borderColor: "#3397979f"
    property color ink: "#f0f0f4"
    property color muted: "#666666"
    property color accentBg: "#142bfef5"
    property color accentBorder: "#332bfef5"
    property color overlayColor: "#a3000000"
    property string closeButtonObjectName: ""
    property string closeButtonAccessibleName: "Close"
    property bool closeEnabled: true
    signal closeRequested()

    onCloseRequested: close()
    modal: true
    focus: true
    parent: Overlay.overlay
    anchors.centerIn: parent
    padding: 20
    topPadding: 12
    standardButtons: Dialog.NoButton
    font.family: "DDetector Nunito"
    palette.window: surface
    palette.windowText: ink
    palette.text: ink

    header: DialogHeader {
        icon: dialog.icon
        title: dialog.title
        surface: dialog.surface
        surface2: dialog.surface2
        borderColor: dialog.borderColor
        ink: dialog.ink
        muted: dialog.muted
        accentBg: dialog.accentBg
        accentBorder: dialog.accentBorder
        closeButtonObjectName: dialog.closeButtonObjectName
        closeButtonAccessibleName: dialog.closeButtonAccessibleName
        closeEnabled: dialog.closeEnabled
        onCloseRequested: dialog.closeRequested()
    }
    footer: Item { implicitHeight: 0 }
    background: Rectangle {
        color: "#000000"
        radius: 18
        border.color: dialog.borderColor
    }
    Overlay.modal: Rectangle { color: dialog.overlayColor }
}

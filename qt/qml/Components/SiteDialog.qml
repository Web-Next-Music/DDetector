pragma ComponentBehavior: Bound
import QtQuick
import QtQuick.Controls
import QtQuick.Effects

Dialog {
    id: dialog

    property bool windowChromeRegistered: false
    function updateWindowChromeRegistration() {
        const owner = dialog.ApplicationWindow.window;
        if (!owner || owner.activeModalCount === undefined) return;
        const register = dialog.visible && dialog.modal;
        if (register === windowChromeRegistered) return;
        owner.activeModalCount += register ? 1 : -1;
        windowChromeRegistered = register;
    }
    onVisibleChanged: updateWindowChromeRegistration()
    onModalChanged: updateWindowChromeRegistration()
    Component.onCompleted: updateWindowChromeRegistration()
    Component.onDestruction: {
        const owner = dialog.ApplicationWindow.window;
        if (windowChromeRegistered && owner && owner.activeModalCount !== undefined)
            owner.activeModalCount -= 1;
    }

    property url icon: ""
    property url assetRoot: "qrc:/assets/"
    property color surface: "#0c0c0e"
    property color surface2: "#171719"
    property color borderColor: "#242426"
    property color ink: "#f0f0f4"
    property color muted: "#999999"
    property color accentBg: "#142bfef5"
    property color accentBorder: "#332bfef5"
    property color overlayColor: "#a3000000"
    property color backgroundColor: surface
    property string closeButtonObjectName: ""
    property string closeButtonAccessibleName: "Close"
    property bool closeEnabled: true
    signal closeRequested()

    onCloseRequested: close()

    modal: true
    focus: true
    parent: Overlay.overlay
    anchors.centerIn: parent
    padding: 8
    topPadding: 0
    standardButtons: Dialog.NoButton
    font.family: "DDetector Nunito"
    palette.window: surface
    palette.windowText: ink
    palette.text: ink

    header: DialogHeader {
        icon: dialog.icon
        assetRoot: dialog.assetRoot
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
        color: dialog.backgroundColor
        radius: 16
        border.color: dialog.borderColor
        layer.enabled: GraphicsInfo.api !== GraphicsInfo.Software
        layer.effect: MultiEffect {
            shadowEnabled: true
            shadowColor: "#40000000"
            shadowBlur: 1
            shadowVerticalOffset: 16
            shadowHorizontalOffset: 0
            shadowScale: 1
        }
    }
    enter: Transition {
            ParallelAnimation {
                NumberAnimation { property: "opacity"; from: 0; to: 1; duration: 180; easing.type: Easing.OutCubic }
                NumberAnimation { property: "scale"; from: 0.92; to: 1; duration: 220; easing.type: Easing.OutExpo }
            }
        }
        exit: Transition {
            ParallelAnimation {
                NumberAnimation { property: "opacity"; from: 1; to: 0; duration: 160; easing.type: Easing.InCubic }
                NumberAnimation { property: "scale"; from: 1; to: 0.95; duration: 160; easing.type: Easing.InCubic }
            }
        }
        Overlay.modal: Rectangle {
            color: dialog.overlayColor
            Behavior on opacity { NumberAnimation { duration: 180 } }
        }
}

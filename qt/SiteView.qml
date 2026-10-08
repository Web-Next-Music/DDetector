import QtQuick
import QtQuick.Controls
import QtQuick.Effects

SiteDialog {
    id: site
    objectName: "accessPopup"
    required property var ui
    property url artworkSource: "qrc:/assets/Kagami-access-denied.jpg"
    readonly property bool mobile: ui.width <= 480
    readonly property string error: backend.connectionError || ui.state.error || ""
    readonly property bool denied: !!ui.state.isBanned || (!!ui.state.authenticated && !ui.state.hasAccess && (!!ui.state.accessChecked || error === "DDetector access denied"))

    visible: ui.state.authenticated !== false && !ui.privileged && !ui.checkingAccess
    width: Math.min(ui.width - 32, 704)
    height: Math.max(mobile ? 304 : 368, panel.height + (mobile ? 88 : 100) + 2)
    closePolicy: Popup.NoAutoClose
    header: Item { implicitHeight: 0 }
    padding: 0
    topPadding: 0
    background: Rectangle {
        radius: 20
        color: site.ui.surface
        border.color: site.ui.borderColor
    }

    contentItem: Item {
        layer.enabled: true
        layer.effect: MultiEffect {
            maskEnabled: true
            maskSource: cardMask
        }
        Rectangle {
            id: cardMask
            anchors.fill: parent
            radius: 20
            layer.enabled: true
            visible: false
        }
        Item {
            id: artwork
            visible: !site.mobile
            x: 1
            y: 1
            width: (parent.width - 2) * 0.9 / 1.9
            height: parent.height - 2
            clip: true
            Image {
                objectName: "accessArtwork"
                readonly property real leftShift: 20
                width: Math.max(parent.width, parent.height * implicitWidth / Math.max(1, implicitHeight))
                height: Math.max(parent.height, parent.width * implicitHeight / Math.max(1, implicitWidth))
                x: Math.max(parent.width - width, (parent.width - width) / 2 - leftShift)
                y: (parent.height - height) / 2
                source: site.artworkSource
                fillMode: Image.PreserveAspectFit
            }
        }
        Column {
            id: panel
            readonly property real horizontalPadding: site.mobile ? 24 : 32
            x: (artwork.visible ? artwork.x + artwork.width : 1) + horizontalPadding
            width: parent.width - x - horizontalPadding - 1
            anchors.verticalCenter: parent.verticalCenter
            spacing: site.mobile ? 24 : 28
            Text {
                objectName: "accessMessage"
                width: parent.width
                text: site.ui.state.isBanned ? "Account banned"
                      : site.denied ? "Access denied"
                      : "Access check failed"
                color: site.ui.ink
                font.family: "Luckystar"
                font.weight: Font.Normal
                font.pixelSize: site.mobile ? 34 : 40
                wrapMode: Text.Wrap
                Accessible.role: Accessible.AlertMessage
                Accessible.name: text
            }
            Text {
                width: parent.width
                text: site.ui.state.isBanned ? "This account is not allowed to use DDetector."
                      : site.denied ? "You do not have permission to use DDetector. Please sign in with an account that has access."
                      : site.error
                color: site.ui.muted
                font.pixelSize: 14
                wrapMode: Text.WrapAnywhere
            }
            SiteButton {
                id: logoutButton
                objectName: "accessSwitchAccount"
                width: parent.width
                height: 46
                visible: !!site.ui.state.authenticated
                text: "Sign in with another account"
                font.family: "DDetector Quicksand"
                font.pixelSize: site.mobile ? 12 : 14
                font.weight: Font.DemiBold
                onClicked: backend.logout()
                contentItem: Text {
                    text: logoutButton.text
                    font: logoutButton.font
                    color: site.ui.ink
                    horizontalAlignment: Text.AlignHCenter
                    verticalAlignment: Text.AlignVCenter
                }
                background: Rectangle {
                    radius: 10
                    color: logoutButton.hovered && logoutButton.enabled ? site.ui.accentBg : site.ui.surface2
                    border.color: logoutButton.activeFocus ? site.ui.accent : logoutButton.hovered && logoutButton.enabled ? site.ui.accentBorder : site.ui.borderColor
                    border.width: logoutButton.activeFocus ? 2 : 1
                }
            }
        }
    }
}

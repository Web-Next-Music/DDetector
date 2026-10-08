import QtQuick
import QtQuick.Controls
import QtQuick.Layouts
import QtQuick.Effects

Item {
    id: site
    required property var ui
    signal signIn
    property bool checking: (ui.state.authenticated === undefined || (!!ui.state.authenticated && !ui.state.accessChecked && !ui.state.isBanned)) && !ui.state.error && backend.connectionError === ""
    property bool loginVisible: ui.state.authenticated === false
    property string error: backend.connectionError || ui.state.error || ""
    readonly property bool mobile: width <= 480
    readonly property bool shortScreen: height < 420

    Rectangle {
        anchors.fill: parent
        color: site.ui.bg
    }

    ScrollView {
        id: scroll
        anchors.fill: parent
        contentWidth: availableWidth
        contentHeight: Math.max(site.height, content.height + 32)
        clip: true
        ScrollBar.horizontal.policy: ScrollBar.AlwaysOff

        Item {
            id: content
            width: scroll.availableWidth
            height: site.loginVisible ? loginCard.height : accessPanel.height
            y: Math.max(16, (site.height - height) / 2)

            Rectangle {
                id: loginCard
                visible: site.loginVisible
                width: Math.max(0, Math.min(parent.width - 32, 704))
                height: Math.max(site.mobile ? 304 : site.shortScreen ? 288 : 368,
                                 loginPanel.height + 2 * loginPanel.verticalPadding + 2)
                anchors.horizontalCenter: parent.horizontalCenter
                radius: 20
                color: site.ui.surface
                border.color: site.ui.borderColor

                Item {
                    id: artwork
                    visible: !site.mobile
                    x: 1
                    y: 1
                    width: (loginCard.width - 2) * 0.9 / 1.9
                    height: loginCard.height - 2
                    clip: true

                    Image {
                        width: Math.max(parent.width, parent.height * implicitWidth / Math.max(1, implicitHeight))
                        height: parent.height
                        x: (parent.width - width) * 0.2
                        source: "qrc:/assets/Kagami.webp"
                        fillMode: Image.PreserveAspectCrop
                    }
                    // Cover only the outside corners, without an offscreen image layer.
                    Canvas {
                        anchors.fill: parent
                        property color cornerColor: site.ui.bg
                        onCornerColorChanged: requestPaint()
                        onWidthChanged: requestPaint()
                        onHeightChanged: requestPaint()
                        onPaint: {
                            const ctx = getContext("2d");
                            ctx.clearRect(0, 0, width, height);
                            ctx.fillStyle = cornerColor;
                            ctx.beginPath();
                            ctx.moveTo(0, 0);
                            ctx.lineTo(19, 0);
                            ctx.quadraticCurveTo(0, 0, 0, 19);
                            ctx.closePath();
                            ctx.moveTo(0, height);
                            ctx.lineTo(0, height - 19);
                            ctx.quadraticCurveTo(0, height, 19, height);
                            ctx.closePath();
                            ctx.fill();
                        }
                    }
                }

                Column {
                    id: loginPanel
                    readonly property real horizontalPadding: site.mobile ? 24 : 32
                    readonly property real verticalPadding: site.shortScreen ? 24 : site.mobile ? 32 : 44
                    x: (site.mobile ? 1 : artwork.x + artwork.width) + horizontalPadding
                    y: (loginCard.height - height) / 2
                    width: Math.max(0, loginCard.width - x - horizontalPadding - 1)
                    spacing: site.mobile ? 24 : 28

                    Text {
                        objectName: site.loginVisible ? "accessMessage" : ""
                        width: parent.width
                        text: "Sign in"
                        color: site.ui.ink
                        font.family: "Luckystar"
                        font.weight: Font.Normal
                        font.pixelSize: site.mobile ? 34 : 40
                        wrapMode: Text.Wrap
                    }

                    Column {
                        width: parent.width
                        spacing: 10
                        Repeater {
                            model: ["github", "discord", "spotify"]
                            Button {
                                id: providerButton
                                required property string modelData
                                objectName: "login-" + modelData
                                width: parent.width
                                height: 46
                                text: backend.pendingProvider === modelData ? "Redirecting…" : "Continue with " + ({github: "GitHub", discord: "Discord", spotify: "Spotify"})[modelData]
                                enabled: !site.ui.state.loginPending && backend.pendingProvider === ""
                                opacity: enabled ? 1 : 0.65
                                font.family: "DDetector Quicksand"
                                font.pixelSize: site.mobile ? 12 : 14
                                font.weight: Font.DemiBold
                                Accessible.name: text
                                onClicked: backend.login(modelData)
                                HoverHandler {
                                    cursorShape: providerButton.enabled ? Qt.PointingHandCursor : Qt.ArrowCursor
                                }
                                contentItem: Item {
                                    Row {
                                        anchors.centerIn: parent
                                        spacing: site.mobile ? 8 : 10
                                        Image {
                                            width: 17
                                            height: 17
                                            anchors.verticalCenter: parent.verticalCenter
                                            source: "qrc:/assets/" + providerButton.modelData + "-dark.svg"
                                        }
                                        Text {
                                            text: providerButton.text
                                            font: providerButton.font
                                            color: site.ui.ink
                                            anchors.verticalCenter: parent.verticalCenter
                                        }
                                    }
                                }
                                background: Rectangle {
                                    radius: 10
                                    color: providerButton.hovered && providerButton.enabled ? site.ui.accentBg : site.ui.surface2
                                    border.color: providerButton.activeFocus ? site.ui.accent : providerButton.hovered && providerButton.enabled ? site.ui.accentBorder : site.ui.borderColor
                                    border.width: providerButton.activeFocus ? 2 : 1
                                }
                            }
                        }
                        Rectangle {
                            width: parent.width
                            height: visible ? loginError.implicitHeight + 16 : 0
                            visible: site.error !== ""
                            radius: 10
                            color: site.ui.surface2
                            border.color: site.ui.ignoredColor
                            Text {
                                id: loginError
                                x: 12
                                y: 8
                                width: parent.width - 24
                                text: site.error
                                color: site.ui.ignoredColor
                                font.pixelSize: 13
                                wrapMode: Text.WrapAnywhere
                            }
                        }
                    }
                }
            }

            Column {
                id: accessPanel
                visible: !site.loginVisible
                width: Math.max(0, Math.min(parent.width - 40, 360))
                anchors.horizontalCenter: parent.horizontalCenter
                spacing: 16
                Text {
                    width: parent.width
                    text: "DDetector"
                    horizontalAlignment: Text.AlignHCenter
                    color: site.ui.ink
                    font.family: "DDetector Quicksand"
                    font.pixelSize: 32
                }
                Text {
                    objectName: site.loginVisible ? "" : "accessMessage"
                    width: parent.width
                    horizontalAlignment: Text.AlignHCenter
                    wrapMode: Text.Wrap
                    color: site.ui.muted
                    text: site.checking ? "Checking access…"
                          : site.ui.state.isBanned ? "Your account is banned."
                          : site.error !== "" ? "Unable to check access."
                          : "Your account does not have access to DDetector."
                }

                Text {
                    width: parent.width
                    visible: site.error !== ""
                    text: site.error
                    color: site.ui.ignoredColor
                    horizontalAlignment: Text.AlignHCenter
                    wrapMode: Text.Wrap
                }
            }
        }
    }
}

import QtQuick
import QtQuick.Controls

Item {
    id: site
    required property var ui
    property string error: backend.connectionError || ui.state.error || ""

    Rectangle {
        anchors.fill: parent
        color: site.ui.bg
    }

    ScrollView {
        id: scroll
        anchors.fill: parent
        contentWidth: availableWidth
        contentHeight: Math.max(site.height, accessPanel.height + 32)
        clip: true
        ScrollBar.horizontal.policy: ScrollBar.AlwaysOff

        Column {
            id: accessPanel
            width: Math.max(0, Math.min(scroll.availableWidth - 40, 360))
            x: (scroll.availableWidth - width) / 2
            y: Math.max(16, (site.height - height) / 2)
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
                objectName: "accessMessage"
                width: parent.width
                horizontalAlignment: Text.AlignHCenter
                wrapMode: Text.Wrap
                color: site.ui.muted
                text: site.ui.state.isBanned ? "Your account is banned."
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

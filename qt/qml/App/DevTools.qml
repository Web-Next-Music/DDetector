pragma ComponentBehavior: Bound
import QtQuick
import QtQuick.Layouts

SiteMenuItem {
    id: root
    property var updater: null
    objectName: "accountDevMenuEntry"
    text: "Dev menu"
    glyph: "flask-conical"
    onTriggered: devDialog.open()
    SiteDialog {
        id: devDialog
        objectName: "devToolsDialog"
        title: "Developer menu"
        icon: "qrc:/assets/lucide-flask-conical.svg"
        width: Math.min(500, parent ? parent.width - 32 : 500)
        contentItem: ColumnLayout {
            SiteSettingsCard {
                objectName: "developerUpdateCard"
                Layout.fillWidth: true
                compact: true
                SiteQuote {
                    objectName: "updateExplanationQuote"
                    Layout.fillWidth: true
                    text: "Force offers the latest release even if it is already installed"
                }
                RowLayout {
                    Layout.fillWidth: true
                    uniformCellSizes: true
                    spacing: 8
                    SiteButton {
                        objectName: "forceUpdateButton"
                        Layout.fillWidth: true
                        Layout.minimumHeight: implicitHeight
                        Layout.maximumHeight: implicitHeight
                        text: "Force latest release"
                        glyph: "download"
                        accented: true
                        enabled: !!root.updater && !root.updater.busy
                        onClicked: {
                            root.updater.forceAvailable();
                            devDialog.close();
                        }
                    }

                    SiteButton {
                        objectName: "realUpdateButton"
                        Layout.fillWidth: true
                        Layout.minimumHeight: implicitHeight
                        Layout.maximumHeight: implicitHeight
                        text: "Check for updates"
                        enabled: !!root.updater && !root.updater.busy
                        onClicked: root.updater.check()
                    }
                }
            }
        }
    }
}

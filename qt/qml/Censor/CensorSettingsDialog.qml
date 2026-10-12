pragma ComponentBehavior: Bound
import QtQuick
import QtQuick.Controls

SiteDialog {
    id: dialog
    property alias bridge: panel.bridge
    property alias unavailableReason: panel.unavailableReason
    property alias saving: panel.saving
    property alias saveError: panel.saveError
    property alias draft: panel.draft

    property alias validationError: panel.validationError
    property alias editable: panel.editable
    title: "Analysis settings"
    icon: "qrc:/assets/lucide-settings.svg"
    height: Math.min(760, parent ? Math.max(240, parent.height - 32) : 760)
    closeEnabled: !saving
    spacing: 8
    closePolicy: saving ? Popup.NoAutoClose : Popup.CloseOnEscape | Popup.CloseOnPressOutside
    onOpened: panel.loadSettings()
    function setValue(key, value) { panel.setValue(key, value); }

    function saveSettings() { panel.saveSettings(); }
    contentItem: ScrollView {
        id: settingsScroll
        objectName: "censorSettingsScroll"
        contentWidth: availableWidth
        rightPadding: 0
        bottomPadding: 0
        clip: true
        CensorSettingsPanel {
            id: panel
            width: settingsScroll.availableWidth

            onCancelRequested: dialog.close()
        }
    }

}

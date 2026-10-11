pragma ComponentBehavior: Bound
import QtQuick
import QtQuick.Controls
import QtQuick.Layouts

Rectangle {
    id: dialog
    component SettingsDescription: SiteQuote {
        Layout.fillWidth: true
        Layout.minimumWidth: 0
        Layout.leftMargin: parent && parent.parent && parent.parent.leadingGlyph ? -30 : 0
        Layout.topMargin: 10
        textColor: "#666666"
        backgroundRadius: 8
    }
    property var bridge: null
    property string unavailableReason: ""
    property bool saving: false
    property string saveError: ""
    property var draft: defaults()

    readonly property string validationError: validateDraft()
    readonly property bool editable: !saving && !unavailableReason
    readonly property int fieldWidth: Math.min(180, Math.max(100, width * 0.38))
    readonly property var modes: ["eco", "balanced", "performance"]


    property bool draftDirty: false
    property string saveFeedback: ""
    property color surface: "#0c0c0e"
    property color ink: "#f0f0f4"
    property color muted: "#999999"
    property var settingsSubmitted: ({})

    property bool settingsQueued: false

    Timer {
        id: autosave
        interval: 400
        onTriggered: dialog.flushAutosave()
    }
    function flushAutosave() {
        if (!bridge || unavailableReason || saving || bridge.censorBusy) return;
        if (settingsQueued && !validationError) saveSettings();
    }
    onUnavailableReasonChanged: autosave.restart()
    signal saved()
    signal cancelRequested()
    implicitHeight: sections.implicitHeight
    color: "transparent"
    radius: 0
    border.width: 0
    border.color: "transparent"
    Component.onCompleted: loadSettings()
    onBridgeChanged: { if (!draftDirty && !saving) loadSettings(); }
    function defaults() {
        return {workers: 2, mode: "balanced", cpu_threads: 1,
            candidate_limit: 20, min_confidence: 0.75};
    }
    function loadSettings() {
        if (draftDirty || saving || settingsQueued) return;
        draft = Object.assign(defaults(), bridge ? bridge.censorState.settings || {} : {});
        draftDirty = false;
        saveError = "";
        saveFeedback = "";

    }

    function setValue(key, value) {
        const next = Object.assign({}, draft);
        next[key] = value;
        if (key === "mode") next.workers = value === "eco" ? 1 : value === "performance" ? 4 : 2;

        draft = next;
        draftDirty = true;
        settingsQueued = true;
        autosave.restart();
        saveFeedback = "";
        saveError = "";
    }

    function validateDraft() {
        if (!Number.isInteger(draft.cpu_threads) || draft.cpu_threads < 1 || draft.cpu_threads > 64) return "CPU threads must be between 1 and 64";
        if (modes.indexOf(draft.mode) < 0) return "Unsupported mode";
        if (!Number.isInteger(draft.workers) || draft.workers < 1 || draft.workers > 32) return "Workers must be between 1 and 32";
        if (!Number.isInteger(draft.candidate_limit) || draft.candidate_limit < 1 || draft.candidate_limit > 100) return "Candidate limit must be between 1 and 100";
        const confidence = Number(draft.min_confidence);
        if (!isFinite(confidence) || confidence <= 0 || confidence > 1) return "Minimum confidence must be greater than 0 and at most 1";
        return "";
    }
    function saveSettings() {
        if (!editable || validationError || !bridge || bridge.censorBusy) return;
        saveError = "";
        saving = true;
        const settings = Object.assign({}, draft);
        settings.min_confidence = Number(draft.min_confidence);
        settingsSubmitted = Object.assign({}, draft);
        settingsQueued = false;
        bridge.censorCommand("settings", {settings: settings});
    }
    Connections {
        target: dialog.bridge
        ignoreUnknownSignals: true
        function onCensorBusyChanged() { autosave.restart(); }
        function onCensorStateChanged() {
            autosave.restart();
            // Polling must not overwrite edits or clear operation feedback.
            if (!dialog.draftDirty && !dialog.saving)
                dialog.draft = Object.assign(dialog.defaults(), dialog.bridge.censorState.settings || {});
        }
        function onCensorCommandFinished(action, success, error) {
            if (action !== "settings" || !dialog.saving) return;
            dialog.saving = false;
            if (success) {
                dialog.draftDirty = JSON.stringify(dialog.draft) !== JSON.stringify(dialog.settingsSubmitted);
                dialog.saveFeedback = "Settings saved";
                dialog.saved();
            } else dialog.saveError = error || "The server did not accept the settings. Your changes have been retained";
            autosave.restart();
        }
    }
    ColumnLayout {
        id: sections
        anchors.left: parent.left
        anchors.right: parent.right
        anchors.top: parent.top
        spacing: 8

        GridLayout {
            Layout.fillWidth: true
            columns: dialog.width >= 800 ? 2 : 1
            columnSpacing: 10
            rowSpacing: 20
            ColumnLayout {
                Layout.fillWidth: true
                Layout.minimumWidth: 0
                Layout.alignment: Qt.AlignTop
                spacing: 20
            SiteSectionCard {
                id: censorProcessingSection
                objectName: "censorProcessingSection"
                compact: true
                Layout.fillWidth: true
                Layout.minimumWidth: 0
                SiteSettingsCard {
                    compact: true
                    objectName: "censorProcessingHeader"
                    Layout.fillWidth: true; title: "Processing resources"; glyph: "settings"
                }
                SiteSettingsCard {
                    objectName: "censorModeCard"
                    compact: true
                    Layout.fillWidth: true
                    leadingGlyph: "sliders-horizontal"
                    RowLayout {
                        Layout.fillWidth: true
                        Label { Layout.minimumWidth: 0; wrapMode: Text.WordWrap; text: "Mode"; color: dialog.ink; Layout.fillWidth: true }
                        SiteComboBox {
                            Layout.preferredWidth: dialog.fieldWidth; Layout.minimumWidth: dialog.fieldWidth; Layout.maximumWidth: dialog.fieldWidth; Layout.preferredHeight: 36
                            objectName: "censorMode"; model: dialog.modes
                            currentIndex: dialog.modes.indexOf(dialog.draft.mode)
                            enabled: dialog.editable; Accessible.name: "Analysis mode"
                            onActivated: function(index) { dialog.setValue("mode", dialog.modes[index]); }
                        }
                    }
                    SettingsDescription { text: "Eco uses 1 worker, Balanced 2, Performance 4. You can adjust the limits below" }
                }
                SiteSettingsCard {
                    objectName: "censorWorkersCard"
                    compact: true
                    Layout.fillWidth: true
                    leadingGlyph: "layers"
                    RowLayout {
                        Layout.fillWidth: true
                        Label { Layout.minimumWidth: 0; wrapMode: Text.WordWrap; text: "Parallel workers"; color: dialog.ink; Layout.fillWidth: true }
                        SiteSpinBox { Layout.preferredWidth: dialog.fieldWidth; Layout.minimumWidth: dialog.fieldWidth; Layout.maximumWidth: dialog.fieldWidth; objectName: "censorWorkers"; from: 1; to: 32; value: dialog.draft.workers; enabled: dialog.editable; Accessible.name: "Analysis workers"; onValueModified: dialog.setValue("workers", value) }
                    }
                }
                SiteSettingsCard {
                    objectName: "censorCpuThreadsCard"
                    compact: true
                    Layout.fillWidth: true
                    leadingGlyph: "cpu"
                    RowLayout {
                        Layout.fillWidth: true
                        Label { Layout.minimumWidth: 0; wrapMode: Text.WordWrap; text: "CPU threads per worker"; color: dialog.ink; Layout.fillWidth: true }
                        SiteSpinBox { Layout.preferredWidth: dialog.fieldWidth; Layout.minimumWidth: dialog.fieldWidth; Layout.maximumWidth: dialog.fieldWidth; objectName: "censorCpuThreads"; from: 1; to: 64; value: dialog.draft.cpu_threads; enabled: dialog.editable; Accessible.name: "CPU threads per worker"; onValueModified: dialog.setValue("cpu_threads", value) }
                    }
                    SettingsDescription { text: "Thread limits apply to FFmpeg. More workers and threads increase CPU usage" }
                }
            }

            SiteSectionCard {
                id: censorComparisonSection
                objectName: "censorComparisonSection"
                compact: true
                Layout.fillWidth: true
                Layout.minimumWidth: 0
                SiteSettingsCard {
                    compact: true
                    objectName: "censorComparisonHeader"
                    Layout.fillWidth: true; title: "Candidate search"; glyph: "search"
                }
                SiteSettingsCard {
                    objectName: "censorCandidateLimitCard"
                    compact: true
                    Layout.fillWidth: true
                    leadingGlyph: "list-filter"
                    RowLayout {
                        Layout.fillWidth: true
                        Label { Layout.minimumWidth: 0; wrapMode: Text.WordWrap; text: "Candidate limit"; color: dialog.ink; Layout.fillWidth: true }
                        SiteSpinBox { Layout.preferredWidth: dialog.fieldWidth; Layout.minimumWidth: dialog.fieldWidth; Layout.maximumWidth: dialog.fieldWidth; objectName: "censorCandidateLimit"; from: 1; to: 100; value: dialog.draft.candidate_limit; enabled: dialog.editable; Accessible.name: "Candidate limit"; onValueModified: dialog.setValue("candidate_limit", value) }
                    }
                }

            }
            }
        }

    }
}

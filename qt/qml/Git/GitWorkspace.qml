pragma ComponentBehavior: Bound

import QtQuick
import QtQuick.Controls
import QtQuick.Layouts
import QtQuick.Effects
import DDetector.Syntax 1.0

// Integration: set workspaceBackend to a GitWorkspace QObject. Route Window.onClosing
// through requestClose(callback), vetoing the initial close; callback performs a guarded
// second close. Alternatively use closeApproved. Never call Qt.quit() before approval.

// Commands are unrestricted trusted input; editor confinement does not sandbox a shell.
Item {
    id: root
    property var workspaceBackend: null
    property var closeCallback: null
    property bool pageActive: true
    property bool expanded: false
        property color bg: "#000000"
    property bool historyTab: false
    property bool showingDiff: false
    function showDiff(path) {
        showingDiff = true
        workspaceBackend.viewDiff(path || "")
    }
    property bool trackedExpanded: true
    property bool untrackedExpanded: true
    property bool commitStaged: false
    property bool amendCommit: false
    property bool signoffCommit: false
    property bool skipCommitHooks: false
    function submitCommit() {
        if (available && !workspaceBackend.gitBusy && commitMessage.text.trim())
            workspaceBackend.commitWithOptions(commitMessage.text, amendCommit, signoffCommit, skipCommitHooks, expanded && commitStaged)
    }
    property string pendingAction: ""
    readonly property var changes: available ? workspaceBackend.changedFiles : []
    readonly property int additions: changes.reduce((sum, file) => sum + (file.additions || 0), 0)
    readonly property int deletions: changes.reduce((sum, file) => sum + (file.deletions || 0), 0)
    function confirmAction(action, description) {
        pendingAction = action
        confirmation.description = description
        confirmation.open()
    }
    onWorkspaceBackendChanged: {
        editor.syncDocument()
        if (workspaceBackend) workspaceBackend.activate()
    }
    onPageActiveChanged: if (!pageActive && available && workspaceBackend.terminalRunning) workspaceBackend.stopTerminal()
    Component.onCompleted: if (workspaceBackend) workspaceBackend.activate()
    readonly property bool available: workspaceBackend !== null && workspaceBackend !== undefined
    readonly property bool fileLoading: available && workspaceBackend.fileLoading
    readonly property string gitNotification: available ? workspaceBackend.gitMessage : ""
    onGitNotificationChanged: if (gitNotification) notify(gitNotification)
    readonly property color surface: "#0c0c0e"
    readonly property color raised: "#171719"
    readonly property color accent: "#2bfef5"
    signal closeApproved()
    signal notify(string message)



    function requestClose(callback) {
        closeCallback = typeof callback === "function" ? callback : null
        if (available) workspaceBackend.requestClose()
        else approveClose()
    }
    function approveClose() {
        const callback = closeCallback
        closeCallback = null
        closeApproved()
        if (callback) callback()
    }
    function resolve(decision) {
        if (available) workspaceBackend.resolveUnsaved(decision)
        // Save failures retain dirty state and the backend's pending operation.
        if (decision !== "save" || !available || !workspaceBackend.dirty)
            unsaved.close()
    }

    Connections {
        target: root.workspaceBackend
        function onUnsavedPrompt() { unsaved.open() }
        function onCloseApproved() { root.approveClose() }
        function onErrorChanged() {
            if (root.available && root.workspaceBackend.error)
                root.notify(root.workspaceBackend.error)
        }
    }
    // The host provides the workspace background; each surface is drawn only once.
    Label {
        textFormat: Text.PlainText
        anchors.centerIn: parent
        width: Math.max(0, parent.width - 48)
        visible: !root.available || !root.workspaceBackend.rootPath
        text: !root.available || !root.workspaceBackend.repositoryPath
              ? "Set a Git repository path in Settings to open the workspace"
              : root.workspaceBackend.resolving ? "Opening repository…"
              : root.workspaceBackend.error || "Repository unavailable. Check the path in Settings and ensure Git is installed"
        color: "#eeeeee"
        horizontalAlignment: Text.AlignHCenter
        wrapMode: Text.Wrap
    }
    Shortcut {
        sequence: "Ctrl+Shift+Return"
        enabled: root.visible && root.available && !root.workspaceBackend.gitBusy
        onActivated: { root.amendCommit = true; root.submitCommit() }
    }
    Shortcut {
        sequence: StandardKey.Save
        enabled: root.visible && root.available && !root.fileLoading && !editor.loadingDocument && !!root.workspaceBackend.filePath
        onActivated: root.workspaceBackend.save()
    }

    ColumnLayout {
        anchors.fill: parent
        anchors.margins: 0
        spacing: 0

        SplitView {
            objectName: "gitMainSplit"
            Layout.fillWidth: true
            Layout.fillHeight: true
            orientation: root.expanded ? Qt.Horizontal : Qt.Vertical
            palette.button: root.raised
            palette.buttonText: "#eeeeee"
            palette.window: root.surface
            palette.windowText: "#eeeeee"
            palette.text: "#eeeeee"
            palette.base: root.surface
            palette.highlight: root.accent
            visible: root.available && !!root.workspaceBackend.rootPath
            handle: Rectangle {
                implicitHeight: 1
                implicitWidth: 1
                color: "#242426"
            }
            Item {
                objectName: "gitEditorSplit"
                SplitView.fillHeight: true
                SplitView.fillWidth: true
                SplitView.minimumWidth: 280
                SplitView.minimumHeight: 80
                SplitView {
                anchors.fill: parent
                orientation: Qt.Horizontal
                handle: Rectangle {
                    implicitWidth: 1
                    color: "#242426"
                }
                Rectangle {
                    SplitView.preferredWidth: 240
                    SplitView.minimumWidth: 140
                    color: root.surface
                    radius: 0
                    ColumnLayout {
                        anchors.fill: parent
                        anchors.topMargin: 8
                        anchors.bottomMargin: 8
                        spacing: 6

                        ListView {
                            id: fileTree
                            Layout.fillWidth: true
                            Layout.fillHeight: true
                            clip: true
                            model: root.available ? root.workspaceBackend.tree : []
                            cacheBuffer: 0
                            reuseItems: true
                            ScrollBar.vertical: ScrollBar {}
                            delegate: ItemDelegate {
                                id: treeRow
                                SiteFocusRing { active: treeRow.visualFocus }
                                required property var modelData
                                width: fileTree.width
                                height: 26
                                HoverHandler { cursorShape: treeRow.enabled ? Qt.PointingHandCursor : Qt.ArrowCursor }
                                text: modelData.name
                                readonly property color fileColor: modelData.statusColor || (modelData.path === (root.available ? root.workspaceBackend.filePath : "") ? root.accent : "#d0d0d5")
                                leftPadding: 8 + modelData.depth * 16
                                contentItem: Item {
                                    Image {
                                        width: 10; height: 10
                                        anchors.verticalCenter: parent.verticalCenter
                                        visible: treeRow.modelData.directory
                                        source: "qrc:/assets/lucide-chevron-" + (treeRow.modelData.expanded ? "down" : "right") + ".svg"
                                        layer.enabled: true
                                        layer.effect: MultiEffect { brightness: 1; colorization: 1; colorizationColor: "#999999" }
                                    }
                                    FileIcon {
                                        x: 16
                                        width: 14; height: 14
                                        anchors.verticalCenter: parent.verticalCenter
                                        fileName: treeRow.modelData.path
                                        directory: treeRow.modelData.directory
                                        expanded: treeRow.modelData.expanded
                                    }
                                    Text {
                                        textFormat: Text.PlainText
                                        x: 36
                                        width: Math.max(0, parent.width - x)
                                        height: parent.height
                                        text: treeRow.text
                                        font.pixelSize: 12
                                        verticalAlignment: Text.AlignVCenter
                                        color: treeRow.fileColor
                                        elide: Text.ElideRight
                                    }
                                }
                                background: Rectangle {
                                    color: treeRow.modelData.path === (root.available ? root.workspaceBackend.filePath : "") ? "#142bfef5" : treeRow.hovered ? "#242426" : "transparent"
                                    Rectangle { width: 2; height: parent.height; visible: treeRow.modelData.path === (root.available ? root.workspaceBackend.filePath : ""); color: root.accent }
                                }
                                SiteToolTip { text: treeRow.modelData.path }
                                onClicked: {
                                    if (modelData.directory) root.workspaceBackend.toggleDirectory(modelData.path)
                                    else root.workspaceBackend.openFile(modelData.path)
                                }
                            }
                        }
                        SiteButton {
                            objectName: "gitCompactPush"
                            readonly property int aheadCount: root.available ? root.workspaceBackend.aheadCount : 0
                            visible: !root.expanded && aheadCount > 0
                            Layout.fillWidth: true
                            Layout.leftMargin: 8
                            Layout.rightMargin: 8
                            text: aheadCount + " Push"
                            glyph: "arrow-up"
                            glyphSpacing: 2
                            accented: true
                            enabled: root.available && !root.workspaceBackend.gitBusy
                            onClicked: root.workspaceBackend.gitAction("push")
                            SiteToolTip { text: "Push local commits" }
                        }
                    }
                }
                Rectangle {
                    objectName: "gitEditorPane"
                    SplitView.fillWidth: true
                    SplitView.minimumWidth: 180
                    color: root.bg
                    radius: 0
                    ColumnLayout {
                        anchors.fill: parent
                        anchors.margins: 0
                        spacing: 0
                        RowLayout {
                            id: breadcrumbRow
                            objectName: "gitEditorHeader"
                            Layout.fillWidth: true
                            Layout.minimumHeight: gitTabs.height + 1
                            Layout.preferredHeight: gitTabs.height + 1
                            Layout.maximumHeight: gitTabs.height + 1
                            visible: !root.showingDiff && root.available && (root.fileLoading || !!root.workspaceBackend.filePath)
                            spacing: 0
                            // Path is a breadcrumb, not a destructive navigation operation.
                            Label {
                                textFormat: Text.PlainText
                                Layout.minimumWidth: 0
                                Layout.fillWidth: true
                                Layout.fillHeight: true
                                leftPadding: 12
                                rightPadding: 12
                                verticalAlignment: Text.AlignVCenter
                                font.family: "monospace"
                                font.pixelSize: 12
                                background: Rectangle {
                                    color: root.raised
                                    Rectangle { anchors.bottom: parent.bottom; width: parent.width; height: 1; color: "#242426" }
                                }
                                text: root.fileLoading ? "Loading " + root.workspaceBackend.loadingFilePath + "…"
                                      : !root.showingDiff && root.available ? root.workspaceBackend.filePath + (root.workspaceBackend.dirty ? " *" : "") : ""
                                color: root.accent
                                elide: Text.ElideMiddle
                            }

                        }
                        ScrollView {
                            id: editorScroll
                            visible: !root.showingDiff && root.available && !!root.workspaceBackend.filePath
                            Layout.fillWidth: true
                            Layout.fillHeight: true
                            padding: 0
                            leftPadding: lineNumbers.visible ? lineNumbers.width : 0
                            FontMetrics { id: editorMetrics; font: editor.font }
                            Item {
                                id: lineNumbers
                                parent: editorScroll
                                z: 1
                                objectName: "editorLineNumbers"
                                visible: editor.enabled && root.available && !root.workspaceBackend.largeFileMode
                                x: 0
                                y: editorScroll.topPadding
                                width: Math.max(32, editorMetrics.averageCharacterWidth * String(editor.lineCount).length + 16)
                                height: editorScroll.availableHeight
                                clip: true
                                readonly property real scrollY: (editorScroll.contentItem as Flickable)?.contentY ?? 0
                                // PlainText + NoWrap has uniform line boxes. Use Qt's maintained
                                // lineCount, not a JS copy/index of the entire document on every edit.
                                readonly property int firstLine: Math.max(0, Math.min(editor.lineCount - 1,
                                    Math.floor((scrollY - editor.topPadding) / editor.lineHeight)))
                                Repeater {
                                    model: Math.max(0, Math.min(editor.lineCount - lineNumbers.firstLine, Math.ceil(lineNumbers.height / editor.lineHeight) + 2))
                                    Text {
                                        textFormat: Text.PlainText
                                        required property int index
                                        readonly property int lineIndex: lineNumbers.firstLine + index
                                        x: 0
                                        y: editor.topPadding + lineIndex * editor.lineHeight - lineNumbers.scrollY
                                        width: lineNumbers.width - 8
                                        height: editor.lineHeight
                                        text: lineIndex + 1
                                        font: editor.font
                                        color: "#999999"
                                        horizontalAlignment: Text.AlignRight
                                        verticalAlignment: Text.AlignVCenter
                                        Accessible.ignored: true
                                    }
                                }
                            }
                            background: Rectangle {
                                color: "#000000"
                                radius: 0
                            }
                            TextArea {
                                id: editor
                                objectName: "workspaceEditor"
                                padding: 0
                                Accessible.name: "File editor: " + (root.available ? root.workspaceBackend.filePath : "")
                                enabled: root.available && !!root.workspaceBackend.filePath
                                readOnly: root.fileLoading || loadingDocument
                                textFormat: TextEdit.PlainText
                                SyntaxHighlighter {
                                    id: syntaxHighlighter
                                    // QQuickTextEdit lays out the complete document. Avoid combining
                                    // full-document highlighting with pathological scene-graph geometry.
                                    document: root.available && !root.workspaceBackend.largeFileMode
                                              ? editor.textDocument : null
                                    filePath: root.available ? root.workspaceBackend.filePath : ""
                                }
                                property bool loadingDocument: false
                                property var loadedBackend: null
                                property int loadedRevision: -1
                                function syncDocument() {
                                    const backend = root.available ? root.workspaceBackend : null
                                    const value = backend ? backend.text : ""
                                    // A local edit is already in this document; retain incremental highlighting.
                                    if (!loadingDocument && loadedBackend === backend
                                            && loadedRevision === (backend ? backend.documentRevision : -1)
                                            && text === value)
                                        return
                                    syntaxHighlighter.beginDocumentReplacement()
                                    loadingDocument = true
                                    loadedBackend = null
                                    loadedRevision = -1
                                    if (text !== value) text = value
                                    // Establish identity only after the buffer has been installed.
                                    loadedRevision = backend ? backend.documentRevision : -1
                                    loadedBackend = backend
                                    loadingDocument = false
                                    syntaxHighlighter.endDocumentReplacement()
                                }
                                Component.onCompleted: syncDocument()
                                Connections {
                                    target: root.workspaceBackend
                                    function onDocumentAboutToChange() {
                                        syntaxHighlighter.beginDocumentReplacement()
                                        editor.loadingDocument = true
                                        editor.loadedRevision = -1
                                    }
                                    function onDocumentChanged() { editor.syncDocument(); root.showingDiff = false }
                                }
                                onTextChanged: {
                                    if (!loadingDocument && !root.fileLoading && root.available
                                            && loadedBackend === root.workspaceBackend
                                            && loadedRevision === root.workspaceBackend.documentRevision
                                            && text !== root.workspaceBackend.text)
                                        root.workspaceBackend.editDocument(loadedRevision, text)
                                }
                                placeholderText: ""
                                color: "#eeeeee"
                                selectionColor: root.accent
                                selectedTextColor: root.surface
                                placeholderTextColor: "#898991"
                                font.family: "monospace"
                                font.pixelSize: 14
                                readonly property real lineHeight: Math.max(1, positionToRectangle(0).height)
                                wrapMode: root.available && root.workspaceBackend.largeFileMode
                                          ? TextEdit.WrapAnywhere : TextEdit.NoWrap
                                selectByMouse: true
                                ContextMenu.menu: SiteEditMenu {
                                    objectName: "editorContextMenu"
                                    textControl: editor
                                }
                                background: null
                            }
                        }
                        GitDiffView {
                            Layout.fillWidth: true
                            Layout.fillHeight: true
                            visible: root.showingDiff
                            bg: root.bg
                            headerHeight: gitTabs.height
                            diffText: root.available ? root.workspaceBackend.diffText : ""
                        }
                        Item {
                            Layout.fillWidth: true
                            Layout.fillHeight: true
                            visible: !root.showingDiff && (!root.available || !root.workspaceBackend.filePath)
                        }
                    }
                }
            }
            }
            Rectangle {
                objectName: "gitActionsPanel"
                SplitView.preferredHeight: 190
                SplitView.minimumHeight: 140
                SplitView.preferredWidth: 360
                SplitView.minimumWidth: 300
                SplitView.maximumWidth: root.expanded ? 480 : Infinity
                color: root.surface
                GridLayout {
                    columns: 1
                    rowSpacing: 0
                    columnSpacing: 0
                    anchors.fill: parent
                    ColumnLayout {
                        Layout.fillWidth: true
                        Layout.fillHeight: true
                        Layout.minimumWidth: 120
                        spacing: 0
                        RowLayout {
                            id: gitTabs
                            objectName: "gitTabs"
                            visible: root.expanded
                            Layout.fillWidth: true
                            Layout.minimumHeight: 30
                            Layout.preferredHeight: 30
                            Layout.maximumHeight: 30
                            spacing: 0
                            SiteButton {
                                Layout.fillWidth: true
                                Layout.preferredWidth: 1
                                borderColor: "transparent"
                                accentBorder: "transparent"
                                hoverBorderColor: "transparent"
                                text: "Changes (" + root.changes.length + ")"
                                accented: !root.historyTab
                                cornerRadius: 0
                                onClicked: root.historyTab = false
                            }
                            Rectangle { implicitHeight: 30; implicitWidth: 1; color: "#242426" }
                            SiteButton {
                                Layout.fillWidth: true
                                Layout.preferredWidth: 1
                                borderColor: "transparent"
                                accentBorder: "transparent"
                                hoverBorderColor: "transparent"
                                text: "History"
                                accented: root.historyTab
                                cornerRadius: 0
                                onClicked: root.historyTab = true
                            }
                        }
                        Rectangle { visible: root.expanded; Layout.fillWidth: true; implicitHeight: 1; color: "#242426" }
                        ColumnLayout {
                            visible: root.expanded && !root.historyTab
                            Layout.fillWidth: true
                            Layout.fillHeight: true
                            spacing: 4
                            RowLayout {
                                Layout.fillWidth: true
                                Layout.topMargin: 7
                                Layout.bottomMargin: 3
                                Layout.rightMargin: 7
                                SiteButton {
                                    text: "View Diff"
                                    implicitHeight: 24
                                    borderColor: "transparent"
                                    onClicked: root.showDiff("")
                                }
                                Label { text: "+" + root.additions; color: "#00a682"; font.pixelSize: 11 }
                                Label { text: "−" + root.deletions; color: "#cf4f2a"; font.pixelSize: 11 }
                                Item { Layout.fillWidth: true }
                                SiteSplitButton {
                                    text: "Stage All"
                                    enabled: root.available && !root.workspaceBackend.gitBusy
                                    onClicked: root.workspaceBackend.stageAll(true)
                                    onMenuRequested: changeMenu.popup()
                                    SiteMenu {
                                        id: changeMenu
                                        SiteMenuItem { text: "Stage All"; onTriggered: root.workspaceBackend.stageAll(true) }
                                        SiteMenuItem { text: "Unstage All"; onTriggered: root.workspaceBackend.stageAll(false) }
                                        SiteMenuItem { text: "Restore All Changes"; onTriggered: root.confirmAction("discardTracked", "Discard all tracked changes, including staged changes? This cannot be undone.") }
                                        SiteMenuSeparator {}
                                        SiteMenuItem { text: "Discard Tracked Changes"; onTriggered: root.confirmAction("discardTracked", "Discard all tracked changes, including staged changes? This cannot be undone.") }
                                        SiteMenuItem { text: "Trash Untracked Files"; onTriggered: root.confirmAction("trashUntracked", "Move all untracked files to the system trash?") }
                                    }
                                }
                            }
                            ListView {
                                id: changedList
                                Layout.fillWidth: true
                                Layout.fillHeight: true
                                clip: true
                                model: root.changes
                                section.property: "untracked"
                                section.criteria: ViewSection.FullString
                                section.delegate: RowLayout {
                                    id: categoryRow
                                    required property string section
                                    readonly property bool untracked: section === "true"
                                    readonly property var files: root.changes.filter(file => !!file.untracked === untracked)
                                    width: changedList.width
                                    height: 28
                                    spacing: 0
                                    SiteButton {
                                        id: categoryButton
                                        Layout.fillWidth: true
                                        Layout.fillHeight: true
                                        text: categoryRow.untracked ? "Untracked" : "Tracked"
                                        glyph: (categoryRow.untracked ? root.untrackedExpanded : root.trackedExpanded) ? "chevron-down" : "chevron-right"
                                        contentItem: Row {
                                            spacing: 6
                                            SiteIcon { glyph: categoryButton.glyph; width: 14; height: 14; anchors.verticalCenter: parent.verticalCenter; tintColor: "#999999" }
                                            Text { text: categoryButton.text; font: categoryButton.font; color: "#999999"; anchors.verticalCenter: parent.verticalCenter }
                                        }
                                        leftPadding: 12
                                        cornerRadius: 0
                                        borderColor: "transparent"
                                        hoverBorderColor: "transparent"
                                        accentBorder: "transparent"
                                        onClicked: {
                                            if (categoryRow.untracked) root.untrackedExpanded = !root.untrackedExpanded
                                            else root.trackedExpanded = !root.trackedExpanded
                                        }
                                    }
                                    SiteCheckBox {
                                        visible: !categoryRow.untracked
                                        Layout.rightMargin: 8
                                        checked: categoryRow.files.length > 0 && categoryRow.files.every(file => file.staged)
                                        enabled: root.available && !root.workspaceBackend.gitBusy && categoryRow.files.length > 0
                                        Accessible.name: "Stage all tracked files"
                                        onClicked: root.workspaceBackend.gitAction(checked ? "stageTracked" : "unstageTracked")
                                    }
                                }
                                ScrollBar.vertical: ScrollBar {}
                                delegate: ItemDelegate {
                                    id: changedRow
                                    required property var modelData
                                    readonly property bool shown: modelData.untracked ? root.untrackedExpanded : root.trackedExpanded
                                    width: changedList.width
                                    height: shown ? 32 : 0
                                    visible: shown
                                    padding: 4
                                    leftPadding: 12
                                    rightPadding: 8
                                    onClicked: root.showDiff(modelData.path)
                                    ContextMenu.menu: SiteMenu {
                                        SiteMenuItem {
                                            text: "Stage File"
                                            enabled: root.available && !root.workspaceBackend.gitBusy && !changedRow.modelData.staged
                                            onTriggered: root.workspaceBackend.stageFile(changedRow.modelData.path, true)
                                        }
                                        SiteMenuItem {
                                            text: "Unstage File"
                                            enabled: root.available && !root.workspaceBackend.gitBusy && changedRow.modelData.staged
                                            onTriggered: root.workspaceBackend.stageFile(changedRow.modelData.path, false)
                                        }
                                    }
                                    contentItem: RowLayout {
                                        spacing: 6
                                        FileIcon { fileName: changedRow.modelData.path; Layout.preferredWidth: 14; Layout.preferredHeight: 14; Layout.alignment: Qt.AlignVCenter }
                                        Label {
                                            Layout.fillWidth: true
                                            text: changedRow.modelData.path
                                            textFormat: Text.PlainText
                                            elide: Text.ElideMiddle
                                            font.pixelSize: 12
                                            Layout.alignment: Qt.AlignVCenter
                                            verticalAlignment: Text.AlignVCenter
                                            color: changedRow.modelData.untracked || changedRow.modelData.status.includes("A") ? "#00a682" : "#ab910d"
                                        }
                                        Label { visible: !changedRow.modelData.untracked; text: changedRow.modelData.additions == null ? "" : "+" + changedRow.modelData.additions; color: "#00a682"; font.pixelSize: 10; Layout.alignment: Qt.AlignVCenter }
                                        Label { visible: !changedRow.modelData.untracked; text: changedRow.modelData.deletions == null ? "" : "−" + changedRow.modelData.deletions; color: "#cf4f2a"; font.pixelSize: 10; Layout.alignment: Qt.AlignVCenter }
                                        SiteCheckBox {
                                            Layout.alignment: Qt.AlignVCenter
                                            checked: changedRow.modelData.staged
                                            enabled: root.available && !root.workspaceBackend.gitBusy
                                            Accessible.name: "Stage " + changedRow.modelData.path
                                            onClicked: root.workspaceBackend.stageFile(changedRow.modelData.path, checked)
                                        }
                                    }
                                    background: Rectangle { color: changedRow.hovered ? root.raised : "transparent" }
                                    SiteToolTip { text: changedRow.modelData.path + " · " + changedRow.modelData.status }
                                }
                                Label { anchors.centerIn: parent; visible: changedList.count === 0; text: "Working tree clean"; color: "#898991"; font.pixelSize: 12 }
                            }
                        }
                        TextField {
                            id: commitSearch
                            visible: !root.expanded || root.historyTab
                            Layout.fillWidth: true
                            implicitHeight: 28
                            placeholderText: "Search commits…"
                            Accessible.name: "Search commits"
                            font.pixelSize: 11
                            color: "#eeeeee"
                            placeholderTextColor: "#898991"
                            background: Rectangle {
                                color: root.raised
                                Rectangle {
                                    anchors.left: parent.left
                                    anchors.right: parent.right
                                    anchors.bottom: parent.bottom
                                    height: 1
                                    color: "#242426"
                                }
                            }
                        }
                        ListView {
                            id: history
                            visible: !root.expanded || root.historyTab
                            objectName: "gitCommitHistory"
                            Layout.fillWidth: true
                            Layout.fillHeight: true
                            clip: true
                            model: root.available ? root.workspaceBackend.commits : []
                            ScrollBar.vertical: ScrollBar {}
                            delegate: ItemDelegate {
                                id: commitRow
                                required property var modelData
                                readonly property bool matches: !commitSearch.text || (modelData.subject + " " + modelData.author + " " + modelData.hash + " " + modelData.refs).toLowerCase().includes(commitSearch.text.toLowerCase())
                                width: history.width
                                height: matches ? 28 : 0
                                visible: matches
                                padding: 0
                                Accessible.name: modelData.subject + ", " + modelData.author
                                contentItem: RowLayout {
                                    spacing: 6
                                    Item {
                                        Layout.preferredWidth: 18
                                        Layout.fillHeight: true
                                        Rectangle { x: 8; width: 1; height: parent.height; color: root.accent; opacity: 0.4 }
                                        Rectangle { x: 6; anchors.verticalCenter: parent.verticalCenter; width: 5; height: 5; radius: 3; color: root.accent }
                                    }
                                    Label {
                                        Layout.fillWidth: true
                                        text: commitRow.modelData.subject
                                        textFormat: Text.PlainText
                                        font.pixelSize: 12
                                        color: "#d0d0d5"
                                        elide: Text.ElideRight
                                    }
                                    Label {
                                        text: commitRow.modelData.author
                                        textFormat: Text.PlainText
                                        Layout.maximumWidth: history.width * 0.35
                                        elide: Text.ElideRight
                                        font.pixelSize: 11
                                        color: "#898991"
                                        rightPadding: 6
                                    }
                                }
                                background: Rectangle { color: commitRow.hovered ? root.raised : "transparent" }
                                SiteToolTip { text: commitRow.modelData.subject + "\n" + commitRow.modelData.author + " · " + commitRow.modelData.date + "\n" + commitRow.modelData.hash + (commitRow.modelData.refs ? "\n" + commitRow.modelData.refs : "") }
                            }
                            Label {
                                anchors.centerIn: parent
                                visible: history.count === 0
                                text: "No commits yet"
                                color: "#898991"
                                font.pixelSize: 11
                            }
                        }
                    }
                    ColumnLayout {
                        visible: root.expanded
                        Layout.preferredWidth: root.expanded ? -1 : 250
                        Layout.maximumWidth: root.expanded ? Infinity : 300
                        Layout.fillWidth: root.expanded
                        Layout.preferredHeight: root.expanded ? 210 : -1
                        Layout.fillHeight: !root.expanded
                        Layout.topMargin: 7
                        Layout.bottomMargin: 7
                        Layout.leftMargin: root.expanded ? 0 : 7
                        Layout.rightMargin: root.expanded ? 0 : 7
                        spacing: 5
                        RowLayout {
                            Layout.fillWidth: true
                            Layout.leftMargin: root.expanded ? 7 : 0
                            Layout.rightMargin: root.expanded ? 7 : 0
                            Label {
                                Layout.fillWidth: true
                                text: root.available ? root.workspaceBackend.branch || "Detached HEAD" : "Git"
                                textFormat: Text.PlainText
                                color: root.accent
                                font.pixelSize: 11
                                elide: Text.ElideRight
                            }
                            Rectangle {
                                implicitWidth: fetchActions.implicitWidth + 2
                                implicitHeight: 24
                                radius: 4
                                color: root.surface
                                border.color: "#242426"
                                Row {
                                    id: fetchActions
                                    anchors.centerIn: parent
                                    spacing: 0
                                    SiteButton {
                                        readonly property int aheadCount: root.available ? root.workspaceBackend.aheadCount : 0
                                        text: aheadCount > 0 ? aheadCount + " Push" : "Fetch"
                                        glyph: aheadCount > 0 ? "arrow-up" : "rotate-cw"
                                                                                glyphSpacing: aheadCount > 0 ? 2 : 6
                                        SiteToolTip { text: parent.aheadCount > 0 ? "Push local commits" : "Fetch" }
                                        implicitHeight: 22
                                        cornerRadius: 3
                                        borderColor: "transparent"
                                        hoverBorderColor: "transparent"
                                        surfaceColor: "transparent"
                                        leftPadding: aheadCount > 0 ? 4 : 7
                                        rightPadding: aheadCount > 0 ? 4 : 7
                                        enabled: root.available && !root.workspaceBackend.gitBusy
                                        onClicked: root.workspaceBackend.gitAction(aheadCount > 0 ? "push" : "fetch")
                                    }
                                    Rectangle { width: 1; height: 22; color: "#242426" }
                                    SiteButton {
                                        text: "Git actions"
                                        glyph: "chevron-down"
                                        iconOnly: true
                                        SiteToolTip { text: "Pull / Push" }
                                        implicitHeight: 22
                                        cornerRadius: 3
                                        borderColor: "transparent"
                                        hoverBorderColor: "transparent"
                                        surfaceColor: "transparent"
                                        enabled: root.available && !root.workspaceBackend.gitBusy
                                        onClicked: syncMenu.popup()
                                        SiteMenu {
                                            id: syncMenu
                                            SiteMenuItem { text: "Pull (fast-forward only)"; onTriggered: root.workspaceBackend.gitAction("pull") }
                                            SiteMenuItem { text: "Push"; onTriggered: root.workspaceBackend.gitAction("push") }
                                            SiteMenuItem { text: "Refresh history"; onTriggered: root.workspaceBackend.refreshGit() }
                                        }
                                    }
                                }
                            }
                        }
                        Rectangle {
                            Layout.fillWidth: true
                            Layout.fillHeight: true
                            Layout.minimumHeight: 70
                            color: "#08080a"
                            radius: root.expanded ? 0 : 4
                            border.color: root.expanded ? "transparent" : "#242426"
                            Rectangle { visible: root.expanded; anchors.top: parent.top; width: parent.width; height: 1; color: "#242426" }
                            Rectangle { visible: root.expanded; anchors.bottom: parent.bottom; width: parent.width; height: 1; color: "#242426" }
                            ScrollView {
                                anchors.fill: parent
                                anchors.margins: 1
                                anchors.bottomMargin: commitControls.height + 10
                                clip: true
                                TextArea {
                                    id: commitMessage
                                    objectName: "gitCommitMessage"
                                    placeholderText: "Enter commit message"
                                    Accessible.name: "Commit message"
                                    color: "#eeeeee"
                                    placeholderTextColor: "#898991"
                                    font.pixelSize: 12
                                    wrapMode: TextEdit.Wrap
                                    background: null
                                }
                            }
                            SiteSplitButton {
                                id: commitControls
                                anchors.right: parent.right
                                anchors.bottom: parent.bottom
                                anchors.margins: 6
                                text: root.amendCommit ? "Amend Commit" : root.commitStaged && root.expanded ? "Commit Staged" : "Commit Tracked"
                                accented: true
                                enabled: root.available && !root.workspaceBackend.gitBusy
                                onClicked: root.submitCommit()
                                onMenuRequested: commitOptions.popup()
                                SiteMenu {
                                    id: commitOptions
                                    SiteMenuItem { text: "Amend"; checkable: true; checked: root.amendCommit; glyph: checked ? "check" : ""; onTriggered: root.amendCommit = checked }
                                    SiteMenuItem { text: "Signoff"; checkable: true; checked: root.signoffCommit; glyph: checked ? "check" : ""; onTriggered: root.signoffCommit = checked }
                                    SiteMenuItem { text: "Skip Hooks"; checkable: true; checked: root.skipCommitHooks; glyph: checked ? "check" : ""; onTriggered: root.skipCommitHooks = checked }
                                }
                            }
                        }
                        RowLayout {
                            visible: root.expanded
                            Layout.fillWidth: true
                            Layout.leftMargin: 7
                            Layout.rightMargin: 7
                            Label {
                                Layout.fillWidth: true
                                text: root.available && root.workspaceBackend.commits.length ? root.workspaceBackend.commits[0].subject : "No commits yet"
                                textFormat: Text.PlainText
                                color: "#898991"
                                font.pixelSize: 11
                                elide: Text.ElideRight
                            }

                            SiteButton {
                                text: "Open system terminal"
                                glyph: "terminal"
                                cornerRadius: 4
                                iconOnly: true
                                implicitHeight: 24
                                enabled: root.available && !!root.workspaceBackend.rootPath
                                SiteToolTip { text: "Open system terminal in repository" }
                                onClicked: root.workspaceBackend.openSystemTerminal()
                            }
                        }
                        Connections {
                            target: root.workspaceBackend
                            function onCommitSucceeded() { commitMessage.clear(); root.amendCommit = false }
                        }
                    }
                }
            }
        }
    }
    SiteDialog {
        id: stashDialog
        title: "Stashes"
        width: Math.min(600, root.width - 24)
        height: Math.min(400, root.height - 24)
        anchors.centerIn: parent
        onCloseRequested: close()
        contentItem: ListView {
            clip: true
            model: root.available ? root.workspaceBackend.stashes : []
            delegate: Label {
                required property var modelData
                width: ListView.view.width
                text: modelData.ref + " · " + modelData.subject
                textFormat: Text.PlainText
                wrapMode: Text.Wrap
                padding: 8
                color: "#eeeeee"
            }
            Label { anchors.centerIn: parent; visible: root.available && !root.workspaceBackend.stashes.length; text: "No stashes"; color: "#898991" }
        }
    }
    SiteDialog {
        id: confirmation
        property string description: ""
        title: "Confirm Git operation"
        width: Math.min(440, root.width - 24)
        anchors.centerIn: parent
        onCloseRequested: close()
        contentItem: ColumnLayout {
            spacing: 16
            Label { Layout.fillWidth: true; text: confirmation.description; wrapMode: Text.Wrap; color: "#eeeeee" }
            RowLayout {
                Layout.alignment: Qt.AlignRight
                SiteButton { text: "Cancel"; onClicked: confirmation.close() }
                SiteButton {
                    text: "Continue"
                    accented: true
                    onClicked: { root.workspaceBackend.gitAction(root.pendingAction); confirmation.close() }
                }
            }
        }
    }
    SiteDialog {
        id: unsaved
        anchors.centerIn: parent
        width: Math.min(440, parent ? parent.width - 32 : 440)
        modal: true
        closePolicy: Popup.NoAutoClose
        title: "Unsaved changes"
        icon: "qrc:/assets/lucide-file.svg"
        onCloseRequested: root.resolve("cancel")
        contentItem: ColumnLayout {
            spacing: 16
            SiteQuote {
                            text: "Save changes before continuing?"
                            Layout.fillWidth: true
                        }
            Label {
                textFormat: Text.PlainText
                text: root.available ? root.workspaceBackend.error : ""
                visible: !!text
                color: "#ff8585"
                wrapMode: Text.Wrap
                Layout.fillWidth: true
            }
            RowLayout {
                Layout.fillWidth: true
                spacing: 8
                Item { Layout.fillWidth: true }
                SiteButton { text: "Save"; accented: true; enabled: !root.fileLoading && !editor.loadingDocument; onClicked: root.resolve("save") }
                SiteButton { text: "Discard"; onClicked: root.resolve("discard") }
                SiteButton { text: "Cancel"; onClicked: root.resolve("cancel") }
            }
        }
    }
}

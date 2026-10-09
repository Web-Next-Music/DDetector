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
    onWorkspaceBackendChanged: editor.syncDocument()
    readonly property bool available: workspaceBackend !== null && workspaceBackend !== undefined
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
              ? "Set a Git repository path in Settings to open the workspace."
              : "Repository unavailable. Check the path in Settings and ensure Git is installed."
        color: "#eeeeee"
        horizontalAlignment: Text.AlignHCenter
        wrapMode: Text.Wrap
    }
    Shortcut {
        sequence: StandardKey.Save
        enabled: root.visible && root.available && !!root.workspaceBackend.filePath
                 && !(terminalLoader.item && terminalLoader.item.terminalHasFocus)
        onActivated: root.workspaceBackend.save()
    }

    ColumnLayout {
        anchors.fill: parent
        anchors.margins: 0
        spacing: 0

        SplitView {
            Layout.fillWidth: true
            Layout.fillHeight: true
            orientation: Qt.Vertical
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
                color: "#3397979f"
            }
            SplitView {
                SplitView.fillHeight: true
                SplitView.minimumHeight: 80
                orientation: Qt.Horizontal
                handle: Rectangle {
                    implicitWidth: 1
                    color: "#3397979f"
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
                                height: 28
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
                                onClicked: {
                                    if (modelData.directory) root.workspaceBackend.toggleDirectory(modelData.path)
                                    else root.workspaceBackend.openFile(modelData.path)
                                }
                            }
                        }
                    }
                }
                Rectangle {
                    SplitView.fillWidth: true
                    SplitView.minimumWidth: 180
                    color: root.surface
                    radius: 0
                    ColumnLayout {
                        anchors.fill: parent
                        anchors.margins: 8
                        spacing: 6
                        RowLayout {
                            id: breadcrumbRow
                            Layout.fillWidth: true
                            Layout.margins: 8
                            spacing: 8
                            // Path is a breadcrumb, not a destructive navigation operation.
                            Label {
                                textFormat: Text.PlainText
                                Layout.minimumWidth: 0
                                Layout.maximumWidth: Math.max(0, breadcrumbRow.width - saveButton.implicitWidth - (dirtyIndicator.visible ? 6 : 0) - 24)
                                text: root.available ? root.workspaceBackend.rootPath.split("/").filter(function(part) { return !!part }).pop() +
                                      (root.workspaceBackend.filePath ? "  ›  " + root.workspaceBackend.filePath.split("/").join("  ›  ") : "") : ""
                                color: root.accent
                                elide: Text.ElideMiddle
                            }
                            Rectangle {
                                id: dirtyIndicator
                                Layout.preferredWidth: 6
                                Layout.preferredHeight: 6
                                radius: 3
                                color: root.accent
                                visible: root.available && root.workspaceBackend.dirty
                            }
                            Item { Layout.fillWidth: true }
                            SiteButton {
                                id: saveButton
                                implicitWidth: implicitHeight
                                leftPadding: 8
                                rightPadding: 8
                                text: "Save"
                                glyph: "save"
                                iconOnly: true
                                SiteToolTip { text: "Save (Ctrl+S)" }
                                enabled: root.available && !!root.workspaceBackend.filePath && root.workspaceBackend.dirty
                                onClicked: root.workspaceBackend.save()
                            }
                        }
                        ScrollView {
                            id: editorScroll
                            Layout.fillWidth: true
                            Layout.fillHeight: true
                            padding: 4
                            leftPadding: lineNumbers.visible ? lineNumbers.width + 4 : 4
                            FontMetrics { id: editorMetrics; font: editor.font }
                            Item {
                                id: lineNumbers
                                parent: editorScroll
                                z: 1
                                objectName: "editorLineNumbers"
                                visible: editor.enabled
                                x: 4
                                y: editorScroll.topPadding
                                width: Math.max(32, editorMetrics.averageCharacterWidth * String(editor.lineCount).length + 16)
                                height: editorScroll.availableHeight
                                clip: true
                                readonly property real scrollY: editorScroll.contentItem ? editorScroll.contentItem.contentY : 0
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
                                radius: 8
                                border.color: "#3397979f"
                            }
                            TextArea {
                                id: editor
                                objectName: "workspaceEditor"
                                Accessible.name: "File editor: " + (root.available ? root.workspaceBackend.filePath : "")
                                enabled: root.available && !!root.workspaceBackend.filePath
                                textFormat: TextEdit.PlainText
                                SyntaxHighlighter {
                                    document: editor.textDocument
                                    filePath: root.available ? root.workspaceBackend.filePath : ""
                                }
                                property bool loadingDocument: false
                                property var loadedBackend: null
                                property int loadedRevision: -1
                                function syncDocument() {
                                    loadingDocument = true
                                    loadedBackend = null
                                    loadedRevision = -1
                                    const backend = root.available ? root.workspaceBackend : null
                                    const value = backend ? backend.text : ""
                                    if (text !== value) text = value
                                    // Establish identity only after the buffer has been installed.
                                    loadedRevision = backend ? backend.documentRevision : -1
                                    loadedBackend = backend
                                    loadingDocument = false
                                }
                                Component.onCompleted: syncDocument()
                                Connections {
                                    target: root.workspaceBackend
                                    function onDocumentAboutToChange() {
                                        editor.loadingDocument = true
                                        editor.loadedRevision = -1
                                    }
                                    function onDocumentChanged() { editor.syncDocument() }
                                }
                                onTextChanged: {
                                    if (!loadingDocument && root.available
                                            && loadedBackend === root.workspaceBackend
                                            && loadedRevision === root.workspaceBackend.documentRevision
                                            && text !== root.workspaceBackend.text)
                                        root.workspaceBackend.editDocument(loadedRevision, text)
                                }
                                placeholderText: "Choose a UTF-8 text file"
                                color: "#eeeeee"
                                selectionColor: root.accent
                                selectedTextColor: root.surface
                                placeholderTextColor: "#898991"
                                font.family: "monospace"
                                font.pixelSize: 14
                                readonly property real lineHeight: Math.max(1, positionToRectangle(0).height)
                                wrapMode: TextEdit.NoWrap
                                selectByMouse: true
                                ContextMenu.menu: SiteEditMenu {
                                    objectName: "editorContextMenu"
                                    textControl: editor
                                }
                                background: null
                            }
                        }
                    }
                }
            }
            Loader {
                id: terminalLoader
                SplitView.preferredHeight: 230
                SplitView.minimumHeight: 120
                source: "terminal.qml"
                onLoaded: item.workspaceBackend = Qt.binding(function() { return root.workspaceBackend })
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
            Label { textFormat: Text.PlainText; text: "Save changes before continuing?"; color: "#eeeeee"; wrapMode: Text.Wrap; Layout.fillWidth: true }
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
                SiteButton { text: "Save"; accented: true; onClicked: root.resolve("save") }
                SiteButton { text: "Discard"; onClicked: root.resolve("discard") }
                SiteButton { text: "Cancel"; onClicked: root.resolve("cancel") }
            }
        }
    }
}

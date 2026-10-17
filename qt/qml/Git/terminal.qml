pragma ComponentBehavior: Bound
// Requires QApplication, DDetector.Terminal registration, and QTermWidget data.
// Unix PTY backend only: Windows ConPTY is not implemented.
import QtQuick
import QtQuick.Controls
import QtQuick.Layouts

import DDetector.Terminal 1.0

Rectangle {
    id: root
    property var workspaceBackend: null
    readonly property bool terminalHasFocus: terminal.activeFocus
    readonly property var terminalSession: workspaceBackend ? workspaceBackend.terminalSession : null
    readonly property string repositoryRoot: workspaceBackend ? workspaceBackend.rootPath : ""
    readonly property bool sessionVisible: visible && width > 0 && height > 0
                                          && Window.window !== null && Window.window.visible

    readonly property color accent: "#2bfef5"
    implicitWidth: 320
    implicitHeight: 230
    radius: 0
    color: "#0c0c0e"

    // One automatic attempt per repository/backend. Errors and shell exit never
    // cause a notification-driven retry; an explicit Restart remains available.
    property bool started: false
    function startIfReady() {
        if (!started && sessionVisible && workspaceBackend && repositoryRoot) {
            started = true // Set before synchronous backend notifications, including errors.
            workspaceBackend.startTerminal(true)
        }
    }
    onWorkspaceBackendChanged: { started = false; startIfReady() }
    onRepositoryRootChanged: { started = false; startIfReady() }
    onSessionVisibleChanged: startIfReady()
    Component.onCompleted: startIfReady()

    component TerminalButton: SiteButton {
        id: control
        implicitWidth: 32
        implicitHeight: 32
        iconOnly: true
        glyphSize: 16
        property string tooltipText: text
        SiteToolTip { text: control.tooltipText }
        font.family: "DDetector Quicksand"
        font.weight: Font.DemiBold
        textColor: "#f0f0f4"
        hoverTextColor: root.accent
        hoverColor: down ? "#242b2b" : "#142bfef5"
        accentedColor: hovered ? hoverColor : surfaceColor
    }

    ColumnLayout {
        anchors.fill: parent
        anchors.margins: 8
        spacing: 6
        RowLayout {
            Layout.fillWidth: true
            spacing: 4
            Label {
                textFormat: Text.PlainText
                Layout.fillWidth: true
                Layout.minimumWidth: 0
                text: root.terminalSession && root.terminalSession.shell
                      ? "TERMINAL · " + root.terminalSession.shell : "TERMINAL"
                elide: Text.ElideMiddle
                color: root.accent
                font.family: "DDetector Quicksand"
                font.pixelSize: 12
                font.weight: Font.DemiBold
            }
            TerminalButton {
                text: root.terminalSession && root.terminalSession.running ? "Stop" : "Restart"
                glyph: root.terminalSession && root.terminalSession.running ? "square" : "rotate-cw"
                accented: true
                enabled: root.sessionVisible && !!root.terminalSession && !!root.repositoryRoot
                onClicked: {
                    root.started = true
                    if (root.terminalSession.running) root.workspaceBackend.stopTerminal()
                    else root.workspaceBackend.startTerminal()
                    terminal.forceActiveFocus()
                }
            }
            TerminalButton {
                text: "Copy"
                glyph: "copy"
                enabled: !!root.terminalSession
                onClicked: terminal.copy()
                tooltipText: "Copy selection (Ctrl+Shift+C)"
            }
            TerminalButton {
                text: "Paste"
                glyph: "clipboard"
                enabled: !!root.terminalSession && root.terminalSession.running
                onClicked: { terminal.paste(); terminal.forceActiveFocus() }
                tooltipText: "Paste into shell (Ctrl+Shift+V); commands are not sandboxed"
            }
            TerminalButton {
                text: "Clear"
                glyph: "eraser"
                enabled: !!root.workspaceBackend
                onClicked: root.workspaceBackend.clearTerminal()
            }
        }
        Label {
            textFormat: Text.PlainText
            id: terminalError
            Layout.fillWidth: true
            visible: !!root.terminalSession && !!root.terminalSession.error
            text: root.terminalSession ? root.terminalSession.error : ""
            color: "#ff8585"
            font.pixelSize: 12
            elide: Text.ElideRight
            SiteToolTip {
                visible: errorHover.hovered && parent.visible && text.length > 0
                text: terminalError.text
            }
            HoverHandler { id: errorHover }
        }
        Rectangle {
            Layout.fillWidth: true
            Layout.fillHeight: true
            Layout.minimumHeight: 24
            implicitHeight: 156
            radius: 8
            color: "#000000"
            border.color: "#242426"
            EmbeddedTerminal {
                id: terminal
                anchors.fill: parent
                // Keep the rectangular terminal texture inside the rounded frame.
                anchors.margins: 4
                session: root.terminalSession
                focus: true
            }
        }
    }
}

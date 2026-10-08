// Requires QApplication, DDetector.Terminal registration, and QTermWidget data.
// Unix PTY backend only: Windows ConPTY is not implemented.
import QtQuick
import QtQuick.Controls
import QtQuick.Layouts
import QtQuick.Effects
import DDetector.Terminal 1.0

Rectangle {
    id: root
    property var workspaceBackend: null
    readonly property bool terminalHasFocus: terminal.activeFocus
    readonly property var terminalSession: workspaceBackend ? workspaceBackend.terminalSession : null
    readonly property string repositoryRoot: workspaceBackend ? workspaceBackend.rootPath : ""
    readonly property bool sessionVisible: visible && width > 0 && height > 0
                                          && Window.window !== null && Window.window.visible
    readonly property bool compact: width < 480
    readonly property real minimumPanelHeight: 120
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
            workspaceBackend.startTerminal()
        }
    }
    onWorkspaceBackendChanged: { started = false; startIfReady() }
    onRepositoryRootChanged: { started = false; startIfReady() }
    onSessionVisibleChanged: startIfReady()
    Component.onCompleted: startIfReady()

    component TerminalButton: Button {
        id: control
        property bool accented: false
        property string glyph: ""
        implicitWidth: 32
        implicitHeight: 32
        Accessible.name: text
        property string tooltipText: text
        SiteToolTip { text: control.tooltipText }
        leftPadding: root.compact ? 6 : 10
        rightPadding: leftPadding
        font.family: "DDetector Quicksand"
        font.pixelSize: root.compact ? 11 : 12
        font.weight: Font.DemiBold
        opacity: enabled ? 1 : 0.5
        contentItem: Item {
            Image {
                anchors.centerIn: parent
                width: 16; height: 16
                source: "qrc:/assets/lucide-" + control.glyph + ".svg"
                layer.enabled: true
                layer.effect: MultiEffect { brightness: 1; colorization: 1; colorizationColor: control.accented || control.hovered ? root.accent : "#f0f0f4" }
            }
        }
        background: Rectangle {
            radius: 8
            color: control.down ? "#242b2b" : control.hovered ? "#142bfef5" : "#0c0c0e"
            border.color: control.activeFocus ? root.accent
                          : control.hovered || control.accented ? "#332bfef5" : "#3397979f"
        }
        HoverHandler { cursorShape: control.enabled ? Qt.PointingHandCursor : Qt.ArrowCursor }
    }

    ColumnLayout {
        anchors.fill: parent
        anchors.margins: 8
        spacing: 6
        RowLayout {
            Layout.fillWidth: true
            spacing: 4
            Label {
                Layout.fillWidth: true
                Layout.minimumWidth: 0
                text: root.compact ? "TTY" : root.terminalSession && root.terminalSession.shell
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
            Layout.fillWidth: true
            visible: !!root.terminalSession && !!root.terminalSession.error
            text: root.terminalSession ? root.terminalSession.error : ""
            color: "#ff8585"
            font.pixelSize: 12
            elide: Text.ElideRight
            SiteToolTip {
                visible: errorHover.hovered && parent.visible && text.length > 0
                text: parent.text
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
            border.color: "#3397979f"
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

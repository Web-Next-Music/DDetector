pragma ComponentBehavior: Bound
import QtQuick


SiteMenu {
    id: editMenu
    required property var textControl
    readonly property bool editable: textControl.enabled && !textControl.readOnly
    readonly property bool hasSelection: textControl.selectedText.length > 0
    readonly property bool canCopy: hasSelection && (textControl.echoMode === undefined || textControl.echoMode === TextInput.Normal)

    width: 200
    SiteMenuItem {
        text: qsTr("Undo"); glyph: "undo-2"
        enabled: editMenu.editable && editMenu.textControl.canUndo
        onTriggered: editMenu.textControl.undo()
    }
    SiteMenuItem {
        text: qsTr("Redo"); glyph: "redo-2"
        enabled: editMenu.editable && editMenu.textControl.canRedo
        onTriggered: editMenu.textControl.redo()
    }
    SiteMenuSeparator {}
    SiteMenuItem {
        text: qsTr("Cut"); glyph: "scissors"
        enabled: editMenu.editable && editMenu.canCopy
        onTriggered: editMenu.textControl.cut()
    }
    SiteMenuItem {
        text: qsTr("Copy"); glyph: "copy"
        enabled: editMenu.canCopy
        onTriggered: editMenu.textControl.copy()
    }
    SiteMenuItem {
        text: qsTr("Paste"); glyph: "clipboard"
        enabled: editMenu.editable && editMenu.textControl.canPaste
        onTriggered: editMenu.textControl.paste()
    }
    SiteMenuItem {
        text: qsTr("Delete"); glyph: "trash-2"
        enabled: editMenu.editable && editMenu.hasSelection
        onTriggered: editMenu.textControl.remove(editMenu.textControl.selectionStart, editMenu.textControl.selectionEnd)
    }
    SiteMenuSeparator {}
    SiteMenuItem {
        text: qsTr("Select All"); glyph: "text-select"
        enabled: editMenu.textControl.length > 0
        onTriggered: editMenu.textControl.selectAll()
    }
}

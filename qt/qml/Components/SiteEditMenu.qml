pragma ComponentBehavior: Bound
import QtQuick
import QtQuick.Controls

SiteMenu {
    id: menu
    required property var textControl
    readonly property bool editable: textControl.enabled && !textControl.readOnly
    readonly property bool hasSelection: textControl.selectedText.length > 0
    readonly property bool canCopy: hasSelection && (textControl.echoMode === undefined || textControl.echoMode === TextInput.Normal)

    width: 200
    SiteMenuItem {
        text: qsTr("Undo"); glyph: "undo-2"
        enabled: menu.editable && menu.textControl.canUndo
        onTriggered: menu.textControl.undo()
    }
    SiteMenuItem {
        text: qsTr("Redo"); glyph: "redo-2"
        enabled: menu.editable && menu.textControl.canRedo
        onTriggered: menu.textControl.redo()
    }
    SiteMenuSeparator {}
    SiteMenuItem {
        text: qsTr("Cut"); glyph: "scissors"
        enabled: menu.editable && menu.canCopy
        onTriggered: menu.textControl.cut()
    }
    SiteMenuItem {
        text: qsTr("Copy"); glyph: "copy"
        enabled: menu.canCopy
        onTriggered: menu.textControl.copy()
    }
    SiteMenuItem {
        text: qsTr("Paste"); glyph: "clipboard"
        enabled: menu.editable && menu.textControl.canPaste
        onTriggered: menu.textControl.paste()
    }
    SiteMenuItem {
        text: qsTr("Delete"); glyph: "trash-2"
        enabled: menu.editable && menu.hasSelection
        onTriggered: menu.textControl.remove(menu.textControl.selectionStart, menu.textControl.selectionEnd)
    }
    SiteMenuSeparator {}
    SiteMenuItem {
        text: qsTr("Select All"); glyph: "text-select"
        enabled: menu.textControl.length > 0
        onTriggered: menu.textControl.selectAll()
    }
}

pragma ComponentBehavior: Bound
import QtQuick
import QtQuick.Controls

import QtQuick.Layouts
import QtQuick.Effects

ComboBox {
    id: control
    property string glyph: ""
    property var optionReason: null
    implicitWidth: 180
    implicitHeight: 30
    leftPadding: 10
    rightPadding: 10
    spacing: 6
    font.family: "DDetector Nunito"
    font.pixelSize: 12
    font.weight: Font.Bold
    opacity: enabled ? 1 : 0.45
    Accessible.name: displayText
    HoverHandler { cursorShape: control.enabled ? Qt.PointingHandCursor : Qt.ArrowCursor }
    indicator: null
    contentItem: RowLayout {
        spacing: 6
        Image {
            visible: !!control.glyph
            Layout.preferredWidth: 14
            Layout.preferredHeight: 14
            source: control.glyph ? "qrc:/assets/lucide-" + control.glyph + ".svg" : ""
            layer.enabled: true
            layer.effect: MultiEffect {
                brightness: 1
                colorization: 1
                colorizationColor: control.hovered || control.popup.visible ? "#2bfef5" : "#999999"
            }
            Accessible.ignored: true
        }
        Text {
            Layout.fillWidth: true
            text: control.displayText
            textFormat: Text.PlainText
            font: control.font
            color: control.hovered || control.popup.visible ? "#2bfef5" : "#999999"
            elide: Text.ElideRight
            verticalAlignment: Text.AlignVCenter
        }
        Image {
            Layout.preferredWidth: 14
            Layout.preferredHeight: 14
            source: "qrc:/assets/lucide-chevron-down.svg"
            rotation: control.popup.visible ? 180 : 0
            layer.enabled: true
            layer.effect: MultiEffect {
                brightness: 1
                colorization: 1
                colorizationColor: control.hovered || control.popup.visible ? "#2bfef5" : "#999999"
            }
            Accessible.ignored: true
        }
    }
    background: Rectangle {
        radius: 8
        color: control.hovered || control.down || control.popup.visible ? "#142bfef5" : "#0c0c0e"
        border.color: control.visualFocus || control.hovered || control.popup.visible ? "#332bfef5" : "#3397979f"
    }
    SiteFocusRing { active: control.visualFocus }
    delegate: SiteMenuItem {
        id: option
        required property int index
        readonly property var disabledReason: typeof control.optionReason === "function"
            ? control.optionReason(control.valueAt(index)) : null
        width: optionList.width
        text: control.textAt(index)
        glyph: index === control.currentIndex ? "check" : ""
        enabled: !disabledReason
        highlighted: control.highlightedIndex === index
        textColor: index === control.currentIndex ? accentColor : "#f0f0f4"
        Accessible.description: disabledReason ? String(disabledReason) : ""
        ToolTip.visible: hovered && !!disabledReason
        ToolTip.text: disabledReason ? String(disabledReason) : ""
    }
    popup: Popup {
        y: control.height + 6
        width: control.width
        padding: 8
        popupType: Popup.Item
        implicitHeight: Math.min(optionList.contentHeight + topPadding + bottomPadding,
            280, control.Overlay.overlay ? Math.max(0, control.Overlay.overlay.height - 16) : 280)
        margins: 8
        contentItem: ListView {
            id: optionList
            clip: true
            implicitHeight: contentHeight
            model: control.popup.visible ? control.delegateModel : null
            currentIndex: control.highlightedIndex
            highlightMoveDuration: 0
            boundsBehavior: Flickable.StopAtBounds
            ScrollBar.vertical: ScrollBar {}
        }
        background: Rectangle {
            radius: 10
            color: "#171719"
            border.color: "#3397979f"
        }
    }
}

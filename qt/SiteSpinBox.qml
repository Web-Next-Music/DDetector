pragma ComponentBehavior: Bound
import QtQuick
import QtQuick.Controls

SpinBox {
    id: control
    implicitWidth: 120
    implicitHeight: 36
    editable: true
    leftPadding: 12
    rightPadding: 38
    topPadding: 0
    bottomPadding: 0
    font.family: "DDetector Nunito"
    font.pixelSize: 14
    opacity: enabled ? 1 : 0.55

    contentItem: TextInput {
        text: control.displayText
        font: control.font
        color: "#f0f0f4"
        selectionColor: "#142bfef5"
        selectedTextColor: "#f0f0f4"
        verticalAlignment: TextInput.AlignVCenter
        readOnly: !control.editable
        selectByMouse: true
        validator: control.validator
        inputMethodHints: control.inputMethodHints
        ContextMenu.menu: SiteEditMenu { textControl: control.contentItem }
    }
    background: Rectangle {
        radius: 8
        color: "#0c0c0e"
        border.color: control.activeFocus ? "#332bfef5" : "#3397979f"
    }
    up.indicator: SiteButton {
        x: control.width - width - 4
        y: 3
        width: 28
        height: (control.height - 6) / 2
        implicitWidth: 28
        implicitHeight: 15
        glyph: "chevron-up"
        iconOnly: true
        cornerRadius: 4
        leftPadding: 0
        rightPadding: 0
        enabled: control.enabled && (control.wrap || (control.from <= control.to ? control.value < control.to : control.value > control.to))
        focusPolicy: Qt.NoFocus
        autoRepeat: true
        Accessible.name: qsTr("Increase value")
        onClicked: {
            const previous = control.value;
            control.increase();
            if (control.value !== previous)
                control.valueModified();
        }
    }
    down.indicator: SiteButton {
        x: control.width - width - 4
        y: control.height / 2
        width: 28
        height: (control.height - 6) / 2
        implicitWidth: 28
        implicitHeight: 15
        glyph: "chevron-down"
        iconOnly: true
        cornerRadius: 4
        leftPadding: 0
        rightPadding: 0
        enabled: control.enabled && (control.wrap || (control.from <= control.to ? control.value > control.from : control.value < control.from))
        focusPolicy: Qt.NoFocus
        autoRepeat: true
        Accessible.name: qsTr("Decrease value")
        onClicked: {
            const previous = control.value;
            control.decrease();
            if (control.value !== previous)
                control.valueModified();
        }
    }
    SiteFocusRing { active: control.activeFocus }
}

pragma ComponentBehavior: Bound
import QtQuick
import QtQuick.Controls

Button {
    id: control
    property string glyph: ""
    property url assetRoot: "qrc:/assets/"
    property bool accented: false
    property bool iconOnly: false
    property int glyphSize: 14
    property int textRenderType: Text.QtRendering
    property real cornerRadius: 8
    property color textColor: "#999999"
    property color hoverTextColor: "#2bfef5"
    property color accentColor: "#2bfef5"
    property color surfaceColor: "#0c0c0e"
    property color hoverColor: "#142bfef5"
    property color accentedColor: hoverColor
    property color borderColor: "#3397979f"
    property color accentBorder: "#332bfef5"
    property color hoverBorderColor: accentBorder
    property bool loading: false
    enabled: !loading
    implicitHeight: 30
    implicitWidth: iconOnly ? implicitHeight : contentItem.implicitWidth + leftPadding + rightPadding
    leftPadding: 10
    rightPadding: 10
    font.family: "DDetector Nunito"
    font.pixelSize: 12
    font.weight: Font.Bold
    opacity: enabled || loading ? 1 : 0.45
    Accessible.name: text
    HoverHandler {
        cursorShape: control.enabled ? Qt.PointingHandCursor : Qt.ArrowCursor
    }
    contentItem: Item {
        implicitWidth: Math.max(contentRow.implicitWidth, control.loading ? buttonSpinner.implicitWidth : 0)
        implicitHeight: contentRow.implicitHeight
        SiteSpinner {
            id: buttonSpinner
            objectName: "buttonSpinner"
            anchors.centerIn: parent
            visible: control.loading
            accentColor: control.accented ? control.accentColor : control.textColor
            label: control.text + " in progress"
        }
        Row {
            id: contentRow
            visible: !control.loading
            anchors.centerIn: parent
            spacing: control.glyph && !control.iconOnly ? 6 : 0
            SiteIcon {
                visible: !!control.glyph
                width: visible ? control.glyphSize : 0
                height: control.glyphSize
                anchors.verticalCenter: parent.verticalCenter
                glyph: control.glyph
                assetRoot: control.assetRoot
                tintColor: control.accented ? control.accentColor : control.hovered ? control.hoverTextColor : control.textColor
            }
            Text {
                objectName: "buttonLabel"
                renderType: control.textRenderType
                visible: !control.iconOnly
                text: control.text
                textFormat: Text.PlainText
                font: control.font
                color: control.accented ? control.accentColor : control.hovered ? control.hoverTextColor : control.textColor
                anchors.verticalCenter: parent.verticalCenter
            }
        }
    }
    background: Rectangle {
        radius: control.cornerRadius
        color: control.accented ? control.accentedColor : control.hovered || control.down ? control.hoverColor : control.surfaceColor
        border.color: control.visualFocus || control.accented ? control.accentBorder : control.hovered ? control.hoverBorderColor : control.borderColor
    }
    SiteFocusRing {
        objectName: "buttonFocusRing"
        active: control.visualFocus
        focusColor: control.accentColor
    }
}

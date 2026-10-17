pragma ComponentBehavior: Bound
import QtQuick
import QtQuick.Controls
import QtQuick.Layouts

CheckBox {
    id: control
    property bool indicatorOnLeft: false
    property string glyph: ""
    property url iconSource: ""
    property color iconColor: "transparent"
    property int iconWidth: 18
    property rect iconSourceClipRect: Qt.rect(0, 0, 0, 0)
    readonly property bool hasIconSource: iconSource.toString().length > 0
    implicitHeight: 56
    padding: 16
    Accessible.name: text
    HoverHandler {
        cursorShape: control.enabled ? Qt.PointingHandCursor : Qt.ArrowCursor
    }
    indicator: Rectangle {
        implicitWidth: 40
        implicitHeight: 22
        x: control.indicatorOnLeft ? control.leftPadding : control.width - width - control.rightPadding
        y: (control.height - height) / 2
        radius: 11
        color: control.checked ? "#142bfef5" : "#0c0c0e"
        border.color: control.checked ? "#332bfef5" : "#242426"
        Rectangle {
            width: 16
            height: 16
            radius: 8
            x: control.checked ? 21 : 3
            y: 3
            color: control.checked ? "#2bfef5" : "#999999"
            Behavior on x {
                NumberAnimation {
                    duration: 160
                }
            }
        }
    }
    contentItem: RowLayout {
        spacing: 12
        Item {
            visible: control.indicatorOnLeft
            Layout.minimumWidth: control.indicator.width
            Layout.preferredWidth: control.indicator.width
        }
        SiteIcon {
            visible: control.hasIconSource || !!control.glyph
            Layout.preferredWidth: visible ? control.iconWidth : 0
            Layout.preferredHeight: 18
            glyph: control.glyph
            iconSource: control.iconSource
            sourceClipRect: control.hasIconSource ? control.iconSourceClipRect : Qt.rect(0, 0, 0, 0)
            fillMode: Image.PreserveAspectFit
            tinted: !control.hasIconSource || control.iconColor.a > 0
            tintColor: control.hasIconSource ? control.iconColor : (control.enabled ? "#999999" : "#666666")
        }
        Text {
            objectName: "toggleLabel"
            Layout.fillWidth: true
            Layout.rightMargin: control.indicatorOnLeft ? 0 : 52
            text: control.text
            textFormat: Text.PlainText
            font: control.font
            color: "#f0f0f4"
            elide: Text.ElideRight
            verticalAlignment: Text.AlignVCenter
        }
    }
    background: Item {}
    SiteFocusRing {
        active: control.visualFocus
    }
}

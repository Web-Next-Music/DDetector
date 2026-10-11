import QtQuick

Item {
    id: shadow
    enabled: false
    z: 1

    readonly property real edgeOffset: 0
    readonly property real shadowExtent: 24
    property Flickable flickable: null
    property color shadowColor: "#000000"

    Rectangle {
        objectName: "scrollTopEdgeShadow"
        visible: shadow.flickable !== null && !shadow.flickable.atYBeginning
        anchors.top: parent.top
        anchors.topMargin: -shadow.edgeOffset
        width: parent.width
        height: shadow.shadowExtent
        gradient: Gradient {
            GradientStop { position: 0; color: Qt.rgba(shadow.shadowColor.r, shadow.shadowColor.g, shadow.shadowColor.b, 1) }
            GradientStop { position: 1; color: Qt.rgba(shadow.shadowColor.r, shadow.shadowColor.g, shadow.shadowColor.b, 0) }
        }
    }
    Rectangle {
        objectName: "scrollBottomEdgeShadow"
        visible: shadow.flickable !== null && !shadow.flickable.atYEnd
        anchors.bottom: parent.bottom
        anchors.bottomMargin: -shadow.edgeOffset
        width: parent.width
        height: shadow.shadowExtent
        gradient: Gradient {
            GradientStop { position: 0; color: Qt.rgba(shadow.shadowColor.r, shadow.shadowColor.g, shadow.shadowColor.b, 0) }
            GradientStop { position: 1; color: Qt.rgba(shadow.shadowColor.r, shadow.shadowColor.g, shadow.shadowColor.b, 1) }
        }
    }
}

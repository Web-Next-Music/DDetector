import QtQuick

Item {
    id: spinner
    property color accentColor: "#2bfef5"
    property string label: "Loading"
    implicitWidth: 33
    implicitHeight: 8
    Accessible.role: Accessible.StaticText
    Accessible.name: label

    Row {
        anchors.centerIn: parent
        spacing: 6
        Repeater {
            model: 3
            Rectangle {
                id: dot
                required property int index
                width: 7
                height: 7
                radius: width / 2
                color: spinner.accentColor
                property real pulse: 0
                opacity: 0.2 + 0.6 * pulse
                scale: 0.85 + 0.25 * pulse

                SequentialAnimation {
                    running: spinner.visible
                    onRunningChanged: { if (!running) dot.pulse = 0; }
                    PauseAnimation { duration: dot.index * 200 }
                    SequentialAnimation {
                        loops: Animation.Infinite
                        NumberAnimation {
                            target: dot
                            property: "pulse"
                            from: 0
                            to: 1
                            duration: 600
                            easing.type: Easing.BezierSpline
                            easing.bezierCurve: [0.42, 0, 0.58, 1, 1, 1]
                        }
                        NumberAnimation {
                            target: dot
                            property: "pulse"
                            from: 1
                            to: 0
                            duration: 600
                            easing.type: Easing.BezierSpline
                            easing.bezierCurve: [0.42, 0, 0.58, 1, 1, 1]
                        }
                    }
                }
            }
        }
    }
}

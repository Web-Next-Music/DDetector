pragma ComponentBehavior: Bound
import QtQuick

Item {
    id: ring
    property real progress: -1
    property color accentColor: "#2bfef5"
    property color trackColor: "#3397979f"
    property real strokeWidth: 2
    property string label: "Loading"
    readonly property bool indeterminate: progress < 0
    readonly property real fraction: Math.max(0, Math.min(1, progress))
    implicitWidth: 22
    implicitHeight: 22
    Accessible.role: Accessible.ProgressBar
    Accessible.name: label
    Accessible.description: indeterminate ? "In progress" : Math.round(fraction * 100) + "%"

    onProgressChanged: arc.requestPaint()
    onAccentColorChanged: arc.requestPaint()
    onTrackColorChanged: arc.requestPaint()
    onStrokeWidthChanged: arc.requestPaint()
    Canvas {
        id: arc
        anchors.fill: parent
        antialiasing: true
        onWidthChanged: requestPaint()
        onHeightChanged: requestPaint()
        onPaint: {
            const ctx = getContext("2d");
            ctx.reset();
            const radius = Math.max(0, (Math.min(width, height) - ring.strokeWidth) / 2);
            ctx.lineWidth = ring.strokeWidth;
            ctx.strokeStyle = ring.trackColor;
            ctx.beginPath();
            ctx.arc(width / 2, height / 2, radius, 0, 2 * Math.PI);
            ctx.stroke();
            const amount = ring.indeterminate ? 0.25 : ring.fraction;
            if (amount > 0) {
                ctx.strokeStyle = ring.accentColor;
                ctx.lineCap = "round";
                ctx.beginPath();
                ctx.arc(width / 2, height / 2, radius, -Math.PI / 2, -Math.PI / 2 + amount * 2 * Math.PI);
                ctx.stroke();
            }
        }
        NumberAnimation on rotation {
            running: ring.visible && ring.indeterminate
            from: 0
            to: 360
            duration: 1000
            loops: Animation.Infinite
            onRunningChanged: if (!running) arc.rotation = 0
        }
    }
}

pragma ComponentBehavior: Bound
import QtQuick
import QtQuick.Shapes

// Covers rounded outside corners without flattening the content into a texture.
// Use inside a rectangularly clipped panel on a uniform opaque background, above
// its content and below its border. This is visual occlusion, not an input mask.
Item {
    id: cover
    required property real radius
    required property color backgroundColor
    readonly property real cornerRadius: Math.max(0, Math.min(radius, width / 2, height / 2))

    Repeater {
        model: 4
        Shape {
            id: corner
            required property int index
            width: cover.cornerRadius
            height: cover.cornerRadius
            x: index === 1 || index === 2 ? cover.width - width : 0
            y: index >= 2 ? cover.height - height : 0
            rotation: index * 90
            preferredRendererType: Shape.GeometryRenderer
            ShapePath {
                strokeWidth: -1
                fillColor: cover.backgroundColor
                PathSvg {
                    path: "M 0 0 H " + cover.cornerRadius
                        + " A " + cover.cornerRadius + " " + cover.cornerRadius
                        + " 0 0 0 0 " + cover.cornerRadius + " Z"
                }
            }
        }
    }
}

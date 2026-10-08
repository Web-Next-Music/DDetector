import QtQuick
import QtQuick.Controls
import QtQuick.Layouts
import QtQuick.Effects

SiteDialog {
    id: dialog

    property var uploadBackend: null
    readonly property bool busy: !!uploadBackend && uploadBackend.busy
    property string trackId: ""
    property string originalTitle: ""
    property url coverSource: ""
    property bool sending: false
    signal sent(string action)

    function openTrack(id, title, artist, cover) {
        if (busy || sending)
            return
        trackId = id === null || id === undefined ? "" : String(id)
        originalTitle = title ? String(title) : ""
        titleInput.text = originalTitle
        artistInput.text = Array.isArray(artist) ? artist.join(", ") : (artist ? String(artist) : "")
        urlInput.text = ""
        coverSource = cover || ""
        open()
    }


    width: Math.min(400, parent ? parent.width - 32 : 400)
    icon: "qrc:/assets/lucide-music-standard.svg"
    title: "Send track"
    closeEnabled: !busy
    closePolicy: busy ? Popup.NoAutoClose : Popup.CloseOnEscape | Popup.CloseOnPressOutside

    onOpened: artistInput.forceActiveFocus()



    component Field: TextField {
        Layout.fillWidth: true
        enabled: !dialog.busy
        implicitHeight: 36
        padding: 10
        leftPadding: 12
        rightPadding: 12
        selectByMouse: true
        color: "#f0f0f4"
        placeholderTextColor: "#97979f"
        selectionColor: "#142bfef5"
        selectedTextColor: "#f0f0f4"
        font.family: dialog.font.family
        font.pixelSize: 14
        background: Rectangle {
            radius: 8
            color: "#0c0c0e"
            border.color: parent.activeFocus ? "#332bfef5" : "#3397979f"
            opacity: parent.enabled ? 1 : 0.55
        }
    }

    component Caption: Label {
        color: "#f0f0f4"
        font.family: dialog.font.family
        font.pixelSize: 13
    }

    component ActionButton: Button {
        property bool accented: false
        enabled: !dialog.busy
        padding: 8
        leftPadding: 16
        rightPadding: 16
        font.family: dialog.font.family
        font.pixelSize: 14
        contentItem: Text {
            text: parent.text
            font: parent.font
            color: parent.accented ? "#2bfef5" : "#f0f0f4"
            horizontalAlignment: Text.AlignHCenter
            verticalAlignment: Text.AlignVCenter
            opacity: parent.enabled ? 1 : 0.45
        }
        background: Rectangle {
            radius: 8
            color: parent.accented ? (parent.hovered ? "#292bfef5" : "#142bfef5") : "#171719"
            border.color: parent.accented ? "#332bfef5" : "#3397979f"
        }
    }

    contentItem: ColumnLayout {
        spacing: 8

        Rectangle {
            Layout.fillWidth: true
            implicitHeight: 84
            radius: 14
            color: "#171719"
            border.color: "#3397979f"
            RowLayout {
                anchors.fill: parent
                anchors.margins: 10
                spacing: 10
                Rectangle {
                    Layout.preferredWidth: 64
                    Layout.preferredHeight: 64
                    color: "#0c0c0e"
                    radius: 8
                    clip: true
                    Image {
                        id: coverImage
                        anchors.fill: parent
                        anchors.margins: 2
                        source: dialog.coverSource
                        sourceSize: Qt.size(128, 128)
                        fillMode: Image.PreserveAspectFit
                        asynchronous: true
                    }
                    Image {
                        anchors.centerIn: parent
                        width: 24; height: 24
                        source: "qrc:/assets/lucide-music-standard.svg"
                        layer.enabled: true
                        layer.effect: MultiEffect { brightness: 1; colorization: 1; colorizationColor: "#97979f" }
                        visible: coverImage.status !== Image.Ready
                    }
                }
                ColumnLayout {
                    Layout.fillWidth: true
                    Caption {
                        Layout.fillWidth: true
                        text: dialog.originalTitle || "Track"
                        elide: Text.ElideRight
                        font.bold: true
                    }
                    Caption {
                        Layout.fillWidth: true
                        text: "ID: " + dialog.trackId
                        color: "#97979f"
                        elide: Text.ElideMiddle
                    }
                }
            }
        }

        Rectangle {
            Layout.fillWidth: true
            implicitHeight: fieldsContent.implicitHeight + 32
            radius: 14
            color: "#171719"
            border.color: "#3397979f"
            ColumnLayout {
                id: fieldsContent
                anchors.fill: parent
                anchors.margins: 16
                spacing: 12
                Caption { text: "Artist" }
                Field { id: artistInput; placeholderText: "Artists, comma-separated"; Accessible.name: "Artist" }
                Caption { text: "Title" }
                Field { id: titleInput; placeholderText: dialog.originalTitle; Accessible.name: "Title" }
                Caption { text: "MP3 URL · optional" }
                Field {
                    id: urlInput
                    placeholderText: "https://…"
                    Accessible.name: "MP3 URL"
                    inputMethodHints: Qt.ImhUrlCharactersOnly
                }
                Caption {
                    Layout.fillWidth: true
                    text: "Leave the URL empty to add only the list entry."
                    color: "#97979f"
                    wrapMode: Text.WordWrap
                    font.pixelSize: 12
                }
            }
        }
        Caption {
            Layout.fillWidth: true
            visible: text.length > 0
            text: !dialog.uploadBackend ? "Upload backend is not configured." : dialog.uploadBackend.error
            color: "#ff9292"
            wrapMode: Text.Wrap
        }
        Caption {
            Layout.fillWidth: true
            visible: text.length > 0
            text: dialog.uploadBackend ? dialog.uploadBackend.status : ""
            color: dialog.busy ? "#2bfef5" : "#97979f"
            wrapMode: Text.Wrap
        }
        RowLayout {
            Layout.fillWidth: true
            Item { Layout.fillWidth: true }
            ActionButton { text: "Cancel"; onClicked: dialog.close() }
            ActionButton {
                text: dialog.busy ? "Sending…" : "Send"
                accented: true
                enabled: !!dialog.uploadBackend && !dialog.busy && dialog.trackId.length > 0
                onClicked: {
                    dialog.sending = true
                    dialog.uploadBackend.send(dialog.trackId,
                                              titleInput.text.trim() || dialog.originalTitle || dialog.trackId,
                                              artistInput.text, urlInput.text.trim())
                    if (!dialog.busy)
                        dialog.sending = false
                }
            }
        }
    }

    Connections {
        target: dialog.uploadBackend
        function onCompleted(action) {
            if (!dialog.sending)
                return
            dialog.sending = false
            dialog.close()
            dialog.sent(action)
        }
        function onStateChanged() {
            if (dialog.uploadBackend && !dialog.uploadBackend.busy && dialog.uploadBackend.error)
                dialog.sending = false
        }
    }
}

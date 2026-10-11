pragma ComponentBehavior: Bound
import QtQuick
import QtQuick.Controls
import QtQuick.Layouts
import QtQuick.Effects
import QtMultimedia

SiteDialog {
    id: dialog

    property var uploadBackend: null
    readonly property bool busy: !!uploadBackend && uploadBackend.busy
    property string trackId: ""
    property string originalTitle: ""
    property url coverSource: ""
    property bool showUrlFields: true
    property bool sending: false
    property bool approveSoundEnabled: false
    signal sent(string action)

    function configureTrack(id, title, artist, cover, mp3Url, showUrl) {
        if (busy || sending)
            return
        trackId = id === null || id === undefined ? "" : String(id)
        originalTitle = title ? String(title) : ""
        titleInput.text = originalTitle
        artistInput.text = Array.isArray(artist) ? artist.join(", ") : (artist ? String(artist) : "")
        urlInput.text = mp3Url ? String(mp3Url) : ""
        showUrlFields = showUrl
        coverSource = cover || ""
        open()
    }

    function openTrack(id, title, artist, cover) {
        configureTrack(id, title, artist, cover, "", true)
    }

    function openCandidate(id, title, artist, cover, mp3Url) {
        configureTrack(id, title, artist, cover, mp3Url, false)
    }


    width: Math.min(400, parent ? parent.width - 32 : 400)
    icon: "qrc:/assets/lucide-music-standard.svg"
    title: "Send track"
    closeEnabled: !busy
    closePolicy: busy ? Popup.NoAutoClose : Popup.CloseOnEscape | Popup.CloseOnPressOutside

    onOpened: artistInput.forceActiveFocus()

    MediaPlayer {
        id: approveSound
        objectName: "approveSoundPlayer"
        audioOutput: AudioOutput {}
    }

    component Field: SiteTextField {
        Layout.fillWidth: true
        enabled: !dialog.busy
        font.family: dialog.font.family
    }

    component Caption: Label { textFormat: Text.PlainText;
        color: "#f0f0f4"
        font.family: dialog.font.family
        font.pixelSize: 13
    }

    component ActionButton: SiteButton {
        enabled: !dialog.busy
        padding: 8
        leftPadding: 16
        rightPadding: 16
        font.family: dialog.font.family
        font.pixelSize: 14
        textColor: "#f0f0f4"
        hoverTextColor: textColor
        surfaceColor: "#171719"
        hoverColor: surfaceColor
        hoverBorderColor: borderColor
        accentedColor: hovered ? "#292bfef5" : "#142bfef5"
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

        SiteSettingsCard {
            Layout.fillWidth: true
            Caption { text: "Artist" }
            Field { id: artistInput; placeholderText: "Artists, comma-separated"; Accessible.name: "Artist" }
            Caption { text: "Title" }
            Field { id: titleInput; placeholderText: dialog.originalTitle; Accessible.name: "Title" }
            Caption { text: "MP3 URL · optional"; visible: dialog.showUrlFields }
            Field {
                id: urlInput
                visible: dialog.showUrlFields
                placeholderText: "https://…"
                Accessible.name: "MP3 URL"
                inputMethodHints: Qt.ImhUrlCharactersOnly
            }
            Caption {
                Layout.fillWidth: true
                visible: dialog.showUrlFields
                text: "Leave the URL empty to add only the list entry"
                color: "#97979f"
                wrapMode: Text.WordWrap
                font.pixelSize: 12
            }
        }
        Caption {
            Layout.fillWidth: true
            visible: text.length > 0
            text: !dialog.uploadBackend ? "Upload backend is not configured" : dialog.uploadBackend.error
            Accessible.role: Accessible.AlertMessage
            Accessible.name: text
            color: "#ff9292"
            wrapMode: Text.Wrap
        }

        RowLayout {
            Layout.fillWidth: true
            Item { Layout.fillWidth: true }
            ActionButton { text: "Cancel"; onClicked: dialog.close() }
            ActionButton {
                objectName: "sendTrackButton"
                text: "Send"
                loading: dialog.busy
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
            if (dialog.approveSoundEnabled) {
                approveSound.source = "qrc:/assets/approved.mp3"
                approveSound.position = 0
                approveSound.play()
            }
            dialog.sent(action)
        }
        function onStateChanged() {
            if (dialog.uploadBackend && !dialog.uploadBackend.busy && dialog.uploadBackend.error)
                dialog.sending = false
        }
    }
}

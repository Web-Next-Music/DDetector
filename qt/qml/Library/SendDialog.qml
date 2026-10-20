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
        property real volume: 0.5
    property var initialMetadata: ({})
    property string initialArtistsText: ""
    property var metadataBaseline: ({})
    property var originalMetadata: ({})
    property string originalArtist: ""
    readonly property string artistsPlaceholder: (originalMetadata.artists || []).map(function(a) {
        const name = String(a.name || "")
        return a.id === undefined || a.id === null || String(a.id) === "" ? name : String(a.id) + ": " + name
    }).filter(function(a) { return a.length > 0 }).join("; ")
    readonly property string coverPlaceholder: String(originalMetadata.coverUri || "").replace(/^https?:\/\//i, "").replace(/^\/\//, "")
    property int durationRequest: -1
    property real replacementDuration: -1
    property bool automaticDuration: false
    readonly property string durationPlaceholder: replacementDuration > 0 ? String(replacementDuration) : "Unknown"

    function resetDuration() {
        durationRequest = -1
        if (automaticDuration && metadataDuration.text === String(replacementDuration)) {
            metadataDuration.text = metadataBaseline.durationMs
            editDuration.checked = false
        }
        replacementDuration = -1
        automaticDuration = false
    }

    function requestDuration() {
        resetDuration()
        if (uploadBackend && typeof uploadBackend.requestDuration === "function")
            durationRequest = uploadBackend.requestDuration(trackId, urlInput.text.trim())
    }

    Timer { id: durationTimer; interval: 400; onTriggered: dialog.requestDuration() }
    function metadataPatch() {
        const patch = {}
        if (!editMetadata.checked) return patch
        if (editTitle.checked && metadataTitle.text.trim() !== metadataBaseline.title)
            patch.title = metadataTitle.text.trim()
        if (editCover.checked && metadataCover.text.trim() !== metadataBaseline.coverUri)
            patch.coverUri = metadataCover.text.trim()
        if (editDuration.checked && (metadataDuration.text.trim() !== metadataBaseline.durationMs || automaticDuration))
            patch.durationMs = metadataDuration.text.trim() === "" ? -1 : Number(metadataDuration.text)
        if (editArtists.checked && metadataArtists.text !== initialArtistsText) {
            patch.artists = metadataArtists.text.split(";").map(function(part) {
                const split = part.indexOf(":")
                return {id: split < 0 ? "" : part.slice(0, split).trim(), name: split < 0 ? part.trim() : part.slice(split + 1).trim()}
            })
        }
        return patch
    }
    signal sent(string action)

    function configureTrack(id, title, artist, cover, mp3Url, showUrl, metadata) {
        if (busy || sending)
            return
        trackId = id === null || id === undefined ? "" : String(id)
        originalTitle = title ? String(title) : ""
        originalMetadata = metadata || ({})
        titleInput.text = originalTitle
        originalArtist = Array.isArray(artist) ? artist.join(", ") : (artist ? String(artist) : "")
        artistInput.text = originalArtist
        urlInput.text = mp3Url ? String(mp3Url) : ""
        initialMetadata = uploadBackend && typeof uploadBackend.trackMetadata === "function" ? uploadBackend.trackMetadata(trackId) : ({})
        durationTimer.stop()
        durationRequest = -1
        replacementDuration = -1
        automaticDuration = false
        editMetadata.checked = false
        editTitle.checked = false; editCover.checked = false
        editDuration.checked = false; editArtists.checked = false
        metadataBaseline = {
            title: String(initialMetadata.title !== undefined ? initialMetadata.title : originalMetadata.title || originalTitle).trim(),
            coverUri: String(initialMetadata.coverUri !== undefined ? initialMetadata.coverUri : coverPlaceholder).trim(),
            durationMs: initialMetadata.durationMs !== undefined ? String(initialMetadata.durationMs).trim()
                : originalMetadata.durationMs !== undefined && originalMetadata.durationMs !== null
                    && Number.isFinite(Number(originalMetadata.durationMs)) && Number(originalMetadata.durationMs) > 0
                    ? String(Math.round(Number(originalMetadata.durationMs))) : ""
        }
        metadataTitle.text = metadataBaseline.title
        metadataCover.text = metadataBaseline.coverUri
        metadataDuration.text = metadataBaseline.durationMs
        initialArtistsText = initialMetadata.artists !== undefined
            ? (initialMetadata.artists || []).map(function(a) { return String(a.id === undefined ? "" : a.id) + ": " + String(a.name || "") }).join("; ")
            : artistsPlaceholder || originalArtist
        metadataArtists.text = initialArtistsText
        showUrlFields = showUrl
        coverSource = cover || ""
        open()
        requestDuration()
    }

    function openTrack(id, title, artist, cover, metadata) {
        configureTrack(id, title, artist, cover, "", true, metadata)
    }

    function openCandidate(id, title, artist, cover, mp3Url, metadata) {
        configureTrack(id, title, artist, cover, mp3Url, false, metadata)
    }


    width: Math.min(480, parent ? parent.width - 32 : 480)
    icon: "qrc:/assets/lucide-music-standard.svg"
    title: "Send track"
    closeEnabled: !busy
    closePolicy: busy ? Popup.NoAutoClose : Popup.CloseOnEscape | Popup.CloseOnPressOutside

    onOpened: artistInput.forceActiveFocus()
    onClosed: {
        durationTimer.stop()
        durationRequest = -1
    }

    MediaPlayer {
        id: approveSound
        objectName: "approveSoundPlayer"
        audioOutput: AudioOutput { volume: dialog.volume }
    }

    component Field: SiteTextField {
        Layout.fillWidth: true
        enabled: !dialog.busy
        font.family: dialog.font.family
    }

    component MetadataToggle: RowLayout {
        property alias checked: toggle.checked
        property alias text: caption.text
        SiteCheckBox { id: toggle; enabled: !dialog.busy; Accessible.name: caption.text }
        Caption { id: caption; Layout.fillWidth: true; wrapMode: Text.WordWrap }
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

    contentItem: ScrollView {
        implicitHeight: Math.min(form.implicitHeight, dialog.parent ? dialog.parent.height - 120 : 600)
        clip: true
        contentWidth: availableWidth
        ColumnLayout {
        id: form
        width: parent.width
        spacing: 8

        Rectangle {
            objectName: "trackSummaryCard"
            Layout.fillWidth: true
            implicitHeight: 84
            radius: 14
            color: "#171719"
            border.color: "#242426"
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
                        objectName: "trackSummaryTitle"
                        text: dialog.originalTitle || "Track"
                        font.pixelSize: 16
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
            objectName: "standardSendPanel"
            visible: !editMetadata.checked
            Layout.fillWidth: true
            Caption { text: "Artist · README / commit label" }
            Field { id: artistInput; objectName: "sendArtist"; placeholderText: "Artists, comma-separated"; Accessible.name: "Artist" }
            Caption { text: "Title · README / commit label" }
            Field { id: titleInput; objectName: "sendTitle"; placeholderText: dialog.originalTitle; Accessible.name: "Title" }
            ColumnLayout {
                id: standardUrlSlot
                Layout.fillWidth: true
                visible: dialog.showUrlFields
            }
        }
        ColumnLayout {
            parent: editMetadata.checked ? metadataUrlSlot : standardUrlSlot
            Layout.fillWidth: true
            spacing: 12
            Caption { text: "MP3 URL · optional" }
            Field {
                id: urlInput
                objectName: "sendUrl"
                placeholderText: "https://…"
                Accessible.name: "MP3 URL"
                inputMethodHints: Qt.ImhUrlCharactersOnly
                onTextEdited: {
                    dialog.resetDuration()
                    durationTimer.restart()
                }
            }
            Caption {
                Layout.fillWidth: true
                text: "Leave the URL empty to add only the list entry"
                color: "#97979f"
                wrapMode: Text.WordWrap
                font.pixelSize: 12
            }
        }
        SiteSettingsCard {
            objectName: "metadataSendPanel"
            visible: editMetadata.checked
            Layout.fillWidth: true
            ColumnLayout {
                visible: editMetadata.checked
                Layout.fillWidth: true
                MetadataToggle { id: editTitle; objectName: "editTitle"; text: "Override title"; enabled: !dialog.busy }
                Field { id: metadataTitle; objectName: "metadataTitle"; enabled: editTitle.checked && !dialog.busy; Accessible.name: "Metadata title"; placeholderText: dialog.originalMetadata.title || dialog.originalTitle || "Track title" }
                MetadataToggle { id: editArtists; objectName: "editArtists"; text: "Override artists"; enabled: !dialog.busy }
                Field { id: metadataArtists; objectName: "metadataArtists"; enabled: editArtists.checked && !dialog.busy; Accessible.name: "Metadata artists with IDs"; placeholderText: dialog.artistsPlaceholder || dialog.originalArtist || "Artist ID: Name" }
                MetadataToggle { id: editCover; objectName: "editCover"; text: "Override cover URI"; enabled: !dialog.busy }
                Field { id: metadataCover; objectName: "metadataCover"; enabled: editCover.checked && !dialog.busy; Accessible.name: "Metadata cover URI"; placeholderText: dialog.coverPlaceholder || "Cover URI" }
                MetadataToggle { id: editDuration; objectName: "editDuration"; text: "Override duration (milliseconds)"; enabled: !dialog.busy }
                Field { id: metadataDuration; objectName: "metadataDuration"; enabled: editDuration.checked && !dialog.busy; Accessible.name: "Metadata duration milliseconds"; placeholderText: dialog.durationPlaceholder; inputMethodHints: Qt.ImhDigitsOnly }
            }
            ColumnLayout {
                id: metadataUrlSlot
                Layout.fillWidth: true
            }
        }
        Caption {
            Layout.fillWidth: true
            visible: text.length > 0
            objectName: "sendError"
            text: !dialog.uploadBackend ? "Upload backend is not configured"
                : dialog.uploadBackend.error === "Wait for the configured repository to be ready before sending" ? "" : dialog.uploadBackend.error
            Accessible.role: Accessible.AlertMessage
            Accessible.name: text
            color: "#ff9292"
            wrapMode: Text.Wrap
        }

        RowLayout {
            id: footer
            objectName: "sendFooter"
            Layout.fillWidth: true
            SiteToggleRow {
                id: editMetadata
                objectName: "editMetadata"
                Layout.fillWidth: true
                Layout.minimumWidth: 40
                Layout.preferredWidth: 174
                Layout.maximumWidth: 174
                Layout.preferredHeight: cancelButton.implicitHeight
                padding: 0
                indicatorOnLeft: true
                text: "Edit metadata"
                font.family: dialog.font.family
                font.pixelSize: 13
                enabled: !dialog.busy
            }
            Item { Layout.fillWidth: true; Layout.minimumWidth: 0 }
            ActionButton { id: cancelButton; objectName: "cancelSendButton"; text: "Cancel"; onClicked: dialog.close() }
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
                                              artistInput.text, urlInput.text.trim(), dialog.metadataPatch())
                    if (!dialog.busy)
                        dialog.sending = false
                }
            }
        }
    }

    }

    Connections {
        target: dialog.uploadBackend
        function onDurationReady(request, durationMs) {
            if (request !== dialog.durationRequest || !dialog.visible || dialog.busy || dialog.sending)
                return
            dialog.replacementDuration = durationMs
            if (durationMs <= 0 || metadataDuration.text !== dialog.metadataBaseline.durationMs)
                return
            const original = Number(dialog.originalMetadata.durationMs)
            if (!Number.isFinite(original) || original <= 0 || durationMs === original)
                return
            dialog.automaticDuration = true
            metadataDuration.text = String(durationMs)
            editDuration.checked = true
            editMetadata.checked = true
        }
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

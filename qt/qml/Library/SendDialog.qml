pragma ComponentBehavior: Bound
import QtQuick
import QtQuick.Controls
import QtQuick.Layouts
import QtQuick.Effects
import QtMultimedia

SiteDialog {
    id: dialog

    property var uploadBackend: null
    property var prepareSend: null
    function submit() {
        if (!canSend) return
        const args = [trackId, titleInput.text.trim() || originalTitle || trackId,
                      artistInput.text, urlInput.text.trim(), metadataPatch(), originalMetadata, restoreMode]
        sending = true
        const proceed = function(success) {
            if (success) dialog.uploadBackend.send(...args)
            if (!dialog.busy) dialog.sending = false
        }
        if (prepareSend) prepareSend(proceed)
        else proceed(true)
    }
    readonly property bool busy: !!uploadBackend && uploadBackend.busy
    property string trackId: ""
    property bool restoreMode: false
    property int metadataRequest: -1
    property var restoreArtists: []
    property string metadataError: ""
    readonly property string restorePreviewTitle: {
        const artists = restoreArtists.length ? restoreArtists
            : initialMetadata.artists && initialMetadata.artists.length ? initialMetadata.artists
            : originalMetadata.artists || []
        const names = artists.map(function(a) {
            const name = String(a.name || "").trim()
            if (!name) return ""
            return name
        }).filter(function(name) { return name.length > 0 }).join(", ") || originalArtist
        return (metadataBaseline.title || originalTitle || "Track") + (names ? " - " + names : "")
    }
    function artistPatch(artists) {
        return Array.prototype.map.call(artists, function(a) { return {id: a.id, name: a.name} })
    }
    function setRestoreArtistId(index, value) {
        const artists = artistPatch(restoreArtists)
        artists[index].id = value.trim()
        restoreArtists = artists
    }
    readonly property string artistIdError: {
        const seen = {}
        for (let i = 0; i < restoreArtists.length; ++i) {
            const artist = restoreArtists[i]
            const id = String(artist.id === undefined ? "" : artist.id)
            if (!/^[1-9][0-9]*$/.test(id) || !Number.isSafeInteger(Number(id)))
                return "Artist " + (i + 1) + " (" + artist.name + "): enter a positive numeric Yandex artist ID."
            if (seen[id]) return "Artist " + (i + 1) + " (" + artist.name + "): duplicate ID " + id + ". Enter a unique artist ID."
            seen[id] = true
        }
        return ""
    }
    function validArtists(artists) {
        return artists && artists.length > 0 && Array.prototype.every.call(artists, function(a) {
            return /^[1-9][0-9]*$/.test(String(a.id || "")) && String(a.name || "").trim().length > 0
        })
    }
    function loadRestoreMetadata() {
        metadataRequest = -1
        metadataError = ""
        restoreArtists = validArtists(initialMetadata.artists) ? initialMetadata.artists
            : validArtists(originalMetadata.artists) ? originalMetadata.artists : []
        if (!restoreArtists.length && trackId) {
            if (uploadBackend && typeof uploadBackend.requestMetadata === "function")
                metadataRequest = uploadBackend.requestMetadata(trackId)
            else metadataError = "Yandex metadata API is unavailable. Reopen Restore to retry."
        }
    }
    function applyRestoreMetadata(request, id, metadata, error) {
        if (!restoreMode || metadataRequest < 0 || request !== metadataRequest || String(id) !== trackId
                || trackId !== String(Number(restoreIdInput.text.trim()))) return
        metadataRequest = -1
        if (error || !validArtists(metadata.artists)) {
            metadataError = error || "Yandex API returned no artist IDs for this track. Restore is unavailable."
            return
        }
        restoreArtists = metadata.artists
        if (!metadataBaseline.title && metadata.title) {
            metadataBaseline = Object.assign({}, metadataBaseline, {title: String(metadata.title)})
            metadataTitle.text = metadataBaseline.title
        }
        if (!metadataBaseline.coverUri && metadata.coverUri) {
            metadataBaseline = Object.assign({}, metadataBaseline, {coverUri: String(metadata.coverUri)})
            metadataCover.text = metadataBaseline.coverUri
        }
    }
    readonly property bool validRestoreId: isValidRestoreId(restoreIdInput.text)

    function isValidRestoreId(text) {
        const value = text.trim()
        return /^[0-9]+$/.test(value) && Number.isSafeInteger(Number(value)) && Number(value) > 0
    }
    readonly property bool canSend: !!uploadBackend && !busy && !sending
        && (restoreMode ? metadataRequest < 0
                            && restoreArtists.length > 0 && !artistIdError && validRestoreId && trackId === String(Number(restoreIdInput.text.trim()))
                    && urlInput.text.trim().length > 0 : trackId.length > 0)
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
        if ((!restoreMode || isValidRestoreId(restoreIdInput.text)) && uploadBackend && typeof uploadBackend.requestDuration === "function")
            durationRequest = uploadBackend.requestDuration(trackId, urlInput.text.trim())
    }

    Timer { id: durationTimer; interval: 400; onTriggered: dialog.requestDuration() }
    function fallbackCoverUrl(value) {
        const raw = String(value || "").trim()
        const provider = /^image:\/\/(?:trackcovers|cover)\//.exec(raw)
        if (provider) {
            try { return decodeURIComponent(raw.slice(provider[0].length)) }
            catch (e) { return "" }
        }
        return raw
    }
    function isTrustedFallbackCover(value) {
        return /^https:\/\/images\.(?:genius|rapgenius)\.com(?::443)?\/[^?#\s]+$/.test(String(value))
    }

        function metadataPatch() {
        const patch = {}
        // Artwork is publication data even when manual metadata editing is off.
        if (!String(originalMetadata.coverUri || "").trim() && metadataBaseline.coverUri)
            patch.coverUri = metadataBaseline.coverUri
        if (restoreMode) {
            patch.title = metadataBaseline.title
            patch.artists = artistPatch(restoreArtists)
            if (metadataBaseline.coverUri) patch.coverUri = metadataBaseline.coverUri
        }
        // Persist the supplied CDN cover, not the preview's image:// provider URL.
        const suppliedCover = fallbackCoverUrl(coverSource)
        if (!String(originalMetadata.coverUri || "").trim()
                && isTrustedFallbackCover(suppliedCover))
            patch.coverUri = suppliedCover
        if (!editMetadata.checked) return patch
        if (editTitle.checked && metadataTitle.text.trim() !== metadataBaseline.title)
            patch.title = metadataTitle.text.trim()
        if (editCover.checked && (metadataCover.text.trim() !== metadataBaseline.coverUri || patch.coverUri !== undefined))
            patch.coverUri = metadataCover.text.trim()
        if (editDuration.checked && (metadataDuration.text.trim() !== metadataBaseline.durationMs || automaticDuration))
            patch.durationMs = metadataDuration.text.trim() === "" ? -1 : Number(metadataDuration.text)
        if (!restoreMode && editArtists.checked && metadataArtists.text !== initialArtistsText) {
            patch.artists = metadataArtists.text.split(";").map(function(part) {
                const split = part.indexOf(":")
                return {id: split < 0 ? "" : part.slice(0, split).trim(), name: split < 0 ? part.trim() : part.slice(split + 1).trim()}
            })
        }
        return patch
    }
    signal sent(string action)

    function configureTrack(id, title, artist, cover, mp3Url, showUrl, metadata, reopen = true) {
        if (busy || sending)
            return
        trackId = id === null || id === undefined ? "" : String(id)
        originalTitle = title ? String(title) : ""
        originalMetadata = metadata || ({})

        titleInput.text = originalTitle
        originalArtist = Array.isArray(artist) ? artist.join(", ") : (artist ? String(artist) : "")
        artistInput.text = originalArtist
        urlInput.text = mp3Url ? String(mp3Url) : ""
        initialMetadata = trackId && uploadBackend && typeof uploadBackend.trackMetadata === "function" ? uploadBackend.trackMetadata(trackId) : ({})
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
        if (reopen)
            open()
        if (restoreMode) loadRestoreMetadata()
        requestDuration()
    }

    function openTrack(id, title, artist, cover, metadata) {
        if (busy || sending) return
        restoreMode = false
        configureTrack(id, title, artist, cover, "", true, metadata)
    }

    function openCandidate(id, title, artist, cover, mp3Url, metadata) {
        if (busy || sending) return
        restoreMode = false
        configureTrack(id, title, artist, cover, mp3Url, false, metadata)
    }

    function openRestore(id, title, artist, cover, metadata, mp3Url) {
        if (busy || sending) return
        restoreMode = false
        restoreIdInput.text = id === undefined || id === null ? "" : String(id)
        restoreMode = true
        const normalizedId = isValidRestoreId(restoreIdInput.text) ? String(Number(restoreIdInput.text.trim())) : ""
        configureTrack(normalizedId, title || "", artist || "", cover || "", mp3Url || "", true, metadata || ({}))
    }

    function reloadRestoreTrack() {
        if (!restoreMode || busy || sending) return
        const id = isValidRestoreId(restoreIdInput.text) ? String(Number(restoreIdInput.text.trim())) : ""
        configureTrack(id, "", "", "", urlInput.text, true, ({}), false)
    }

    width: Math.min(480, parent ? parent.width - 32 : 480)
    icon: "qrc:/assets/lucide-music-standard.svg"
    title: restoreMode ? "Restore" : "Send track"
    closeEnabled: !busy
    closePolicy: busy || sending ? Popup.NoAutoClose : Popup.CloseOnEscape | Popup.CloseOnPressOutside

    onOpened: restoreMode ? restoreIdInput.forceActiveFocus() : artistInput.forceActiveFocus()
    onClosed: {
        durationTimer.stop()
        durationRequest = -1
        metadataRequest = -1
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
        Label { text: dialog.metadataError; visible: text.length > 0; wrapMode: Text.Wrap; Layout.fillWidth: true }
        id: form
        width: parent.width
        spacing: 8


        Rectangle {
            objectName: "trackSummaryCard"
            Layout.fillWidth: true
            implicitHeight: Math.max(84, summaryContent.implicitHeight + 20)
            radius: 14
            color: "#171719"
            border.color: "#242426"
            RowLayout {
                anchors.fill: parent
                anchors.margins: 10
                spacing: 10
                Rectangle {
                    objectName: "sendCoverPreview"
                    visible: !dialog.restoreMode || dialog.coverSource.toString().length > 0
                    Layout.preferredWidth: 64
                    Layout.preferredHeight: 64
                    color: "#0c0c0e"
                    radius: 8
                    clip: true
                    Image {
                        id: coverImage
                        anchors.fill: parent
                        anchors.margins: 2
                        objectName: "sendCoverImage"
                        source: dialog.isTrustedFallbackCover(dialog.coverSource)
                                                        ? "image://trackcovers/" + encodeURIComponent(String(dialog.coverSource)) : dialog.coverSource
                        sourceSize: Qt.size(Math.ceil(Math.max(128, width * Screen.devicePixelRatio)),
                                                                    Math.ceil(Math.max(128, height * Screen.devicePixelRatio)))
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
                    id: summaryContent
                    Layout.fillWidth: true
                    Flow {
                        Layout.fillWidth: true
                        spacing: 0
                    Caption {
                        id: summaryTitle
                        width: Math.min(implicitWidth, summaryContent.width)
                        objectName: "trackSummaryTitle"
                        text: dialog.restoreMode ? (dialog.restoreArtists.length ? dialog.metadataBaseline.title || dialog.originalTitle || "Track" : dialog.restorePreviewTitle) : dialog.originalTitle || "Track"
                        font.pixelSize: 16
                        wrapMode: dialog.restoreMode ? Text.Wrap : Text.NoWrap
                        elide: dialog.restoreMode ? Text.ElideNone : Text.ElideRight
                        font.bold: true
                    }
                    Caption {
                        visible: dialog.restoreMode && dialog.restoreArtists.length > 0
                        text: " - "
                        font: summaryTitle.font
                    }
                        Repeater {
                            objectName: "restoreArtistFields"
                            model: dialog.restoreMode ? dialog.restoreArtists.length : 0
                            delegate: Item {
                                id: artistRow
                                required property int index
                                property bool editing: false
                                width: Math.min(editing ? idMetrics.advanceWidth + 2 : artistName.implicitWidth, summaryContent.width)
                                height: editing ? idMetrics.height : artistName.implicitHeight
                                TextMetrics { id: idMetrics; font: summaryTitle.font; text: artistId.text }
                                Caption {
                                    id: artistName
                                    objectName: "restoreArtistName" + artistRow.index
                                    anchors.fill: parent
                                    visible: !artistRow.editing
                                    text: String(dialog.restoreArtists[artistRow.index].name || "Artist") + (artistRow.index + 1 < dialog.restoreArtists.length ? ", " : "")
                                    font: Qt.font({family: summaryTitle.font.family,
                                                   pixelSize: summaryTitle.font.pixelSize,
                                                   weight: summaryTitle.font.weight,
                                                   underline: hovered || activeFocus})
                                    wrapMode: Text.Wrap
                                    activeFocusOnTab: true
                                    readonly property bool hovered: artistHover.containsMouse
                                    color: hovered || activeFocus ? dialog.ink : summaryTitle.color

                                    Accessible.role: Accessible.Link
                                    Accessible.name: text
                                    Accessible.onPressAction: editId()
                                    Accessible.description: "Edit Yandex artist ID"
                                    function editId() {
                                        artistRow.editing = true
                                        artistId.forceActiveFocus()
                                        artistId.selectAll()
                                    }
                                    Keys.onReturnPressed: editId()
                                    Keys.onEnterPressed: editId()
                                    Keys.onSpacePressed: editId()
                                    MouseArea {
                                        id: artistHover
                                        anchors.fill: parent
                                        hoverEnabled: true
                                        cursorShape: Qt.PointingHandCursor
                                        onClicked: artistName.editId()
                                    }
                                }
                                TextInput {
                                    id: artistId
                                    font: summaryTitle.font
                                    color: dialog.ink
                                    clip: true
                                    selectByMouse: true
                                    activeFocusOnTab: true
                                    objectName: "restoreArtistId" + artistRow.index
                                    anchors.fill: parent
                                    visible: artistRow.editing
                                    readonly property int artistIndex: artistRow.index
                                    text: String(dialog.restoreArtists[artistIndex].id)
                                    Accessible.name: "Yandex artist ID for " + dialog.restoreArtists[artistIndex].name
                                    inputMethodHints: Qt.ImhDigitsOnly
                                    onTextEdited: dialog.setRestoreArtistId(artistIndex, text)
                                    onActiveFocusChanged: if (!activeFocus) artistRow.editing = false
                                    onAccepted: {
                                        artistRow.editing = false
                                        artistName.forceActiveFocus(Qt.TabFocusReason)
                                    }
                                }
                            }
                        }
                    }
                    Caption {
                        visible: dialog.restoreMode && text.length > 0
                        text: dialog.artistIdError
                        Layout.fillWidth: true
                        wrapMode: Text.Wrap
                    }
                    Field {
                        id: restoreIdInput
                        objectName: "restoreTrackId"
                        visible: dialog.restoreMode
                        Accessible.name: "Yandex track ID"
                        placeholderText: "Yandex track ID"
                        inputMethodHints: Qt.ImhDigitsOnly
                        onTextChanged: dialog.reloadRestoreTrack()
                    }
                    Caption {
                        visible: !dialog.restoreMode
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
            Caption { text: "Artist" }
            Field { id: artistInput; objectName: "sendArtist"; placeholderText: "Artists, comma-separated"; Accessible.name: "Artist" }
            Caption { text: "Title" }
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
            Caption { text: dialog.restoreMode ? "MP3 URL · required" : "MP3 URL · optional" }
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
            SiteQuote {
                Layout.fillWidth: true
                text: dialog.restoreMode ? "An MP3 URL is required"
                                    : "Leave the URL empty to add only the list entry"
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
                MetadataToggle { id: editArtists; objectName: "editArtists"; text: "Override artists"; visible: !dialog.restoreMode; enabled: !dialog.busy }
                Field { id: metadataArtists; visible: !dialog.restoreMode; objectName: "metadataArtists"; enabled: editArtists.checked && !dialog.busy; Accessible.name: "Metadata artists with IDs"; placeholderText: dialog.artistsPlaceholder || dialog.originalArtist || "Artist ID: Name" }
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
        SiteQuote {
            Layout.fillWidth: true
            visible: text.length > 0
            objectName: "sendError"
            text: dialog.restoreMode && dialog.metadataError ? dialog.metadataError
                            : !dialog.uploadBackend ? "Upload backend is not configured"
                : dialog.uploadBackend.error === "Wait for the configured repository to be ready before sending" ? "" : dialog.uploadBackend.error
            Accessible.role: Accessible.AlertMessage
            Accessible.name: text
            textColor: "#ff9292"
            borderColor: textColor
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
            ActionButton { id: cancelButton; objectName: "cancelSendButton"; text: "Cancel"; enabled: !dialog.sending && !dialog.busy; onClicked: dialog.close() }
            ActionButton {
                objectName: "sendTrackButton"
                text: dialog.restoreMode ? "Restore" : "Send"
                loading: dialog.busy || dialog.sending
                accented: true
                enabled: dialog.canSend
                onClicked: {
                    dialog.submit()
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
        function onMetadataReady(request, id, metadata, error) {
            dialog.applyRestoreMetadata(request, id, metadata, error)
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

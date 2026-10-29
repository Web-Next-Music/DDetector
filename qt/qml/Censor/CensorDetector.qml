pragma ComponentBehavior: Bound
import QtQuick
import QtQuick.Controls
import QtQuick.Layouts
import QtQuick.Effects
import QtMultimedia

Rectangle {
    id: root
    property var bridge: null
    property var uploadBackend: null
    property bool approveSoundEnabled: false
        property real volume: 0.5
    property bool pageActive: true
    property var censorState: bridge ? bridge.censorState : ({})
    property var jobs: censorState.jobs || []
    readonly property string analysisStatus: "Analyzing - " + (job ? job.stage || "preparing" : "preparing")
    property string selectedId: ""
    property string candidateId: ""
    property var selectedDetail: bridge && bridge.censorSelectedJob !== undefined ? bridge.censorSelectedJob : ({})
    property var job: {
        // Legacy backends provide complete inline jobs and no paging envelope.
                if (censorState.jobs_total !== undefined && bridge && bridge.censorSelectJob !== undefined) return selectedDetail.id === selectedId ? selectedDetail : null;
        for (const j of jobs) if (j.id === selectedId) return j;
        return null;
    }
    // job.result may be the best candidate, not the candidate being reviewed.
    property var sourceResult: job && job.result ? job.result : ({})
    property var result: candidate ? candidate.result || ({}) : sourceResult
    property var comparison: candidate && candidate.result ? candidate.result.comparison || null : null
    property var waveformData: result.waveform && (result.waveform.samples || result.waveform.points || result.waveform).length ? result.waveform : sourceResult.waveform || []
    property real waveformDuration: Number(result.waveform && (result.waveform.samples || result.waveform.points || result.waveform).length ? result.duration_seconds : sourceResult.duration_seconds) || 0
    property var statistics: censorState.statistics || censorState.stats || ({})
    property var statisticKeys: {
        const keys = ["total", "pending", "running", "suitable_mp3", "review_required", "local_saved", "succeeded", "failed"];
        for (const key of Object.keys(statistics)) if (keys.indexOf(key) < 0) keys.push(key);
        return keys;
    }
    property var retryIds: []
    readonly property var hostWindow: Window.window
        property var normalPlayer: hostWindow ? hostWindow.player || null : null
    property var candidates: job && job.candidates ? job.candidates : []
    property var candidate: {
        for (const c of candidates) if (c.id === candidateId) return c;
        return null;
    }
    readonly property int candidateIndex: candidates.findIndex(c => c.id === candidateId)
    function switchCandidate(step) {
        if (!candidates.length) return;
        const next = originalUnavailable
                    ? (candidateIndex + step + candidates.length) % candidates.length
                    : (candidateIndex + 1 + step + candidates.length + 1) % (candidates.length + 1) - 1;
        const position = timelinePosition;
        const resume = playing;
        candidateId = next < 0 ? "" : candidates[next].id;

        seek(position);
        if (resume && playableSelection) { playing = true; playSelection(); }
    }
    function waveformTarget(fraction) {
        if (!isFinite(fraction) || !(waveformDuration > 0)) return 0;
        return Math.max(0, Math.min(1, fraction)) * waveformDuration * 1000;
    }
    function waveformSeek(fraction) {
        if (!playableSelection || !(waveformDuration > 0) || !isFinite(fraction)) return;
        seek(waveformTarget(fraction));
    }
    property var explicitTargetTrack: null
    readonly property bool originalUnavailable: (!!explicitTargetTrack && explicitTargetTrack.available === false)
            || !!(job && job.request && job.request.original_unavailable === true)
        readonly property bool restoreTarget: originalUnavailable
        readonly property var restoreMetadata: explicitTargetTrack || (job && job.request ? job.request : {})
            readonly property string restoreTrackId: String(explicitTargetTrack ? explicitTargetTrack.id : restoreMetadata.track_id || "")
        function selectUnavailableCandidate() {
            if (originalUnavailable && !candidates.some(c => c.id === candidateId))
                candidateId = candidates.length ? candidates[0].id : "";
        }
        onCandidatesChanged: selectUnavailableCandidate()
        onOriginalUnavailableChanged: { updateOriginalSource(); selectUnavailableCandidate(); }
    readonly property string playerTrackId: normalPlayer ? String(normalPlayer.trackId || "") : ""
    readonly property string currentTrackId: explicitTargetTrack ? explicitTargetTrack.id : playerTrackId
    readonly property string currentTitle: explicitTargetTrack ? explicitTargetTrack.title || "Original recording" : normalPlayer ? normalPlayer.title || "Select a track in the player" : job ? job.request.title || "Original recording" : "Select a track in the player"
    readonly property string currentArtist: explicitTargetTrack ? explicitTargetTrack.artist || "" : normalPlayer ? normalPlayer.artist || "" : job ? job.request.artist || "" : ""
    onPlayerTrackIdChanged: explicitTargetTrack = null
    onNormalPlayerChanged: explicitTargetTrack = null
    property url originalAudioUrl: ""
    property string playbackError: ""
    property url resolvedCandidateUrl: ""
    property int playbackRequestId: 0
    property bool resolvingPlayback: false
    Connections {
        target: root.bridge
        ignoreUnknownSignals: true
        function onCensorPlaybackResolved(requestId, jobId, candidateId, url, error) {
            if (requestId !== root.playbackRequestId || jobId !== root.selectedId || candidateId !== root.candidateId || !root.resolvingPlayback) return;
            root.resolvingPlayback = false;
            if (error || !url.toString()) {
                root.playbackError = error || "Unable to resolve candidate audio";
                root.playing = false;
                return;
            }
            root.resolvedCandidateUrl = url;
            if (root.playing && root.listenCandidate) replacement.play();
        }
    }
    property real pendingPosition: 0
    readonly property bool playableSelection: candidateId ? playableCandidate(candidate) : !originalUnavailable && originalAudioUrl.toString().length > 0
    readonly property real timelinePosition: listenCandidate ? Math.max(0, replacement.position + offsetMs) : original.position
    readonly property real timelineDuration: originalUnavailable ? replacement.duration : original.duration > 0 ? original.duration : waveformDuration * 1000
    function syncCurrentTrack() {
        if (!normalPlayer && !explicitTargetTrack) return;
        const matching = jobs.find(j => j.request && String(j.request.track_id) === currentTrackId);
        const next = matching ? matching.id : "";
        if (selectedId !== next) selectedId = next;
        updateOriginalSource();
    }
    function updateOriginalSource() {
        if (originalUnavailable) { originalAudioUrl = ""; return; }
        const url = normalPlayer && playerTrackId === currentTrackId && normalPlayer.streamUrl ? normalPlayer.streamUrl.toString() : "";
        if (url) originalAudioUrl = url;
        else if (job && job.request && (!normalPlayer || explicitTargetTrack || String(job.request.track_id) === currentTrackId) && job.request.original_url) originalAudioUrl = job.request.original_url;
        else originalAudioUrl = "";
    }
    onCurrentTrackIdChanged: {
        stopPlayers(); originalAudioUrl = ""; selectedId = ""; syncCurrentTrack();
    }
    onJobsChanged: syncCurrentTrack()
    property string ipCountry: ""
    property string ipCheckStatus: ""
    readonly property string ipCountryName: ipCountry && bridge && bridge.countryName !== undefined
        ? bridge.countryName(ipCountry) : "Unknown country"
    property var ipRequest: null
    function checkIpCountry() {
        if (ipRequest) return;
        ipCheckStatus = "Checking IP country...";
        ipCountry = "";
        const request = new XMLHttpRequest();
        ipRequest = request;
        request.onreadystatechange = function() {
            if (request.readyState !== XMLHttpRequest.DONE || root.ipRequest !== request) return;
            ipCheckTimeout.stop();
            root.ipRequest = null;
            let country = "";
            if (request.status === 200) {
                try {
                    const data = JSON.parse(request.responseText);
                    if (data && typeof data.cc === "string" && /^[A-Z]{2}$/.test(data.cc)) country = data.cc;
                } catch (error) {
                    country = "";
                }
            }
            root.ipCountry = country;
            root.ipCheckStatus = country ? "" : "Unable to determine your IP country. Check your connection.";
        };
        request.open("GET", "https://ident.me/json");
        ipCheckTimeout.start();
        request.send();
    }
    Timer {
        id: ipCheckTimeout
        interval: 10000
        onTriggered: {
            const request = root.ipRequest;
            root.ipRequest = null;
            if (request) request.abort();
            root.ipCheckStatus = "Unable to determine your IP country. The request timed out.";
        }
    }
    Component.onCompleted: { syncCurrentTrack(); if (pageActive) checkIpCountry(); }
    Component.onDestruction: { if (ipRequest) { ipRequest.onreadystatechange = null; ipRequest.abort(); } }
    Connections {
        target: root.normalPlayer
        ignoreUnknownSignals: true
        function onStateChanged() { root.syncCurrentTrack(); }
    }
    function pauseMainPlayer() {
        if (!normalPlayer || !normalPlayer.playing) return;
        if (normalPlayer.pause !== undefined) normalPlayer.pause();
        else normalPlayer.toggle();
    }
    function playSelection() {
        if (!playableSelection) { playing = false; return; }
        playbackError = "";
        if (listenCandidate) {
            original.pause();
            if (censorState.native === true && bridge && bridge.censorResolvePlayback !== undefined) {
                replacement.stop();
                resolvingPlayback = true;
                bridge.censorResolvePlayback(selectedId, candidateId, ++playbackRequestId);
            } else replacement.play();
        }
        else { replacement.pause(); original.play(); }
    }
    function togglePlayback() {
        if (!playableSelection) return;
        if (playing) { playing = false; resolvingPlayback = false; ++playbackRequestId; original.pause(); replacement.pause(); }
        else { playing = true; playSelection(); }
    }
    property url candidateAudioUrl: playableCandidate(candidate)
        ? (censorState.native === true && bridge && bridge.censorResolvePlayback !== undefined
            ? resolvedCandidateUrl : resolvedCandidateUrl.toString() || candidate.url) : ""
    onCandidateAudioUrlChanged: if (!candidateAudioUrl.toString()) replacement.stop()
    property bool configured: censorState.configured === true
    property bool busy: bridge ? bridge.censorBusy : false
    property bool backendBusy: censorState.busy === true
    property bool playing: false
    property bool clockTime: false
    readonly property bool listenCandidate: candidateId.length > 0 && playableCandidate(candidate)
    property real offsetMs: !originalUnavailable && comparison && comparison.comparable ? Number(comparison.offset_seconds || 0) * 1000 : 0
    property var retryableErrors: jobs.filter(j => (j.status === "failed" || !!j.error)
        && !j.publication_key && ["running", "publishing", "rolling_back", "succeeded"].indexOf(j.status) < 0)
    color: "#09090b"
    onSelectedIdChanged: { candidateId = ""; selectUnavailableCandidate(); stopPlayers(); approveDialog.close(); if (bridge && bridge.censorSelectJob !== undefined) bridge.censorSelectJob(selectedId); }
    onCandidateIdChanged: { playing = false; resolvingPlayback = false; ++playbackRequestId; resolvedCandidateUrl = ""; playbackError = ""; original.pause(); replacement.stop(); approveDialog.close(); }
    onJobChanged: { updateOriginalSource(); if (!job) approveDialog.close(); }
    onCandidateChanged: if (!candidate) { replacement.stop(); approveDialog.close(); }
    onConfiguredChanged: if (!configured) { retryIds = []; stopPlayers(); }
    onVisibleChanged: if (!visible) stopPlayers()
    onPageActiveChanged: { if (!pageActive) stopPlayers(); else checkIpCountry(); }
    function stopPlayers() { playing = false; resolvingPlayback = false; ++playbackRequestId; original.stop(); replacement.stop(); }
    function audioCandidate(c) {
        if (!c || c.full_audio !== true || c.downloadable !== true || !/^https:\/\/[^/?#@\s]+(?:[/?][^#\s]*)?$/.test(c.url || "")) return false;
        const metadata = c.metadata || {};
        const kind = String(metadata.kind || "").trim().toLowerCase();
        return kind !== "page" && kind !== "preview" && !String(metadata.content_type || "").trim().toLowerCase().startsWith("text/");
    }
    function webAudioCandidate(c) {
        if (!c || c.downloadable !== true || !/^https:\/\/[^/?#@\s]+(?:[/?][^#\s]*)?$/.test(c.url || "")) return false;
        const metadata = c.metadata || {};
        return String(c.id || "").startsWith("candidate:web:") && c.provider === "direct_web"
            && String(metadata.kind || "").trim().toLowerCase() === "audio"
            && String(metadata.content_type || "").trim().toLowerCase() === "audio/mpeg";
    }
    function playableCandidate(c) {
        return audioCandidate(c) || (webAudioCandidate(c) && censorState.native === true
            && !!bridge && bridge.censorResolvePlayback !== undefined);
    }
    function candidateType(c) {
        const metadata = c.metadata || {};
        const kind = String(metadata.kind || "").trim().toLowerCase();
        if (kind === "page" || String(metadata.content_type || "").trim().toLowerCase().startsWith("text/")) return "Web page (review only)";
        if (kind === "preview") return "Preview (review only)";
        if (!c.full_audio && webAudioCandidate(c)) return "Downloadable MP3 (manual confirmation required)";
        if (!c.full_audio) return "Not full audio (review only)";
        if (!c.downloadable) return "Full stream (not downloadable)";
        return audioCandidate(c) ? "Full downloadable audio" : "Unsupported audio source";
    }
    function candidateDetails(c) {
        return candidateType(c) + " - provider: " + (c.provider || "not supplied")
            + " - duration: " + (Number(c.duration_seconds) > 0 ? seconds(c.duration_seconds) : "not supplied");
    }
    function candidateMetadata(c) {
        const metadata = c.metadata || {};
        return Object.keys(metadata).sort().map(key => key + ": " + metadata[key]).join("\n");
    }
    function approvalEvidenceReason() {
        if (!candidate) return "Select a candidate to review its own comparison";
        if (!audioCandidate(candidate)) return webAudioCandidate(candidate)
            ? "Web audio is listen-only: completeness and rights are not confirmed. Approval requires full downloadable audio and verified comparison evidence"
            : "Only full downloadable audio can be approved. Pages, previews, and non-downloadable streams remain review-only";
        if (candidate.error) return candidate.error;
        const r = candidate.result;
        const c = comparison;
        if (!r || !c) return "The selected candidate has no comparison result. Analyze or search again";
        if (!(Number(r.duration_seconds) > 0) || !c.comparable || c.confidence_class !== "strong_match"
            || !(c.confidence >= 0.75) || !(c.spectral_similarity >= 0.93)
            || !(c.spectral_match_fraction >= 0.8) || !(c.correlation >= 0.5)
            || !isFinite(c.offset_seconds) || !isFinite(c.overlap_seconds)
            || !(c.overlap_seconds >= Math.max(6, 0.9 * r.duration_seconds))
            || (c.fingerprint_similarity !== undefined && c.fingerprint_similarity !== null && !(c.fingerprint_similarity >= 0.85)))
            return "The selected candidate does not meet the server's strong full-recording match requirements";
        if (c.confidence < Number((censorState.settings || {}).min_confidence || 0.75)) return "The selected candidate's comparison confidence is below the configured minimum";
        return "";
    }
    function analyzeTrack(track) {
        if (!track || typeof track !== "object") return "Select a Yandex library track first";
        const id = String(track.id || "");
        const reason = disabledReason("check_track", id);
        if (reason) return reason;
        explicitTargetTrack = Object.assign({}, track, { id: id, title: String(track.title || ""), artist: String(track.artist || "") });
        syncCurrentTrack();
        return command("check_track");
    }
    function currentTrackError(trackId) {
        const id = trackId === undefined ? currentTrackId : trackId;
        if (!/^[0-9]+$/.test(id) || !(Number(id) > 0)) return "Select a current Yandex track first";
        if (!Number.isSafeInteger(Number(id))) return "Track ID exceeds JavaScript's safe integer range; manual analysis is unavailable";
        return "";
    }
    function requestOrigin(request) {
        return request && request.origin === "manual" ? "Manual Yandex analysis (not a public request)"
            : request && request.origin === "reported_tracks" ? "Reported public request" : "Request";
    }
    function disabledReason(action, trackId) {
        if (action === "restore") {
            if (!restoreTarget) return "Select a track confirmed unavailable by Yandex";
            if (!uploadBackend) return "Upload backend is not configured";
            if (uploadBackend.busy) return "Wait for the current Send operation to finish";
            return currentTrackError(restoreTrackId);
        }
        if (action === "send_candidate") {
            if (!uploadBackend) return "Upload backend is not configured";
            if (censorState.native === true && censorState.native_repository_ready !== true) return "Configure and validate the Git repository first";
            if (uploadBackend.busy) return "Wait for the current Send operation to finish";
            if (!candidate) return "Select a candidate first";
            if (!playableCandidate(candidate)) return "Select a downloadable audio candidate";
            if (!job || !job.request || !/^[0-9]+$/.test(String(job.request.track_id || ""))) return "The original Yandex track is unavailable";
            return "";
        }
        if (action === "check_track") {
            if (!bridge || typeof bridge.censorCommand !== "function" || censorState.native !== true) return "Manual current-track analysis requires the native backend";
            if (busy || backendBusy || censorState.running || retryIds.length) return "Wait for current analysis or commands to finish";
            return currentTrackError(trackId);
        }
        if (!bridge || !configured) return "Censor Detector is not configured or authorized";
        if (action === "settings") return "";
        if (busy) return "Waiting for the current HTTP command";
        if (retryIds.length && action !== "pause" && action !== "stop") return "Retrying errored jobs one at a time";
        if (backendBusy && action !== "pause" && action !== "stop") return "A server command is active; pause or stop it first";

        if (["fetch", "start", "resume", "pause", "stop", "retry_errors"].indexOf(action) >= 0) {
            if (action === "retry_errors" && !retryableErrors.length) return "No retryable errors on this page. Publication errors require approving the same candidate; rollback errors require rollback";
            return "";
        }
        if (!job) return "Select a request first";
        if (["running", "publishing", "rolling_back"].indexOf(job.status) >= 0) return "This request is active (" + (job.stage || job.status) + "). Pause or stop first";
        if (action === "rollback") {
            if (!censorState.rollback_supported) return "This backend does not support rollback";
            return (job.status === "succeeded" || job.status === "local_saved") && job.replacement ? "" : "Rollback requires a durably saved replacement";
        }
        if (action === "approve") {
            if (censorState.native === true && censorState.native_repository_ready !== true) return "Connect a repository in the Git workspace before approving a replacement";
            if (job.status !== "review_required") return "Approval requires a request awaiting review";
            if (job.publication_key && candidateId !== job.selected_candidate) return "Publication is awaiting reconciliation. Approve the same previously selected candidate";
            return approvalEvidenceReason();
        }
        if (job.publication_key || job.status === "succeeded" || job.status === "local_saved") return "Published or unacknowledged publication: use the same candidate's approval to reconcile, or rollback a published replacement";
        return "";
    }
    function command(action, extra) {
        const reason = disabledReason(action);
        if (reason) return reason;
        if (action === "pause" || action === "stop") retryIds = [];
        let args = {};
        if (action === "check_track") args.track_id = currentTrackId;
        if (["retry", "analyze", "search", "approve", "reject", "defer", "rollback"].indexOf(action) >= 0 && job) {
            args.id = job.id; args.request_id = job.request.id; args.track_id = job.request.track_id;
        }
        if (action === "approve" && candidate) args.candidate_id = candidate.id;
        if (extra) for (const key in extra) args[key] = extra[key];
        bridge.censorCommand(action, args);
        return "";
    }
    function retryErrors() {
        if (disabledReason("retry_errors")) return;
        retryIds = retryableErrors.map(j => j.id);
    }
    function candidatePosition(ms) { return Math.max(0, ms - offsetMs); }
    function seek(ms) {
        const position = Math.max(0, Math.min(timelineDuration > 0 ? timelineDuration : ms, ms));
        pendingPosition = position;
        if (!originalUnavailable && original.duration > 0) original.position = position;
        if (replacement.duration > 0) replacement.position = Math.min(replacement.duration, candidatePosition(position));
    }
    function seconds(value) { return Number(value || 0).toFixed(2) + " s"; }
    function clock(value) {
        const total = Math.max(0, Math.floor(Number(value) || 0));
        const hours = Math.floor(total / 3600);
        const minutes = Math.floor(total % 3600 / 60);
        const seconds = total % 60;
        return String(hours).padStart(2, "0") + ":" + String(minutes).padStart(2, "0") + ":" + String(seconds).padStart(2, "0");
    }
        function statisticLabel(key) {
            const labels = {total: "Total", pending: "Awaiting analysis", running: "Processing", suitable_mp3: "Suitable MP3s", review_required: "Review required", local_saved: "Saved locally", succeeded: "Completed", failed: "Errors"};
            return labels[key] || key.replace(/_/g, " ");
        }
        function actionLabel(key) {
            const labels = {fetch: "Fetch requests", start: "Process queue", pause: "Pause", resume: "Resume", stop: "Stop", retry: "Retry", analyze: "Reanalyze", reject: "Reject", defer: "Defer", rollback: "Roll back replacement"};
            return labels[key] || key;
        }
    Timer {
        objectName: "censorRetryTimer"
        interval: 150; repeat: true; running: root.pageActive && root.retryIds.length > 0
        onTriggered: {
            if (root.busy || root.backendBusy || !root.configured) return;
            const id = root.retryIds[0];
            root.retryIds = root.retryIds.slice(1);
            if (root.retryableErrors.some(j => j.id === id)) root.bridge.censorCommand("retry", {id: id});
        }
    }
    MediaPlayer {
        id: original
        objectName: "censorOriginalPlayer"
        source: root.originalAudioUrl
        audioOutput: AudioOutput { volume: root.volume; muted: root.listenCandidate }
        onDurationChanged: if (duration > 0) position = Math.min(duration, root.pendingPosition)
        onPlaybackStateChanged: if (playbackState === MediaPlayer.PlayingState) root.pauseMainPlayer()
        onMediaStatusChanged: if (mediaStatus === MediaPlayer.EndOfMedia && !root.listenCandidate) root.playing = false
        onErrorOccurred: function(error, errorString) { if (!root.originalUnavailable && !root.listenCandidate) { root.playbackError = errorString; root.playing = false; } }
    }
    MediaPlayer {
        id: replacement
        objectName: "censorCandidatePlayer"
        source: root.candidateAudioUrl
        audioOutput: AudioOutput { volume: root.volume; muted: !root.listenCandidate }
        onDurationChanged: if (duration > 0) position = Math.min(duration, root.candidatePosition(root.pendingPosition))
        onPlaybackStateChanged: if (playbackState === MediaPlayer.PlayingState) root.pauseMainPlayer()
        onMediaStatusChanged: if (mediaStatus === MediaPlayer.EndOfMedia && root.listenCandidate && !root.resolvingPlayback) root.playing = false
        onErrorOccurred: function(error, errorString) { if (root.listenCandidate) { root.playbackError = errorString; root.playing = false; } }
    }
    SiteDialog {
        id: searchSitesDialog
        objectName: "censorSearchSitesDialog"
        title: "Search sites"
        width: Math.min(520, parent ? parent.width - 32 : 520)
        height: Math.min(600, parent ? parent.height - 32 : 600)
        contentItem: ScrollView {
            id: searchSitesScroll
            clip: true
            contentWidth: searchSitesScroll.availableWidth
            ColumnLayout {
                width: searchSitesScroll.availableWidth
                spacing: 8
                SiteCodeBlock {
                    objectName: "censorSearchSitesCodeBlock"
                    Layout.fillWidth: true
                    visible: (root.censorState.search_sites || []).length > 0
                    text: (root.censorState.search_sites || []).join("\n")
                }
                Label {
                    Layout.fillWidth: true
                    visible: !(root.censorState.search_sites || []).length
                    text: "This search engine has not provided a list of sites."
                    color: "#999999"
                    wrapMode: Text.Wrap
                }
            }
        }
    }
    ScrollView {
        id: detectorScroll
        objectName: "censorScrollViewport"
        anchors.fill: parent
        anchors.margins: 10
        contentWidth: availableWidth
        ColumnLayout {
            width: detectorScroll.availableWidth
            spacing: 10

            SiteQuote {
                objectName: "censorRuntimeError"
                Layout.fillWidth: true
                visible: text.length > 0
                text: root.censorState.error || ""
                textFormat: Text.PlainText
                textColor: "#dc5050"
                borderColor: textColor
                Accessible.role: Accessible.AlertMessage
            }
            SiteQuote {
                objectName: "censorVpnWarning"
                Layout.fillWidth: true
                visible: root.ipCountry.length > 0 && root.ipCountry !== "RU"
                borderColor: "#ef4444"
                textColor: "#fca5a5"
                textFormat: Text.RichText
                linkColor: textColor
                keyboardLink: "search-sites"
                text: ("Search may not work with a VPN enabled if your IP country is not Russia. Current country: "
                    + root.ipCountryName + ". "
                    + '<a href="search-sites">Search sites</a>.')
                onLinkActivated: searchSitesDialog.open()
            }




                SiteSettingsCard {
                    objectName: "censorPlaybackCard"
                    Layout.fillWidth: true
                    compact: true
                    RowLayout {
                        Layout.fillWidth: true; spacing: 10
                        SiteButton {
                            objectName: "censorPreviousCandidate"; glyph: "chevron-left"; iconOnly: true
                            Accessible.name: "Previous candidate"; enabled: root.candidates.length > 0
                            SiteToolTip { text: "Previous candidate" }
                            onClicked: root.switchCandidate(-1)
                        }
                        ColumnLayout {
                            Layout.fillWidth: true; spacing: 0
                            Label { Layout.fillWidth: true; text: root.candidate ? root.candidate.title || root.candidate.id : root.currentTitle; horizontalAlignment: Text.AlignHCenter; color: "#eeeeef"; font.pixelSize: 16; font.bold: true; elide: Text.ElideRight; textFormat: Text.PlainText }
                            Label { Layout.fillWidth: true; visible: !!text; text: root.candidate ? root.candidate.artist || "" : root.currentArtist; horizontalAlignment: Text.AlignHCenter; color: "#999999"; elide: Text.ElideRight; textFormat: Text.PlainText }
                        }
                        SiteButton {
                            objectName: "censorNextCandidate"; glyph: "chevron-right"; iconOnly: true
                            Accessible.name: "Next candidate"; enabled: root.candidates.length > 0
                            SiteToolTip { text: "Next candidate" }
                            onClicked: root.switchCandidate(1)
                        }
                    }
                    Item {
                        Layout.fillWidth: true; Layout.preferredHeight: visible ? 120 : 0
                        visible: waveform.samples.length > 0
                        Canvas {
                            id: waveform
                            anchors.fill: parent
                            objectName: "censorWaveform"
                            property var samples: root.waveformData.samples || root.waveformData.points || root.waveformData
                            property real duration: root.waveformDuration
                            property real playhead: root.timelinePosition / 1000
                            onDurationChanged: requestPaint()
                            onSamplesChanged: requestPaint()
                            onWidthChanged: requestPaint()
                            onHeightChanged: requestPaint()
                            onPaint: {
                                const ctx = getContext("2d"); ctx.clearRect(0, 0, width, height);
                                ctx.fillStyle = "#0c0c0e"; ctx.fillRect(0, 0, width, height);
                                ctx.strokeStyle = "#242428"; ctx.beginPath(); ctx.moveTo(0, height / 2); ctx.lineTo(width, height / 2); ctx.stroke();
                                ctx.lineWidth = 1;
                                for (let i = 0; i < samples.length; ++i) {
                                    const point = samples[i];
                                    const nativePoint = typeof point === "object" && point !== null;
                                    const low = nativePoint ? Math.max(-1, Number(point.min)) : -Math.min(1, Math.abs(Number(point)));
                                    const high = nativePoint ? Math.min(1, Number(point.max)) : -low;
                                    if (!isFinite(low) || !isFinite(high)) continue;
                                    const time = nativePoint ? (Number(point.start_seconds) + Number(point.end_seconds)) / 2 : i / samples.length * duration;
                                    const x = nativePoint && duration > 0 ? time / duration * width : i * width / samples.length;
                                    ctx.strokeStyle = "#2bfef5";
                                    ctx.beginPath(); ctx.moveTo(x, height * (1 - high) / 2); ctx.lineTo(x, height * (1 - low) / 2); ctx.stroke();
                                }
                            }
                        }
                        Rectangle {
                            width: 2; height: parent.height; color: "#D50811"
                            visible: waveform.duration > 0
                            x: Math.max(0, Math.min(parent.width - width, waveform.playhead / waveform.duration * parent.width))
                        }
                        MouseArea {
                            objectName: "censorWaveformSeek"
                            anchors.fill: parent; enabled: root.playableSelection && waveform.duration > 0
                            cursorShape: enabled ? Qt.PointingHandCursor : Qt.ArrowCursor
                            onClicked: function(mouse) { root.waveformSeek(mouse.x / width); }
                        }
                    }
                    Item {
                        objectName: "censorEmptyWaveform"
                        Layout.fillWidth: true; Layout.preferredHeight: 100
                        visible: !waveform.samples.length
                    }
                    Item {
                        Layout.fillWidth: true
                        implicitHeight: Math.max(playbackButton.implicitHeight, playbackMessage.implicitHeight, playbackTime.implicitHeight)
                        SiteButton {
                            id: playbackButton
                            objectName: "censorPlaybackButton"
                            anchors.left: parent.left
                            anchors.verticalCenter: parent.verticalCenter
                            glyph: root.playing ? "pause" : "play"
                            text: root.playing ? "Pause" : "Play"; enabled: root.playableSelection
                            loading: root.resolvingPlayback
                            onClicked: root.togglePlayback()
                        }
                        Label {
                            id: playbackMessage
                            objectName: "censorPlaybackMessage"
                            visible: !candidateStatus.visible
                            anchors.horizontalCenter: parent.horizontalCenter
                            anchors.verticalCenter: parent.verticalCenter
                            width: Math.max(0, parent.width - 2 * Math.max(playbackButton.width, playbackTime.width) - 24)
                            text: root.playbackError || (root.candidate ? "Candidate " + (root.candidateIndex + 1) + " / " + root.candidates.length : "")
                            textFormat: Text.PlainText
                            horizontalAlignment: Text.AlignHCenter
                            elide: Text.ElideRight
                            color: root.playbackError ? "#f4c779" : "#999999"
                            SiteToolTip { text: root.playbackError; visible: playbackMessageHover.hovered && !!root.playbackError }
                            HoverHandler { id: playbackMessageHover }
                        }
                        Label {
                            id: candidateStatus
                            objectName: "censorCandidateStatus"
                            anchors.fill: playbackMessage
                            visible: root.originalUnavailable && !root.candidates.length
                            text: !root.job || root.job.status === "running" || root.job.status === "pending"
                                || root.censorState.running === true ? root.analysisStatus : "No candidates found"
                            horizontalAlignment: Text.AlignHCenter
                            verticalAlignment: Text.AlignVCenter
                            elide: Text.ElideRight
                            color: "#999999"
                            textFormat: Text.PlainText
                            Accessible.role: Accessible.StaticText
                        }
                        Label {
                            id: playbackTime
                            objectName: "censorPlaybackTime"
                            anchors.right: parent.right
                            anchors.verticalCenter: parent.verticalCenter
                            text: root.clockTime
                                ? root.clock(root.timelinePosition / 1000) + " / " + root.clock(root.timelineDuration / 1000)
                                : root.seconds(root.timelinePosition / 1000) + " / " + root.seconds(root.timelineDuration / 1000)
                            color: "#999999"
                            Accessible.role: Accessible.Button
                            Accessible.name: "Toggle playback time format"
                            MouseArea {
                                anchors.fill: parent
                                cursorShape: Qt.PointingHandCursor
                                onClicked: root.clockTime = !root.clockTime
                            }
                            SiteToolTip { text: root.clockTime ? "Show time in seconds" : "Show hours, minutes and seconds" }
                        }
                    }

                }
            RowLayout {
                Layout.fillWidth: true; spacing: 6
                SiteButton {
                    text: root.job ? "Analyze again" : "Analyze track"
                    glyph: "search"
                    loading: root.censorState.running === true || (!!root.job && root.job.status === "running")
                    enabled: !root.disabledReason("check_track")
                    onClicked: root.command("check_track")
                    SiteToolTip { text: root.disabledReason("check_track") }
                }
                SiteButton { text: "Stop"; glyph: "square"; visible: root.censorState.running === true; enabled: !root.disabledReason("stop"); onClicked: root.command("stop") }


                SiteButton { text: "Undo replacement"; glyph: "undo-2"; visible: !!root.job && !!root.job.replacement; enabled: !root.disabledReason("rollback"); onClicked: root.command("rollback") }
                Item { Layout.fillWidth: true }
                Item {
                    implicitWidth: approvalButton.implicitWidth
                    implicitHeight: approvalButton.implicitHeight
                    HoverHandler { id: approvalHover }
                    SiteButton {
                        id: approvalButton
                        anchors.fill: parent
                        objectName: "censorReviewApproval"
                        text: root.restoreTarget ? "Restore" : "Approve"
                        glyph: root.restoreTarget ? "undo-2" : "check"
                        accented: true
                        accentColor: root.restoreTarget ? "#3b82f6" : "#2bfef5"
                        accentedColor: Qt.rgba(accentColor.r, accentColor.g, accentColor.b, 0.08)
                        accentBorder: Qt.rgba(accentColor.r, accentColor.g, accentColor.b, 0.2)
                        enabled: !root.disabledReason(root.restoreTarget ? "restore" : "send_candidate")
                        onClicked: {
                            if (root.restoreTarget) {
                                const track = root.restoreMetadata;
                                const selected = root.candidate;
                                const metadata = selected ? selected.metadata || ({}) : ({});
                                const mp3Url = root.playableCandidate(selected)
                                    ? root.resolvedCandidateUrl.toString() || selected.url || "" : "";
                                approveDialog.openRestore(root.restoreTrackId,
                                    track.title || (selected ? selected.title || "" : ""),
                                    track.artist || (selected ? selected.artist || "" : ""),
                                    track.cover || metadata.coverUri || metadata.cover || "", track, mp3Url);
                                return;
                            }
                            const request = root.job.request;
                            const cover = root.normalPlayer && String(root.normalPlayer.trackId || "") === String(request.track_id)
                                && root.normalPlayer.cover ? "image://trackcovers/" + encodeURIComponent(root.normalPlayer.cover) : "";
                            approveDialog.openCandidate(request.track_id, request.title || root.currentTitle,
                                                        request.artist || root.currentArtist, cover,
                                                        root.candidate.url,
                                                        root.normalPlayer && String(root.normalPlayer.trackId || "") === String(request.track_id)
                                                            ? root.normalPlayer.metadata : ({}));
                        }
                    }
                    SiteToolTip {
                        visible: approvalHover.hovered || approvalButton.activeFocus
                        text: root.disabledReason(root.restoreTarget ? "restore" : "send_candidate")
                                                    || (root.restoreTarget ? "Restore this unavailable track" : "Review and send this replacement")
                    }
                }
            }
            Rectangle {
                id: analysisCard
                objectName: "censorAnalysisProgress"
                Layout.fillWidth: true
                implicitHeight: analysisSections.implicitHeight + 16
                radius: 16
                color: "#0c0c0e"
                border.color: "#242426"
                border.pixelAligned: false
                visible: !!root.job && ["running", "review_required", "error"].indexOf(root.job.status) >= 0
                readonly property var steps: [
                    {stage: "analysis", label: "Audio analysis", glyph: "music-standard"},
                    {stage: "search", label: "Candidate search", glyph: "search"}
                ]
                readonly property int activeStep: root.job ? ["analysis", "search", "comparison", "review"].indexOf(root.job.stage) : -1
                ColumnLayout {
                    id: analysisSections
                    anchors.fill: parent
                    anchors.margins: 8
                    spacing: 8
                    DialogHeader {
                        Layout.fillWidth: true
                        Layout.leftMargin: -8; Layout.rightMargin: -8
                        Layout.topMargin: -8
                        Layout.bottomMargin: -8
                        title: root.analysisStatus
                        icon: "qrc:/assets/lucide-shield-check.svg"
                        closeVisible: false
                    }
                Repeater {
                    model: analysisCard.steps
                    delegate: SiteSettingsCard {
                        id: stepRow
                        visible: modelData.stage !== "analysis" || !root.originalUnavailable
                        required property var modelData
                        required property int index
                        Layout.fillWidth: true
                        compact: false
                        readonly property bool complete: analysisCard.activeStep > index
                        readonly property bool active: analysisCard.activeStep === index
                        readonly property real progress: {
                            const values = root.job ? root.job.stage_progress || {} : {};
                            const measured = values[modelData.stage];
                            return measured !== undefined && isFinite(Number(measured))
                                ? Math.max(0, Math.min(1, Number(measured))) : complete ? 1 : 0;
                        }
                        RowLayout {
                            Layout.fillWidth: true
                            spacing: 6
                            Image {
                                Layout.preferredWidth: 14
                                Layout.preferredHeight: 14
                                source: "qrc:/assets/lucide-" + stepRow.modelData.glyph + ".svg"
                                sourceSize: Qt.size(14, 14)
                                layer.enabled: true
                                layer.effect: MultiEffect {
                                    brightness: 1
                                    colorization: 1
                                    colorizationColor: stepRow.complete || stepRow.active ? "#eeeeef" : "#999999"
                                }
                                Accessible.ignored: true
                            }
                            Label {
                                text: stepRow.modelData.label
                                color: stepRow.complete || stepRow.active ? "#eeeeef" : "#999999"
                            }
                            Label {
                                Layout.maximumWidth: stepRow.width * 0.4
                                visible: stepRow.modelData.stage === "search" && stepRow.active && !!text
                                elide: Text.ElideMiddle
                                text: root.job ? root.job.search_site || "" : ""
                                color: "#999999"
                            }
                            Item { Layout.fillWidth: true }
                            Label {
                                text: stepRow.complete ? "Done" : stepRow.active ? (root.job && root.job.status === "running" ? "In progress - " + Math.floor(stepRow.progress * 100) + "%" : "Stopped - " + Math.floor(stepRow.progress * 100) + "%") : "Pending"
                                color: stepRow.complete ? "#2bfef5" : "#999999"
                            }
                        }
                        ProgressBar {
                            id: stepProgress
                            objectName: "censorStepProgress" + stepRow.index
                            Layout.fillWidth: true
                            from: 0; to: 1
                            value: stepRow.progress
                            indeterminate: false
                            Accessible.name: stepRow.modelData.label
                            padding: 0
                            implicitHeight: 10
                            background: Rectangle {
                                implicitHeight: 10
                                radius: height / 2
                                color: "#242428"
                                border.color: "#303034"
                            }
                            contentItem: Item {
                                clip: true
                                Rectangle {
                                    width: parent.width * stepProgress.visualPosition
                                    Behavior on width { NumberAnimation { duration: 350; easing.type: Easing.OutCubic } }
                                    height: parent.height
                                    radius: height / 2
                                    color: "#2bfef5"

                                }
                            }
                        }
                    }
                }
                }
            }

            CensorSettingsPanel {
                objectName: "censorSettingsPanel"
                Layout.fillWidth: true
                bridge: root.bridge
                unavailableReason: root.disabledReason("settings")
            }
        }
    }
    ScrollEdgeShadow {
        objectName: "censorScrollEdgeShadow"
        anchors.fill: detectorScroll
        flickable: detectorScroll.contentItem as Flickable
        shadowColor: "#0c0c0e"
    }
    SendDialog {
        id: approveDialog
        uploadBackend: root.uploadBackend
        approveSoundEnabled: root.approveSoundEnabled
                volume: root.volume
    }

}

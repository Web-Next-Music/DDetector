import QtQuick
import QtTest
import "qrc:/" as UI

Item {
    id: scene
    width: 800
    height: 600

    QtObject {
        id: backend
        property var censorState: ({})
        property bool censorBusy: false
        property var calls: []
        function censorCommand(action, args) {
            calls = calls.concat([{action: action, args: args}]);
        }
    }
    QtObject {
        id: player
        property string trackId: "99"
        property string title: "Player title"
        property string artist: "Player artist"
        property url streamUrl: ""
        property bool playing: false
        signal stateChanged()
    }
    QtObject {
        id: fakeUploader
        property bool busy: false
        property string error: ""
        property string status: ""
        property string metadataTrackId: ""
        signal completed(string action)
        signal stateChanged()
        signal metadataReady(int request, string id, var metadata, string error)
        function requestMetadata(id) { Qt.callLater(function() { metadataReady(1, id, {artists: [{id: "42", name: "Artist"}]}, "") }); return 1 }
        signal durationReady(int request, double durationMs)
        function trackMetadata(id) { metadataTrackId = id; return {}; }
        property var extractionRequests: []
        function requestDuration(id, url) {
            extractionRequests = extractionRequests.concat([{id: id, url: url}]);
            return extractionRequests.length;
        }
    }
    Component {
        id: detectorFactory
        UI.CensorDetector {
            width: 800
            height: 600
            pageActive: false
            normalPlayer: null
            bridge: backend
            uploadBackend: fakeUploader
        }
    }

    TestCase {
        name: "TrackAnalysis"
        when: windowShown

        function init() {
            fakeUploader.busy = false;
            fakeUploader.metadataTrackId = "";
            fakeUploader.extractionRequests = [];
            backend.censorBusy = false;
            backend.calls = [];
            backend.censorState = {native: true, configured: false, jobs: []};
            player.trackId = "99";
        }
        function detector() {
            const item = createTemporaryObject(detectorFactory, scene);
            verify(item !== null);
            return item;
        }
        function assertDispatch(id) {
            compare(backend.calls.length, 1);
            compare(backend.calls[0].action, "check_track");
            compare(backend.calls[0].args, {track_id: id});
        }
        function test_dispatchWithoutPlayer() {
            const item = detector();
            compare(item.normalPlayer, null);
            compare(item.analyzeTrack({id: "123", title: "Library title", artist: "Library artist"}), "");
            assertDispatch("123");
            compare(item.currentTrackId, "123");
            compare(item.currentTitle, "Library title");
            compare(item.currentArtist, "Library artist");
        }
        function test_jobsRetainExplicitTarget() {
            const item = detector();
            item.normalPlayer = player;
            compare(item.analyzeTrack({id: "123", title: "Library title", artist: "Library artist"}), "");
            const request = {id: "request-123", track_id: "123", title: "Job title", artist: "Job artist"};
            backend.censorState = {native: true, jobs: [
                {id: "unrelated", request: {track_id: "99"}},
                {id: "analysis", request: request, status: "running"}
            ]};
            compare(item.selectedId, "analysis");
            backend.censorState = {native: true, jobs: [
                {id: "analysis", request: request, status: "review_required", result: {duration_seconds: 42}}
            ]};
            player.stateChanged();
            compare(item.currentTrackId, "123");
            compare(item.currentTitle, "Library title");
            compare(item.currentArtist, "Library artist");
            compare(item.selectedId, "analysis");
            compare(item.job.status, "review_required");
            compare(item.result.duration_seconds, 42);
            assertDispatch("123");
        }
        function test_invalidIdDoesNotChangeTarget_data() {
            return [
                {tag: "missing", track: null},
                {tag: "empty", track: {}},
                {tag: "nonnumeric", track: {id: "invalid"}},
                {tag: "zero", track: {id: "0"}},
                {tag: "unsafe", track: {id: "9007199254740992"}}
            ];
        }
        function test_invalidIdDoesNotChangeTarget(data) {
            const item = detector();
            compare(item.analyzeTrack({id: "123", title: "Keep title"}), "");
            verify(item.analyzeTrack(data.track).length > 0);
            compare(item.currentTrackId, "123");
            compare(item.currentTitle, "Keep title");
            assertDispatch("123");
        }
        function test_busyDoesNotChangeTarget_data() {
            return [
                {tag: "bridge", field: "bridge"},
                {tag: "backend", field: "busy"},
                {tag: "running", field: "running"},
                {tag: "retry", field: "retry"}
            ];
        }
        function test_busyDoesNotChangeTarget(data) {
            const item = detector();
            compare(item.analyzeTrack({id: "123", title: "Keep title"}), "");
            if (data.field === "bridge") backend.censorBusy = true;
            else if (data.field === "retry") item.retryIds = ["pending"];
            else {
                const state = {native: true, jobs: []};
                state[data.field] = true;
                backend.censorState = state;
            }
            verify(item.analyzeTrack({id: "456", title: "Rejected"}).length > 0);
            compare(item.currentTrackId, "123");
            compare(item.currentTitle, "Keep title");
            assertDispatch("123");
        }
        function test_unavailableTrackStillDispatches() {
            const item = detector();
            item.normalPlayer = player;
            const track = {
                id: "456", title: "Unavailable", artist: "Library artist",
                available: false, streamUrl: "", cover: "qrc:/assets/lucide-music.svg",
                coverUri: "example.org/original/%%", durationMs: 234567,
                artists: [{id: "42", name: "Library artist"}]
            };
            compare(item.analyzeTrack(track), "");
            compare(item.explicitTargetTrack, track);
            compare(item.explicitTargetTrack.available, false);
            compare(item.explicitTargetTrack.cover, track.cover);
            assertDispatch("456");
            compare(item.currentTrackId, "456");
            compare(item.currentTitle, "Unavailable");
            compare(item.originalAudioUrl.toString(), "");
            compare(item.playableSelection, false);
            compare(player.trackId, "99");
        }
        function test_restoreApprovalWithoutJobOrCandidate() {
            const item = detector();
            const track = {
                id: "456", title: "Unavailable", artist: "Library artist",
                available: false, cover: "qrc:/assets/lucide-music.svg",
                coverUri: "example.org/original/%%", durationMs: 234567,
                artists: [{id: "42", name: "Library artist"}]
            };
            compare(item.analyzeTrack(track), "");
            assertDispatch("456");
            verify(!item.job);
            verify(!item.candidate);
            const button = findChild(item, "censorReviewApproval");
            verify(button !== null);
            compare(button.text, "Restore");
            compare(button.accentColor, Qt.color("#3b82f6"));
            compare(button.enabled, true);
            mouseClick(button);
            const dialog = Array.from(item.data).find(child => typeof child.openRestore === "function");
            verify(dialog !== undefined);
            tryCompare(dialog, "opened", true);
            compare(dialog.restoreMode, true);
            compare(dialog.trackId, "456");
            compare(dialog.originalTitle, track.title);
            compare(dialog.originalArtist, track.artist);
            compare(dialog.coverSource.toString(), track.cover);
            compare(dialog.originalMetadata, track);
            compare(fakeUploader.metadataTrackId, "456");
            compare(findChild(item, "restoreTrackId").text, "456");
            compare(findChild(item, "sendTitle").text, track.title);
            compare(findChild(item, "sendArtist").text, track.artist);
            dialog.close();
        }
        function publishCandidates(item, unavailable, candidates, status) {
            backend.censorState = {native: true, native_repository_ready: true, jobs: [{
                id: "analysis", status: status || "review_required",
                request: {id: "request-456", track_id: "456", title: "Job title", artist: "Job artist", original_unavailable: unavailable},
                candidates: candidates
            }]};
            item.selectedId = "analysis";
        }
        function test_restoreCandidatePrefill_data() {
            return [
                {tag: "library-raw", explicit: true, resolved: false},
                {tag: "library-resolved", explicit: true, resolved: true},
                {tag: "request-raw", explicit: false, resolved: false},
                {tag: "request-resolved", explicit: false, resolved: true}
            ];
        }
        function test_restoreCandidatePrefill(data) {
            const item = detector();
            const track = {id: "456", title: "Library title", artist: "Library artist", available: false};
            if (data.explicit) compare(item.analyzeTrack(track), "");
            const selected = {
                id: "selected", title: "Candidate title", artist: "Candidate artist",
                url: "https://example.org/selected.mp3", full_audio: true, downloadable: true
            };
            publishCandidates(item, true, [
                {id: "other", url: "https://example.org/other.mp3", full_audio: true, downloadable: true},
                selected
            ]);
            item.switchCandidate(1);
            compare(item.candidateId, selected.id);
            verify(item.playableCandidate(item.candidate));
            if (data.resolved) item.resolvedCandidateUrl = "https://example.org/resolved.mp3";
            const expectedUrl = data.resolved ? "https://example.org/resolved.mp3" : selected.url;
            const button = findChild(item, "censorReviewApproval");
            compare(button.text, "Restore");
            compare(button.enabled, true);
            mouseClick(button);
            const dialog = Array.from(item.data).find(child => typeof child.openRestore === "function");
            verify(dialog !== undefined);
            tryCompare(dialog, "opened", true);
            compare(dialog.restoreMode, true);
            compare(dialog.trackId, "456");
            compare(dialog.originalTitle, data.explicit ? track.title : "Job title");
            compare(dialog.originalArtist, data.explicit ? track.artist : "Job artist");
            compare(dialog.originalMetadata, data.explicit ? track : item.job.request);
            compare(findChild(dialog, "sendTitle").text, dialog.originalTitle);
            compare(findChild(dialog, "sendArtist").text, dialog.originalArtist);
            compare(findChild(dialog, "sendUrl").text, expectedUrl);
            tryCompare(findChild(dialog, "sendTrackButton"), "enabled", true);
            verify(fakeUploader.extractionRequests.length > 0);
            compare(fakeUploader.extractionRequests[fakeUploader.extractionRequests.length - 1],
                    {id: "456", url: expectedUrl});
            verify(dialog.durationRequest > 0);
            dialog.close();
        }
        function test_unavailableCandidates_data() {
            return [{tag: "explicit", explicit: true}, {tag: "request", explicit: false}];
        }
        function test_unavailableCandidates(data) {
            const item = detector();
            if (data.explicit) compare(item.analyzeTrack({id: "456", available: false}), "");
            const first = {id: "first", title: "First"};
            const second = {id: "second", title: "Second"};
            publishCandidates(item, !data.explicit, [first, second]);
            tryCompare(item, "candidateId", "first");
            compare(item.originalUnavailable, true);
            compare(findChild(item, "censorPlaybackCard").visible, true);
            compare(findChild(item, "censorEmptyWaveform").visible, false);
            compare(findChild(item, "censorWaveformSeek").enabled, false);
            compare(item.timelineDuration, findChild(item, "censorCandidatePlayer").duration);
            compare(item.originalAudioUrl.toString(), "");
            item.switchCandidate(-1);
            compare(item.candidateId, "second");
            item.switchCandidate(1);
            compare(item.candidateId, "first");
            item.switchCandidate(1);
            compare(item.candidateId, "second");
            publishCandidates(item, !data.explicit, [first, second]);
            compare(item.candidateId, "second");
            publishCandidates(item, !data.explicit, [first]);
            tryCompare(item, "candidateId", "first");
            verify(item.disabledReason("approve").length > 0);
        }
        function test_unavailableEmptyResults() {
            const item = detector();
            compare(item.analyzeTrack({id: "456", available: false}), "");
            const status = findChild(item, "censorCandidateStatus");
            compare(status.visible, true);
            compare(status.text, "Searching candidates...");
            compare(findChild(item, "censorPlaybackCard").visible, false);
            publishCandidates(item, true, [], "running");
            compare(status.text, "Searching candidates...");
            publishCandidates(item, true, []);
            compare(status.text, "No candidates found");
            compare(item.candidateId, "");
            item.switchCandidate(1);
            compare(item.playableSelection, false);
            compare(item.playbackError, "");
            compare(findChild(item, "censorPlaybackCard").visible, false);
        }
        function test_requestRestoreMetadata() {
            const item = detector();
            publishCandidates(item, true, []);
            const button = findChild(item, "censorReviewApproval");
            compare(button.text, "Restore");
            compare(button.enabled, true);
            mouseClick(button);
            const dialog = Array.from(item.data).find(child => typeof child.openRestore === "function");
            tryCompare(dialog, "opened", true);
            compare(dialog.trackId, "456");
            compare(dialog.originalTitle, "Job title");
            compare(dialog.originalArtist, "Job artist");
            compare(dialog.originalMetadata, item.job.request);
            dialog.close();
        }
        function test_availableCandidateNavigation() {
            const item = detector();
            publishCandidates(item, false, [{id: "first"}, {id: "second"}]);
            compare(item.originalUnavailable, false);
            compare(item.candidateId, "");
            compare(findChild(item, "censorPlaybackCard").visible, true);
            compare(findChild(item, "censorCandidateStatus").visible, false);
            item.switchCandidate(1);
            compare(item.candidateId, "first");
            item.switchCandidate(1);
            compare(item.candidateId, "second");
            item.switchCandidate(1);
            compare(item.candidateId, "");
            item.switchCandidate(-1);
            compare(item.candidateId, "second");
            compare(findChild(item, "censorReviewApproval").text, "Approve");
            verify(item.disabledReason("approve").length > 0);
        }
        function test_normalApprovalWithoutCandidate_data() {
            return [
                {tag: "available", track: {id: "123", available: true}},
                {tag: "unknown", track: {id: "123"}}
            ];
        }
        function test_normalApprovalWithoutCandidate(data) {
            const item = detector();
            backend.censorState = {native: true, native_repository_ready: true, jobs: []};
            compare(item.analyzeTrack(data.track), "");
            assertDispatch("123");
            verify(!item.job);
            verify(!item.candidate);
            const button = findChild(item, "censorReviewApproval");
            verify(button !== null);
            compare(button.text, "Approve");
            compare(button.accentColor, Qt.color("#2bfef5"));
            compare(button.enabled, false);
            compare(item.disabledReason("send_candidate"), "Select a candidate first");
        }
        function test_playerTrackChangeResetsTarget() {
            const item = detector();
            item.normalPlayer = player;
            compare(item.analyzeTrack({id: "123", title: "Library title"}), "");
            player.trackId = "100";
            compare(item.explicitTargetTrack, null);
            compare(item.currentTrackId, "100");
            compare(item.currentTitle, "Player title");
        }
        function test_unsupportedBackendDoesNotSelect() {
            const item = detector();
            backend.censorState = {native: false, jobs: []};
            verify(item.analyzeTrack({id: "123"}).length > 0);
            compare(item.explicitTargetTrack, null);
            compare(backend.calls.length, 0);
        }
    }
}

import QtQuick
import QtQuick.Controls
import QtTest
import "qrc:/" as UI

Item {
    id: scene
    width: 800
    height: 600
    FontLoader { id: nunitoBold; source: Qt.resolvedUrl("../assets/fonts/Nunito-Bold.ttf") }
    FontLoader { id: nunitoSemiBold; source: Qt.resolvedUrl("../assets/fonts/Nunito-SemiBold.ttf") }
    QtObject {
        id: fakeUi
        property string selectedId: ""
        property var expanded: ({})
        property color bg: "#000000"
        property color muted: "#999999"
        property color ink: "#f0f0f4"
        property color surface: "#0c0c0e"
        property color surface2: "#171719"
        property color accentBorder: "#332bfef5"
        property color accentBg: "#142bfef5"
        property color accent: "#2bfef5"
        property color borderColor: "#3397979f"
        property color ignoredColor: "#dc5050"
        property int choices: 0
        property int menus: 0
        property int plays: 0
        function playTrack(row) { plays++; }
        function toggleExpanded(id) { const next = Object.assign({}, expanded); next[id] = !next[id]; expanded = next; }
        function choose(row) { selectedId = row.id; choices++; }
        function context(row, item, x, y, full) { menus++; }
    }
    QtObject {
        id: fakePlayer
        property bool enabled: true
        property int plays: 0
        property string timestamp: ""
        function playAt(id, ts) { plays++; timestamp = ts; }
    }
    Component {
        id: cardFactory
        UI.ResultCard {
            width: 600
            ui: fakeUi
            playbackBackend: fakePlayer
            actionBackend: null
            placeholderSource: "qrc:/assets/track-placeholder.svg"
            row: ({id: "101", title: "<b>literal title</b>", artist: "Artist", lines: [
                {ts: "00:42", parts: [{text: "<literal>", marked: true}]},
                {ts: "01:17", parts: [{text: "second line", marked: false}]}
            ]})
        }
    }
    Component {
        id: lyricsListFactory
        ListView {
            width: 600
            height: 160
            property int rowCount: 500
            model: 1
            cacheBuffer: 0
            reuseItems: true
            delegate: UI.ResultCard {
                width: 600
                ui: fakeUi
                playbackBackend: fakePlayer
                actionBackend: null
                placeholderSource: "qrc:/assets/track-placeholder.svg"
                row: {
                    const lines = [];
                    for (let i = 0; i < ListView.view.rowCount; ++i)
                        lines.push({ts: "00:42", parts: [{text: "marked words " + i, marked: true}]});
                    return {id: "101", title: "Long lyrics", lines: lines};
                }
            }
        }
    }
    QtObject {
        id: fakeUploader
        property bool busy: false
        property string error: ""
        property string status: "List: added · committed"
        signal completed(string action)
        signal stateChanged()
        property var metadata: ({title: "Existing", artists: [{id: "42", name: "Artist"}], coverUri: "example.org/cover", durationMs: 123000})
        function trackMetadata(id) { return metadata }
        property var lastSend: ({})
        function send(id, title, artist, url, patch) {
            lastSend = {id: id, title: title, artist: artist, url: url, patch: patch}
            busy = true; stateChanged();
        }
    }
    Component { id: sendFactory; UI.SendDialog { uploadBackend: fakeUploader } }
    Component { id: quoteFactory; UI.SiteQuote { width: 220; text: "A long warning that wraps across several lines without leaving the shared quote block" } }
    Component { id: buttonFactory; UI.SiteButton { text: "<b>literal</b>" } }
    Component { id: fieldFactory; UI.SiteTextField { Accessible.name: "Artist" } }
    Component { id: toggleFactory; UI.SiteToggleRow { text: "Hide ignored"; width: 300 } }
    Component {
        id: rowFactory
        UI.TrackRow {
            width: 450
            ui: fakeUi
            number: 1
            row: ({id: "101", title: "<b>literal title</b>", artist: "Artist", status: "found"})
            placeholderSource: "qrc:/assets/track-placeholder.svg"
        }
    }
    Component { id: dialogFactory; UI.SiteDialog { title: "Settings"; assetRoot: "qrc:/assets/"; width: 300; contentItem: UI.SiteTextField { Accessible.name: "Token" } } }

    TestCase {
        name: "SharedControls"
        when: windowShown
        function initTestCase() {
            tryCompare(nunitoBold, "status", FontLoader.Ready);
            tryCompare(nunitoSemiBold, "status", FontLoader.Ready);
        }
        function init() { fakeUi.plays = 0; fakeUi.choices = 0; fakeUi.menus = 0; fakeUi.selectedId = ""; fakeUi.expanded = ({}); fakePlayer.plays = 0; }
        function test_sharedTextContextMenu() {
            const field = createTemporaryObject(fieldFactory, scene, {text: "Artist"});
            const menu = field.ContextMenu.menu;
            verify(menu !== null);
            compare(menu.popupType, Popup.Item);
            compare(menu.count, 9);
            compare(menu.itemAt(3).glyph, "scissors");
            compare(menu.itemAt(4).enabled, false);
            field.selectAll();
            compare(menu.itemAt(4).enabled, true);
            field.readOnly = true;
            field.selectAll();
            compare(menu.itemAt(3).enabled, false);
            compare(menu.itemAt(4).enabled, true);
            field.echoMode = TextInput.Password;
            compare(menu.itemAt(4).enabled, false);
        }
        function test_trackDoubleClickPlays() {
            const row = createTemporaryObject(rowFactory, scene);
            verify(row !== null);
            mouseClick(row, 200, 29);
            tryCompare(fakeUi, "choices", 1);
            compare(fakeUi.plays, 0);
            compare(fakeUi.selectedId, "101");
            fakeUi.choices = 0;
            fakeUi.selectedId = "";
            // Qt Quick Test adds 500 ms to synthetic timestamps after every mouse
            // release, even in mouseDoubleClickSequence with an explicit delay.
            // Use timestamp-correct touch events to exercise the same TapHandler's
            // exclusive double-tap path without changing the production interval.
            const taps = touchEvent(row);
            taps.press(0, row, 200, 29).commit();
            taps.release(0, row, 200, 29).commit();
            taps.press(0, row, 200, 29).commit();
            taps.release(0, row, 200, 29).commit();
            tryCompare(fakeUi, "plays", 1);
            // Let any deferred singleTapped signal fire before checking exclusivity.
            wait(Qt.styleHints.mouseDoubleClickInterval);
            compare(fakeUi.choices, 0);
            compare(fakeUi.selectedId, "");
        }
        function test_buttonKeyboardAndPlainText() {
            const button = createTemporaryObject(buttonFactory, scene);
            verify(button !== null);
            compare(findChild(button, "buttonLabel").textFormat, Text.PlainText);
            button.forceActiveFocus(Qt.TabFocusReason);
            tryCompare(button, "visualFocus", true);
            const ring = findChild(button, "buttonFocusRing");
            verify(ring !== null);
            compare(ring.visible, true);
            const spy = createTemporaryQmlObject('import QtTest; SignalSpy {}', scene, "buttonSpy");
            spy.target = button; spy.signalName = "clicked";
            keyClick(Qt.Key_Space);
            compare(spy.count, 1);
            button.enabled = false;
            keyClick(Qt.Key_Space);
            compare(spy.count, 1);
        }
        function test_loadingDisablesButton() {
            const button = createTemporaryObject(buttonFactory, scene, {loading: true});
            compare(button.enabled, false);
            const spinner = findChild(button, "buttonSpinner");
            verify(spinner !== null);
            compare(spinner.visible, true);
            button.loading = false;
            compare(spinner.visible, false);
            compare(button.enabled, true);
        }
        function test_sendOriginalPrefills_data() {
            return [{tag: "library", candidate: false}, {tag: "candidate", candidate: true}]
        }
        function test_sendOriginalPrefills(data) {
            fakeUploader.busy = false
            const dialog = createTemporaryObject(sendFactory, scene)
            const saved = fakeUploader.metadata
            fakeUploader.metadata = ({})
            const original = {title: "Original title", artists: [{id: 12, name: "First"}, {id: "34", name: "Second"}], coverUri: "https://avatars.yandex.net/real/%%", durationMs: 234567}
            if (data.candidate)
                dialog.openCandidate("123", "Display title", "Display artist", "", "https://example.org/audio.mp3", original)
            else
                dialog.openTrack("123", "Display title", "Display artist", "", original)
            fakeUploader.metadata = saved
            tryCompare(dialog, "opened", true)
            compare(JSON.stringify(dialog.metadataPatch()), "{}")
            compare(findChild(dialog, "metadataTitle").text, "Original title")
            compare(findChild(dialog, "metadataArtists").text, "12: First; 34: Second")
            compare(findChild(dialog, "metadataCover").text, "avatars.yandex.net/real/%%")
            compare(findChild(dialog, "metadataDuration").text, "234567")
            findChild(dialog, "sendTrackButton").clicked()
            compare(JSON.stringify(fakeUploader.lastSend.patch), "{}")
            fakeUploader.busy = false
            dialog.sending = false
            findChild(dialog, "editMetadata").checked = true
            for (const name of ["editTitle", "editArtists", "editCover", "editDuration"])
                findChild(dialog, name).checked = true
            compare(JSON.stringify(dialog.metadataPatch()), "{}")
            findChild(dialog, "sendTrackButton").clicked()
            compare(JSON.stringify(fakeUploader.lastSend.patch), "{}")
            fakeUploader.busy = false
            dialog.sending = false
            dialog.close()
            findChild(dialog, "metadataTitle").text = "Edited original"
            findChild(dialog, "metadataArtists").text = "12: Renamed"
            findChild(dialog, "metadataCover").text = "example.org/edited"
            findChild(dialog, "metadataDuration").text = "1000"
            findChild(dialog, "sendTrackButton").clicked()
            compare(JSON.stringify(fakeUploader.lastSend.patch), JSON.stringify({title: "Edited original", coverUri: "example.org/edited", durationMs: 1000, artists: [{id: "12", name: "Renamed"}]}))
            fakeUploader.busy = false
            dialog.sending = false
            findChild(dialog, "metadataTitle").text = "Discard this draft"
            fakeUploader.metadata = ({})
            if (data.candidate)
                dialog.openCandidate("456", "Next title", "Unknown ID artist", "", "")
            else
                dialog.openTrack("456", "Next title", "Unknown ID artist", "")
            fakeUploader.metadata = saved
            compare(findChild(dialog, "metadataTitle").text, "Next title")
            compare(findChild(dialog, "metadataArtists").text, "Unknown ID artist")
            compare(findChild(dialog, "metadataCover").text, "")
            compare(findChild(dialog, "metadataDuration").text, "")
            verify(!findChild(dialog, "editMetadata").checked)
            compare(JSON.stringify(dialog.metadataPatch()), "{}")
            findChild(dialog, "editMetadata").checked = true
            for (const name of ["editTitle", "editArtists", "editCover", "editDuration"])
                findChild(dialog, name).checked = true
            compare(JSON.stringify(dialog.metadataPatch()), "{}")
            findChild(dialog, "metadataTitle").text = "New title"
            findChild(dialog, "metadataArtists").text = "7: New artist"
            findChild(dialog, "metadataCover").text = "example.org/new"
            findChild(dialog, "metadataDuration").text = "4567"
            findChild(dialog, "sendTrackButton").clicked()
            compare(JSON.stringify(fakeUploader.lastSend.patch), JSON.stringify({title: "New title", coverUri: "example.org/new", durationMs: 4567, artists: [{id: "7", name: "New artist"}]}))
            fakeUploader.busy = false
            dialog.sending = false
            dialog.close()
            fakeUploader.metadata = ({})
            dialog.openTrack("789", "", "", "", {artists: [{name: "No ID"}], coverUri: "//example.org/cover", durationMs: 1234.4})
            fakeUploader.metadata = saved
            compare(findChild(dialog, "metadataTitle").text, "")
            compare(findChild(dialog, "metadataArtists").text, "No ID")
            compare(findChild(dialog, "metadataCover").text, "example.org/cover")
            compare(findChild(dialog, "metadataDuration").text, "1234")
            dialog.close()
        }
        function test_sendOptionalMetadataPatch() {
            fakeUploader.busy = false
            const dialog = createTemporaryObject(sendFactory, scene)
            dialog.openTrack("123", "Display title", "Display artist", "")
            tryCompare(dialog, "opened", true)
            compare(JSON.stringify(dialog.metadataPatch()), "{}")
            compare(findChild(dialog, "metadataTitle").text, "Existing")
            compare(findChild(dialog, "metadataArtists").text, "42: Artist")
            compare(findChild(dialog, "metadataCover").text, "example.org/cover")
            compare(findChild(dialog, "metadataDuration").text, "123000")
            findChild(dialog, "editMetadata").checked = true
            findChild(dialog, "editTitle").checked = true
            findChild(dialog, "editArtists").checked = true
            compare(JSON.stringify(dialog.metadataPatch()), "{}")
            findChild(dialog, "metadataTitle").text = "Custom"
            compare(dialog.metadataPatch().title, "Custom")
            verify(dialog.metadataPatch().artists === undefined)
            findChild(dialog, "metadataArtists").text = "42: Renamed; 99: New"
            compare(dialog.metadataPatch().artists[0].id, "42")
            compare(dialog.metadataPatch().artists[1].name, "New")
            findChild(dialog, "editTitle").checked = false
            verify(dialog.metadataPatch().title === undefined)
            findChild(dialog, "editMetadata").checked = false
            compare(JSON.stringify(dialog.metadataPatch()), "{}")
            dialog.close()
            dialog.openTrack("456", "Other", "Other", "")
            compare(JSON.stringify(dialog.metadataPatch()), "{}")
            verify(!findChild(dialog, "editArtists").checked)
            verify(dialog.height <= scene.height)
            dialog.close()
        }
        function test_sendMetadataSwitchPreservesPanels_data() {
            return [{tag: "track", candidate: false}, {tag: "candidate", candidate: true}]
        }
        function test_sendMetadataSwitchPreservesPanels(data) {
            fakeUploader.busy = false
            const dialog = createTemporaryObject(sendFactory, scene)
            if (data.candidate)
                dialog.openCandidate("123", "Display title", "Display artist", "", "https://example.org/track.mp3")
            else
                dialog.openTrack("123", "Display title", "Display artist", "")
            tryCompare(dialog, "opened", true)
            const toggle = findChild(dialog, "editMetadata")
            const summary = findChild(dialog, "trackSummaryCard")
            const standard = findChild(dialog, "standardSendPanel")
            const metadata = findChild(dialog, "metadataSendPanel")
            const title = findChild(dialog, "sendTitle")
            const artist = findChild(dialog, "sendArtist")
            const url = findChild(dialog, "sendUrl")
            verify(toggle instanceof UI.SiteToggleRow)
            compare(toggle.Accessible.name, "Edit metadata")
            const footer = findChild(dialog, "sendFooter")
            const cancel = findChild(dialog, "cancelSendButton")
            const send = findChild(dialog, "sendTrackButton")
            const summaryTitle = findChild(dialog, "trackSummaryTitle")
            compare(summaryTitle.font.pixelSize, 16)
            verify(summaryTitle.font.pixelSize > title.font.pixelSize)
            compare(toggle.parent, footer)
            compare(toggle.indicatorOnLeft, true)

            for (const width of [480, 320]) {
                dialog.width = width
                waitForRendering(dialog.contentItem)
                const label = findChild(toggle, "toggleLabel")
                verify(toggle.indicator.x + toggle.indicator.width <= label.mapToItem(toggle, 0, 0).x,
                                       "Indicator right: " + (toggle.indicator.x + toggle.indicator.width) + ", label left: " + label.mapToItem(toggle, 0, 0).x)
                verify(footer.mapToItem(summary, 0, 0).y >= summary.height)
                verify(toggle.x < cancel.x)
                verify(toggle.x + toggle.width <= cancel.x)
                verify(cancel.x + cancel.width <= send.x)
                verify(send.x + send.width <= footer.width + 1)
                verify(Math.abs(toggle.y + toggle.height / 2 - send.y - send.height / 2) < 1)
            }
            compare(standard.visible, true)
            compare(metadata.visible, false)
            compare(url.visible, !data.candidate)
            title.text = "Commit title"
            artist.text = "Commit artist"
            const expectedUrl = data.candidate ? url.text : "https://example.org/custom.mp3"
            url.text = expectedUrl
            toggle.forceActiveFocus(Qt.TabFocusReason)
            keyClick(Qt.Key_Space)
            compare(toggle.checked, true)
            compare(standard.visible, false)
            compare(title.visible, false)
            compare(artist.visible, false)
            compare(url.visible, false)
            compare(metadata.visible, true)
            findChild(dialog, "editTitle").checked = true
            findChild(dialog, "metadataTitle").text = "Override title"
            keyClick(Qt.Key_Space)
            compare(toggle.checked, false)
            compare(standard.visible, true)
            compare(metadata.visible, false)
            compare(title.text, "Commit title")
            compare(artist.text, "Commit artist")
            compare(url.text, expectedUrl)
            compare(JSON.stringify(dialog.metadataPatch()), "{}")
            keyClick(Qt.Key_Space)
            compare(dialog.metadataPatch().title, "Override title")
            findChild(dialog, "sendTrackButton").clicked()
            compare(fakeUploader.lastSend.title, "Commit title")
            compare(fakeUploader.lastSend.artist, "Commit artist")
            compare(fakeUploader.lastSend.url, expectedUrl)
            compare(fakeUploader.lastSend.patch.title, "Override title")
            compare(toggle.enabled, false)
            fakeUploader.busy = false
            dialog.close()
        }
        function test_sendUsesSharedLoader() {
            fakeUploader.busy = false;
            const dialog = createTemporaryObject(sendFactory, scene);
            dialog.openTrack("123", "Title", "Artist", "");
            tryCompare(dialog, "opened", true);
            const button = findChild(dialog, "sendTrackButton");
            verify(button !== null);
            compare(button.text, "Send");
            fakeUploader.busy = true;
            tryCompare(button, "loading", true);
            compare(button.enabled, false);
            compare(findChild(button, "buttonSpinner").visible, true);
            fakeUploader.busy = false;
            tryCompare(button, "loading", false);
            dialog.close();
        }
        function test_quoteWrapsWithinComponent() {
            const quote = createTemporaryObject(quoteFactory, scene);
            verify(quote.implicitHeight > 40);
            compare(quote.leftPadding, 16);
            compare(quote.contentItem.textFormat, Text.PlainText);
            compare(quote.contentItem.wrapMode, Text.Wrap);
            compare(quote.contentItem.lineHeightMode, Text.ProportionalHeight);
            compare(quote.contentItem.lineHeight, 1.0);
        }
        function test_fieldSelectionAndToggleKeyboard() {
            const field = createTemporaryObject(fieldFactory, scene, {text: "Artist"});
            field.forceActiveFocus(Qt.TabFocusReason);
            field.selectAll();
            compare(field.selectedText, "Artist");
            const toggle = createTemporaryObject(toggleFactory, scene, {y: 60});
            toggle.forceActiveFocus(Qt.TabFocusReason);
            keyClick(Qt.Key_Space);
            compare(toggle.checked, true);
            keyClick(Qt.Key_Space);
            compare(toggle.checked, false);
        }
        function test_trackKeyboardAndMenu() {
            const row = createTemporaryObject(rowFactory, scene);
            row.forceActiveFocus(Qt.TabFocusReason);
            keyClick(Qt.Key_Return);
            compare(fakeUi.selectedId, "101");
            compare(fakeUi.choices, 1);
            keyClick(Qt.Key_Menu);
            keyClick(Qt.Key_F10, Qt.ShiftModifier);
            compare(fakeUi.menus, 2);
        }
        function test_lyricsKeyboardAndLiteralMetadata() {
            const card = createTemporaryObject(cardFactory, scene);
            compare(findChild(card, "resultTitle").textFormat, Text.PlainText);
            card.forceActiveFocus(Qt.TabFocusReason);
            keyClick(Qt.Key_Down);
            compare(card.keyboardLine, 1);
            keyClick(Qt.Key_Return);
            compare(fakePlayer.plays, 1);
            compare(fakePlayer.timestamp, "01:17");
            keyClick(Qt.Key_Menu);
            compare(fakeUi.menus, 1);
            verify(!card.canPlayLine({ts: "99:99"}));
            verify(!card.canPlayLine({ts: null}));
        }
        function test_unsyncedExpansionCannotPlay() {
            const card = createTemporaryObject(cardFactory, scene, {row: {
                id: "102", title: "Unsynced", lines: [{parts: [{text: "hit", marked: true}]}],
                allLines: [{parts: [{text: "full", marked: false}]}, {parts: [{text: "hit", marked: true}]}]
            }});
            compare(card.displayLines.length, 1);
            fakeUi.toggleExpanded("102");
            tryCompare(card, "expanded", true);
            compare(card.displayLines.length, 2);
            verify(!card.canPlayLine({ts: "00:42"}));
            card.forceActiveFocus(Qt.TabFocusReason);
            keyClick(Qt.Key_Return);
            compare(fakePlayer.plays, 0);
        }
        function test_pooledLyricsReleaseEditors() {
            const view = createTemporaryObject(lyricsListFactory, scene, {rowCount: 500});
            tryVerify(function() { return view.itemAtIndex(0) !== null; });
            const card = view.itemAtIndex(0);
            tryVerify(function() { return card.instantiatedEditors > 0; });
            const originalHeight = card.height;
            card.pooled = true;
            tryCompare(card, "instantiatedEditors", 0);
            compare(card.inViewport, false);
            compare(card.height, originalHeight);
            card.pooled = false;
            tryVerify(function() { return card.instantiatedEditors > 0; });
            verify(card.instantiatedEditors <= 12);
            card.keyboardLine = 10;
            card.row = {id: "102", title: "Replacement", lines: [
                {ts: "01:00", parts: [{text: "New lyrics", marked: false}]}
            ]};
            compare(card.keyboardLine, 0);
            tryVerify(function() { return findChild(card, "lyricWords0") !== null; });
            compare(findChild(card, "lyricWords0").text.indexOf("New lyrics") >= 0, true);
        }
        function test_longLyricsOffscreenMarksAndKeyboardScroll_data() {
            return [{tag: "500 rows", rows: 500}, {tag: "5000 rows", rows: 5000}];
        }
        function test_longLyricsOffscreenMarksAndKeyboardScroll(data) {
            const started = Date.now();
            const view = createTemporaryObject(lyricsListFactory, scene, {rowCount: data.rows});
            tryVerify(function() { return view.itemAtIndex(0) !== null; });
            const card = view.itemAtIndex(0);
            verify(card.layoutBackend !== null, "Native lyrics layout is required");
            tryVerify(function() { return card.lineOffsets.length === data.rows + 1; });
            const first = findChild(card, "lyricWords0");
            verify(first !== null);
            tryVerify(function() { return first.markRects.length > 0; });
            compare(findChild(card, "lyricWords" + (data.rows - 1)), null);
            verify(card.instantiatedEditors <= 12);
            const originalHeight = card.height;
            const loadMilliseconds = Date.now() - started;
            const scrollStarted = Date.now();
            const passes = card.measurementPasses;
            const initialEditors = card.instantiatedEditors;
            let peakEditors = initialEditors;
            card.instantiatedEditorsChanged.connect(function() {
                peakEditors = Math.max(peakEditors, card.instantiatedEditors);
            });
            card.forceActiveFocus(Qt.TabFocusReason);
            card.keyboardLine = data.rows - 1;
            tryVerify(function() { return view.contentY > 1000; });
            tryVerify(function() {
                const last = findChild(card, "lyricWords" + (data.rows - 1));
                return last !== null && last.markRects.length > 0;
            });
            compare(findChild(card, "lyricWords0"), null);
            verify(card.instantiatedEditors <= 12);
            compare(card.height, originalHeight);
            compare(card.measurementPasses, passes);
            keyClick(Qt.Key_Return);
            compare(fakePlayer.timestamp, "00:42");
            card.keyboardLine = 0;
            tryCompare(view, "contentY", 54);
            tryVerify(function() {
                const firstAgain = findChild(card, "lyricWords0");
                return firstAgain !== null && firstAgain.markRects.length > 0;
            });
            compare(card.height, originalHeight);
            verify(peakEditors <= 12, "Editor bound exceeded during a jump: " + peakEditors);
            console.info("Lyrics bench:", data.rows, "rows,", initialEditors,
                "visible editors, peak", peakEditors, "+ 1 measuring editor, native", !!card.layoutBackend,
                                "load", loadMilliseconds, "ms, scroll", Date.now() - scrollStarted, "ms, passes", passes);
        }
        function test_longUnsyncedExpansionAndDataReplacement() {
            const view = createTemporaryObject(lyricsListFactory, scene, {rowCount: 500});
            tryVerify(function() { return view.itemAtIndex(0) !== null; });
            const card = view.itemAtIndex(0);
            const full = card.displayLines;
            card.row = {id: "102", lines: [full[0]], allLines: full};
            tryVerify(function() { return card.lineOffsets.length === 2; });
            const collapsedHeight = card.height;
            fakeUi.toggleExpanded("102");
            tryVerify(function() { return card.lineOffsets.length === 501; });
            verify(card.height > collapsedHeight);
            card.forceActiveFocus(Qt.TabFocusReason);
            card.keyboardLine = 499;
            tryVerify(function() { return findChild(card, "lyricWords499") !== null; });
            keyClick(Qt.Key_Return);
            compare(fakePlayer.plays, 0);
            fakeUi.toggleExpanded("102");
            tryVerify(function() { return card.lineOffsets.length === 2; });
            compare(card.keyboardLine, 0);
            compare(card.height, collapsedHeight);
            tryVerify(function() { return findChild(card, "lyricWords0") !== null; });
            card.row = {id: "101", lines: [{ts: "02:34", parts: [{text: "replacement", marked: true}]}]};
            tryVerify(function() { return findChild(card, "lyricWords0").getText(0, 11) === "replacement"; });
            const words = findChild(card, "lyricWords0");
            mouseClick(card, words.x + 12, 54 + words.height / 2);
            compare(fakePlayer.plays, 1);
            compare(fakePlayer.timestamp, "02:34");
            verify(card.instantiatedEditors <= 12);
        }
        function test_lyricsMeasuredGeometryAndResize() {
            const rows = [
                {ts: "00:01", parts: []},
                {ts: "00:02", parts: [{text: "<literal> & \"quoted\"", marked: true}]},
                {ts: "00:03", parts: [{text: "before\n", marked: false}, {text: "marked\nafter", marked: true}]},
                {ts: "00:04", parts: [{text: "long wrapped marked words ".repeat(25), marked: true}]},
                {ts: "00:05", parts: [{text: "العربية עברית 😀 é 漢字 ".repeat(12), marked: true}]}
            ];
            const card = createTemporaryObject(cardFactory, scene, {
                width: 320, row: {id: "101", lines: rows}
            });
            tryVerify(function() { return card.lineOffsets.length === rows.length + 1; });
            verify(card.layoutBackend !== null, "Native lyrics layout is required");
            for (let i = 0; i < rows.length; ++i) {
                const words = findChild(card, "lyricWords" + i);
                if (words)
                    fuzzyCompare(card.lineOffsets[i + 1] - card.lineOffsets[i],
                        Math.max(19, words.contentHeight) + 8, 0.01);
            }
            const narrowHeight = card.height;
            card.width = 600;
            tryVerify(function() { return card.height < narrowHeight; });
            for (let i = 0; i < rows.length; ++i) {
                const words = findChild(card, "lyricWords" + i);
                if (words)
                    fuzzyCompare(card.lineOffsets[i + 1] - card.lineOffsets[i],
                        Math.max(19, words.contentHeight) + 8, 0.01);
            }
            const first = findChild(card, "lyricWords0");
            verify(first !== null);
            compare(first.selectByMouse, false);
            compare(first.readOnly, true);
            compare(first.activeFocusOnPress, false);
            for (let i = 0; i < rows.length; ++i) {
                compare(card.lineAtOffset(card.lineOffsets[i]), i);
                compare(card.lineAtOffset(card.lineOffsets[i + 1] - 0.01), i);
            }
        }
        function test_dialogSharedPadding() {
            const dialog = createTemporaryObject(dialogFactory, scene);
            compare(dialog.leftPadding, 8);
            compare(dialog.rightPadding, 8);
            compare(dialog.bottomPadding, 8);
            compare(dialog.topPadding, 0);
        }
        function test_dialogRestoresFocus() {
            const button = createTemporaryObject(buttonFactory, scene);
            button.forceActiveFocus(Qt.TabFocusReason);
            const dialog = createTemporaryObject(dialogFactory, scene);
            dialog.open();
            tryCompare(dialog, "opened", true);
            dialog.contentItem.forceActiveFocus(Qt.TabFocusReason);
            keyClick(Qt.Key_Escape);
            tryCompare(dialog, "visible", false);
            tryCompare(button, "activeFocus", true);
        }
    }
}

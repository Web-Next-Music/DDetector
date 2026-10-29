import QtQuick
import QtQuick.Layouts
import QtTest
import "qrc:/" as UI
Item {
    width: 800; height: 600
    QtObject {
        id: backend
        property bool busy: false
        property string error: ""
        property string status: ""
        property int serial: 0
        property int durationSerial: 0
        signal stateChanged()
        signal completed(string action)
        signal durationReady(int request, double durationMs)
        signal metadataReady(int request, string id, var metadata, string error)
        property string cachedTitle: "Cached title"
        function trackMetadata(id) { return {title: cachedTitle} }
        function requestMetadata(id) { return ++serial }
        function requestDuration(id, url) { return ++durationSerial }
    }
    UI.SendDialog { id: dialog; uploadBackend: backend }
    TestCase {
        name: "RestoreMetadata"
        when: windowShown
        function summaryTitle() { return dialog.restoreMode ? dialog.restorePreviewTitle : findChild(dialog, "trackSummaryTitle").text }
        function cleanup() { dialog.close(); backend.cachedTitle = "Cached title" }
        function test_previewContainsEditableIDWithoutCover() {
            dialog.openRestore("42", "Title", "Name", "", {artists: [{id: 7, name: "Name"}]})
            const card = findChild(dialog, "trackSummaryCard")
            const input = findChild(card, "restoreTrackId")
            verify(input !== null)
            verify(card.visible)
            verify(input.visible)
            verify(input.enabled)
            compare(input.text, "42")
            tryVerify(function() { return input.width > 0 && input.height > 0 })
            const position = input.mapToItem(card, 0, 0)
            verify(position.x >= 0 && position.y >= 0)
            verify(position.y + input.height <= card.height)
            verify(!findChild(dialog, "sendCoverPreview").visible)
            input.text = "43"
            compare(dialog.trackId, "43")
            compare(dialog.restoreArtists.length, 0)
        }
        function test_artistIdEditingKeepsSummaryHeight() {
            dialog.openRestore("42", "Title", "Name", "", {
                artists: [{id: 4622996, name: "Artist with a much longer name than its ID"}]
            })
            const card = findChild(dialog, "trackSummaryCard")
            const name = findChild(card, "restoreArtistName0")
            const input = findChild(card, "restoreArtistId0")
            tryVerify(function() { return name.width > 0 && card.height > 0 })
            const initialHeight = card.height
            name.editId()
            tryVerify(function() { return input.visible && input.activeFocus })
            tryVerify(function() { return card.height <= initialHeight })
            verify(input.parent.height <= findChild(card, "trackSummaryTitle").height + 2)
        }
        function test_multiartistTitleAndAsyncUpdate() {
            dialog.openRestore("42", "Title", "Fallback name", "", {
                artists: [{name: "First"}, {name: "Second", id: ""}]
            })
            compare(summaryTitle(), "Cached title - First, Second")
            backend.metadataReady(dialog.metadataRequest, "42", {
                artists: [{id: 7, name: "First"}, {id: "8", name: "Second"}]
            }, "")
            compare(summaryTitle(), "Cached title - First, Second")
            dialog.openRestore("43", "Title", "Fallback name", "", {})
            compare(summaryTitle(), "Cached title - Fallback name")
        }
        function test_structuredMultiartistTitle() {
            dialog.openRestore("42", "Title", "Name", "", {
                artists: [{id: 7, name: "First"}, {id: 8, name: "Second"}]
            })
            compare(summaryTitle(), "Cached title - First, Second")
        }
        function test_targetChangeClearsMetadataAndGuardsResults() {
            dialog.openRestore("42", "Title", "Name", "", {}, "https://example.test/audio.mp3")
            const oldMetadata = dialog.metadataRequest
            const oldDuration = dialog.durationRequest
            backend.durationReady(oldDuration, 1234)
            compare(dialog.replacementDuration, 1234)
            const input = findChild(dialog, "restoreTrackId")
            input.text = "43"
            compare(dialog.restoreArtists.length, 0)
            compare(dialog.originalArtist, "")
            compare(dialog.replacementDuration, -1)
            backend.metadataReady(oldMetadata, "42", {artists: [{id: 7, name: "Stale"}]}, "")
            backend.durationReady(oldDuration, 9999)
            compare(dialog.restoreArtists.length, 0)
            compare(dialog.replacementDuration, -1)
            verify(summaryTitle().indexOf("Stale") < 0)
            const request = dialog.metadataRequest
            input.text = "invalid"
            verify(!dialog.validRestoreId)
            compare(dialog.trackId, "")
            verify(!dialog.canSend)
            backend.metadataReady(request, "43", {artists: [{id: 8, name: "Late"}]}, "")
            compare(dialog.restoreArtists.length, 0)
            input.text = "9007199254740992"
            verify(!dialog.validRestoreId)
            input.text = "0"
            verify(!dialog.validRestoreId)
        }
        function test_standardModesKeepPreview() {
            dialog.openTrack("42", "Send title", "Name", "", {})
            compare(summaryTitle(), "Send title")
            verify(!findChild(dialog, "restoreTrackId").visible)
            dialog.openCandidate("43", "Candidate title", "Name", "", "https://example.test/audio.mp3", {})
            compare(summaryTitle(), "Candidate title")
            verify(!findChild(dialog, "restoreTrackId").visible)
            verify(!dialog.showUrlFields)
        }
        function test_staleCacheAndLateResult() {
            dialog.openRestore("36045842", "Title", "Name", "", {})
            const old = dialog.metadataRequest
            compare(dialog.restoreArtists.length, 0)
            dialog.openRestore("42", "Other", "Other artist", "", {})
            backend.metadataReady(old, "36045842", {artists: [{id: 4622996, name: "Name"}]}, "")
            compare(dialog.restoreArtists.length, 0)
            backend.metadataReady(dialog.metadataRequest, "42", {artists: [{id: 7, name: "Other artist"}]}, "")
            compare(dialog.metadataPatch().artists[0].id, 7)
            compare(dialog.metadataPatch().title, "Cached title")
            dialog.close()
        }
        function test_providerCoverIsDecodedOnceAndValidated() {
            const cover = "https://images.genius.com/fixture"
            dialog.openRestore("42", "Title", "Name", "image://trackcovers/" + encodeURIComponent(cover), {artists: [{id: 7, name: "Name"}]})
            compare(dialog.metadataPatch().coverUri, cover)
            compare(dialog.fallbackCoverUrl("image://trackcovers/%ZZ"), "")
        }
        function test_structuredCacheNeedsNoAPI() {
            const serial = backend.serial
            dialog.openRestore("42", "Title", "Name", "", {artists: [{id: "7", name: "Name"}]})
            compare(backend.serial, serial)
            compare(dialog.metadataPatch().artists[0].id, "7")
            dialog.close()
        }
        function test_geniusCoverPersistence() {
                    const cover = "https://images.genius.com/cover"
                    dialog.openRestore("42", "Title", "Name", cover, {artists: [{id: 7, name: "Name"}]})
                    compare(dialog.metadataPatch().coverUri, cover)
                    compare(dialog.metadataPatch().artists[0].id, 7)
                    for (const rejected of ["image://trackcovers/cover", "https://images.genius.com.evil.test/cover", "https://secret@images.genius.com/cover", "https://images.genius.com/cover?secret=1", "https://images.genius.com.evil.test/123"]) {
                        dialog.openRestore("42", "Title", "Name", rejected, {artists: [{id: 7, name: "Name"}]})
                        verify(dialog.metadataPatch().coverUri === undefined)
                    }

        }
        function editArtist(index, value) {
            const fields = findChild(dialog, "restoreArtistFields")
            tryVerify(function() { return fields.itemAt(index) !== null })
            const input = findChild(fields.itemAt(index), "restoreArtistId" + index)
            verify(input !== null)
            tryCompare(dialog, "opened", true)
            if (!input.visible) mouseClick(findChild(fields.itemAt(index), "restoreArtistName" + index))
            verify(input.visible && input.enabled)
            input.forceActiveFocus()
            keyClick(Qt.Key_A, Qt.ControlModifier)
            for (let i = 0; i < value.length; ++i) keyClick(value[i])
        }
        function test_screenshotAPIBaselineAndEditableMultiartist() {
            backend.cachedTitle = "Отсиди"
            dialog.openRestore("36045842", "Отсиди", "Овсянкин", "", {}, "https://example.test/audio.mp3")
            backend.metadataReady(dialog.metadataRequest, "36045842", {
                artists: [{id: 4622996, name: "Овсянкин", entity: "artist", various: false}]
            }, "")
            compare(summaryTitle(), "Отсиди - Овсянкин")
            compare(dialog.metadataPatch().title, "Отсиди")
            verify(!findChild(dialog, "editMetadata").checked)
            compare(JSON.stringify(dialog.metadataPatch().artists), '[{"id":4622996,"name":"Овсянкин"}]')
            editArtist(0, "4622997")
            compare(dialog.metadataPatch().artists[0].id, "4622997")
            compare(dialog.originalMetadata.artists, undefined)
            backend.cachedTitle = "Cached title"
            dialog.openRestore("42", "Title", "Names", "", {
                artists: [{id: 7, name: "First", entity: "artist"}, {id: "8", name: "Second"}]
            }, "https://example.test/audio.mp3")
            editArtist(1, "7")
            verify(dialog.artistIdError.indexOf("duplicate ID 7") >= 0)
            verify(!dialog.canSend)
            editArtist(1, "invalid")
            verify(dialog.artistIdError.indexOf("positive numeric") >= 0)
            editArtist(1, "9")
            verify(dialog.canSend)
            verify(dialog.canSend)
            compare(dialog.metadataPatch().artists[1].id, "9")
            compare(dialog.originalMetadata.artists[1].id, "8")
            compare(summaryTitle(), "Cached title - First, Second")
            findChild(dialog, "restoreTrackId").text = "43"
            compare(dialog.restoreArtists.length, 0)
            backend.metadataReady(dialog.metadataRequest, "43", {artists: [{id: 8, name: "New"}]}, "")
            compare(dialog.metadataPatch().artists[0].id, 8)
        }
        function test_inlineArtistInteraction() {
            dialog.openRestore("42", "Title", "Names", "", {
                artists: [{id: 7, name: "First"}, {id: 8, name: "Second"}]
            }, "https://example.test/audio.mp3")
            const fields = findChild(dialog, "restoreArtistFields")
            tryVerify(function() { return fields.itemAt(1) !== null })
            tryCompare(dialog, "opened", true)
            const first = findChild(fields.itemAt(0), "restoreArtistName0")
            const second = findChild(fields.itemAt(1), "restoreArtistName1")
            const input = findChild(fields.itemAt(0), "restoreArtistId0")
            verify(first.visible && second.visible)
            verify(!input.visible)
            compare(findChild(dialog, "trackSummaryTitle").text, "Cached title")
            mouseMove(first, first.width / 2, first.height / 2)
            tryCompare(first, "hovered", true)
            verify(first.font.underline)
            verify(first.background == null)
            const title = findChild(dialog, "trackSummaryTitle")
            compare(first.font.pixelSize, title.font.pixelSize)
            compare(first.font.bold, title.font.bold)
            compare(first.mapToItem(title, 0, 0).y, 0)
            verify(first.mapToItem(title, 0, 0).x > title.width)
            compare(first.text, "First, ")
            editArtist(0, "17")
            verify(!first.visible)
            const metrics = Qt.createQmlObject('import QtQuick; TextMetrics {}', dialog)
            metrics.font = input.font
            metrics.text = input.text
            compare(input.width, metrics.advanceWidth + 2)
            compare(input.height, first.implicitHeight)
            compare(input.font.pixelSize, title.font.pixelSize)
            metrics.destroy()
            mouseClick(second)
            tryCompare(input, "visible", false)
            verify(first.visible)
            compare(dialog.metadataPatch().artists[0].id, "17")
            keyClick(Qt.Key_Return)
            tryCompare(second, "visible", true)
            first.forceActiveFocus(Qt.TabFocusReason)
            keyClick(Qt.Key_Space)
            tryCompare(input, "activeFocus", true)
            keyClick(Qt.Key_Return)
            verify(first.visible && first.activeFocus)
            keyClick(Qt.Key_Return)
            tryCompare(input, "activeFocus", true)
            findChild(dialog, "restoreTrackId").text = "43"
            backend.metadataReady(dialog.metadataRequest, "43", {artists: [{id: 9, name: "New"}]}, "")
            tryVerify(function() { return fields.itemAt(0) !== null })
            verify(findChild(fields.itemAt(0), "restoreArtistName0").visible)
            verify(!findChild(fields.itemAt(0), "restoreArtistId0").visible)
            compare(dialog.metadataPatch().artists[0].id, 9)
        }
        function test_inlineArtistsWrap() {
            dialog.openRestore("42", "Title", "Names", "", {
                artists: [{id: 7, name: "First"}, {id: 8, name: "Second"},
                          {id: 9, name: "Third"}]
            })
            tryCompare(dialog, "opened", true)
            const fields = findChild(dialog, "restoreArtistFields")
            const flow = fields.parent
            const savedWidth = flow.Layout.maximumWidth
            try {
                flow.Layout.maximumWidth = 180
                const first = fields.itemAt(0)
                const second = fields.itemAt(1)
                tryVerify(function() { return second.y > first.y })
                for (let i = 0; i < fields.count; ++i) {
                    const row = fields.itemAt(i)
                    verify(row.x >= 0 && row.x + row.width <= flow.width)
                    compare(findChild(row, "restoreArtistName" + i).font.pixelSize, 16)
                }
            } finally {
                flow.Layout.maximumWidth = savedWidth
            }
        }
        function test_noArtistsReturned() {
            dialog.openRestore("42", "Title", "Name", "", {})
            backend.metadataReady(dialog.metadataRequest, "42", {title: "Title"}, "")
            verify(dialog.metadataError.indexOf("no artist IDs") >= 0)
            compare(dialog.restoreArtists.length, 0)
            dialog.close()
        }
        function test_unavailable() {
            dialog.openRestore("36045842", "Title", "Name", "", {})
            backend.metadataReady(dialog.metadataRequest, "36045842", {}, "Yandex API unavailable")
            compare(dialog.metadataError, "Yandex API unavailable")
            verify(!dialog.canSend)
            dialog.close()
        }
    }
}

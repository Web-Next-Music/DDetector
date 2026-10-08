import QtQuick
import QtQuick.Controls
import QtQuick.Layouts
import QtQuick.Effects

ApplicationWindow {
    id: appUi
    width: 1200
    height: 800
    minimumWidth: 1200
    minimumHeight: 750
    visible: true
    flags: Qt.Window | Qt.FramelessWindowHint
    function toggleMaximized() {
        if (visibility === Window.Maximized) showNormal();
        else showMaximized();
    }
    title: demoMode ? "DDetector — Demo" : "DDetector"
    color: bg
    font.family: "DDetector Nunito"
    font.pixelSize: 13
    font.weight: Font.Bold
    property bool mobile: width <= 600
    property bool compactHeader: width < 950
    property color bg: "#000000"
    property color playerSurface: "#0c0c0e"
    property color surface: playerSurface
    property color surface2: "#171719"
    property color borderColor: "#3397979f"
    property color ink: "#f0f0f4"
    property color muted: "#666666"
    property color accent: "#2bfef5"
    property color accentBg: "#142bfef5"
    property color accentBorder: "#332bfef5"
    property color ignoredColor: "#dc5050"
    property var state: backend.state
    property bool privileged: !!state.authenticated && !!state.hasAccess && !state.isBanned
    property string selectedId: ""
    property bool sheetOpen: false
    property var expanded: ({})
    property var menuTrack: ({})
    property bool menuFullLyrics: false
    property string toastText: ""
    property int total: state.trackCount || 0
        property var userMetadata: (state.user || {}).user_metadata || ({})
        property string userName: userMetadata.full_name || userMetadata.name || userMetadata.preferred_username || (state.user || {}).email || "Account"
        property string avatarUrl: userMetadata.avatar_url || userMetadata.picture || ""

    function toggleExpanded(id) {
        let next = Object.assign({}, expanded);
        next[id] = !next[id];
        expanded = next;
    }
    function choose(track) {
        backend.copyId(track.id);
        selectedId = track.id;
        const index = backend.cards.indexOf(track.id);
        if (index >= 0) {
            sheetOpen = true;
            results.positionViewAtIndex(index, ListView.Contain);
        }
    }
    function context(track, source, x, y, fullLyrics) {
        menuTrack = track;
        menuFullLyrics = fullLyrics;
        const p = source.mapToItem(appUi.contentItem, x, y);
        trackMenu.x = Math.min(p.x, appUi.width - trackMenu.width - 8);
        trackMenu.y = Math.min(p.y, appUi.height - trackMenu.height - 8);
        trackMenu.open();
    }
    function notify(message) {
        toastText = message;
        toastTimer.restart();
    }
    Connections {
        target: backend
        function onToast(message) {
            appUi.notify(message);
        }
        function onStateChanged() {
            if (!appUi.privileged) {
                appUi.selectedId = "";
                appUi.expanded = ({});
                appUi.sheetOpen = false;
                trackMenu.close();
            }
            if (appUi.state.authenticated)
                auth.close();
        }
    }
    Connections {
        target: musicPlayer
        property string lastError: ""
        function onStateChanged() {
            if (musicPlayer.error && musicPlayer.error !== lastError)
                appUi.notify(musicPlayer.error);
            lastError = musicPlayer.error;
        }
    }
    Timer {
        id: toastTimer
        interval: 3500
        onTriggered: appUi.toastText = ""
    }

    component SmallButton: Button {
        HoverHandler { cursorShape: parent.enabled ? Qt.PointingHandCursor : Qt.ArrowCursor }
        property bool accented: false
        implicitHeight: 28
        Accessible.name: text
        font.family: "DDetector Nunito"
        font.pixelSize: 12
        font.weight: Font.Bold
        padding: 6
        leftPadding: 12
        rightPadding: 12
        opacity: enabled ? 1 : 0.5
        contentItem: Text {
            text: parent.text
            font: parent.font
            color: parent.accented || parent.hovered ? appUi.accent : appUi.muted
            horizontalAlignment: Text.AlignHCenter
            verticalAlignment: Text.AlignVCenter
        }
        background: Rectangle {
            radius: 8
            color: parent.accented || parent.hovered ? appUi.accentBg : appUi.surface
            border.color: parent.accented || parent.hovered || parent.activeFocus ? appUi.accentBorder : appUi.borderColor
        }
    }
    component MenuEntry: MenuItem {
        id: menuEntry
        property string glyph: ""
        property bool danger: false
                property bool green: false
                opacity: enabled ? 1 : 0.45
        HoverHandler { cursorShape: parent.enabled ? Qt.PointingHandCursor : Qt.ArrowCursor }
        font.family: "DDetector Nunito"
        font.pixelSize: 12
        font.weight: Font.Bold
        implicitHeight: 32
        contentItem: RowLayout {
            spacing: 8

            Image {
                visible: menuEntry.glyph !== ""
                Layout.preferredWidth: 16
                Layout.preferredHeight: 16
                source: menuEntry.glyph ? "qrc:/assets/lucide-" + menuEntry.glyph + ".svg" : ""
                layer.enabled: true
                layer.effect: MultiEffect {
                    brightness: 1
                    colorization: 1
                    colorizationColor: menuEntry.green ? "#35d078" : menuEntry.danger ? appUi.ignoredColor : menuEntry.highlighted ? appUi.accent : appUi.muted
                }
            }
            Text {
                Layout.fillWidth: true
                text: menuEntry.text
                font: menuEntry.font
                color: menuEntry.green ? "#35d078" : menuEntry.danger ? appUi.ignoredColor : menuEntry.highlighted ? appUi.accent : appUi.ink
                verticalAlignment: Text.AlignVCenter
            }
        }
        background: Rectangle {
            radius: 6
            color: parent.highlighted ? (parent.green ? "#2035d078" : parent.danger ? "#20dc5050" : appUi.accentBg) : "transparent"
        }
    }
    Menu {
        id: trackMenu
        width: 190
        padding: 4
        spacing: 2
        background: Rectangle {
            color: appUi.surface2
            radius: 10
            border.color: appUi.borderColor
        }
        Control {
            width: trackMenu.availableWidth
            padding: 6
            implicitHeight: contentItem.implicitHeight + topPadding + bottomPadding
            contentItem: Column {
                spacing: 2
                Text {
                    width: parent.width
                    text: appUi.menuTrack.title || ""
                    color: appUi.ink
                    font.pixelSize: 12
                    elide: Text.ElideRight
                }
                Text {
                    width: parent.width
                    text: appUi.menuTrack.artist || ""
                    color: appUi.muted
                    font.pixelSize: 11
                    elide: Text.ElideRight
                }
            }
        }
        MenuSeparator {
            contentItem: Rectangle {
                implicitHeight: 1
                color: appUi.borderColor
            }
        }
        MenuEntry {
            objectName: "playTrackEntry"
            text: "Play"
            glyph: "play"
            green: true
            enabled: musicPlayer.enabled && !musicPlayer.busy
            onTriggered: musicPlayer.playTrack(appUi.menuTrack.id)
        }
        MenuSeparator {
            contentItem: Rectangle { implicitHeight: 1; color: appUi.borderColor }
        }
        MenuEntry {
            text: "Search on Yandex"
            glyph: "search"
            onTriggered: backend.searchYandex(appUi.menuTrack.title, appUi.menuTrack.artist)
        }
        MenuEntry {
            text: "Copy track ID"
            glyph: "copy"
            onTriggered: backend.copyId(appUi.menuTrack.id)
        }
        MenuEntry {
            glyph: "maximize"
            visible: appUi.menuFullLyrics
            height: visible ? implicitHeight : 0
            text: appUi.expanded[appUi.menuTrack.id] ? "Collapse" : "Full lyrics"
            onTriggered: appUi.toggleExpanded(appUi.menuTrack.id)
        }
        MenuSeparator {
            contentItem: Rectangle {
                implicitHeight: 1
                color: appUi.borderColor
            }
        }
        MenuEntry {
            text: appUi.menuTrack.ignored ? "Unignore" : "Ignore"
            glyph: appUi.menuTrack.ignored ? "shield-check" : "shield"
            danger: !appUi.menuTrack.ignored
            onTriggered: backend.ignore(appUi.menuTrack.id, !appUi.menuTrack.ignored)
        }
    }
    Menu {
        id: headerMenu
        objectName: "accountMenu"
        width: 210
        padding: 4
        spacing: 2
        background: Rectangle {
            color: appUi.surface
            radius: 12
            border.color: appUi.borderColor
        }
        MenuEntry {
            objectName: "accountSettingsEntry"
            glyph: "settings"
            text: "Settings"
            onTriggered: settingsDialog.open()
        }

        MenuEntry {
            text: "Sign out"
            glyph: "log-out"
            danger: true
            enabled: !!appUi.state.authenticated
            onTriggered: backend.logout()
        }
    }
    Repeater {
        model: [Qt.LeftEdge, Qt.RightEdge, Qt.TopEdge, Qt.BottomEdge,
                Qt.LeftEdge | Qt.TopEdge, Qt.RightEdge | Qt.TopEdge,
                Qt.LeftEdge | Qt.BottomEdge, Qt.RightEdge | Qt.BottomEdge]
        MouseArea {
            required property int modelData
            property bool leftEdge: (modelData & Qt.LeftEdge) !== 0
            property bool rightEdge: (modelData & Qt.RightEdge) !== 0
            property bool topEdge: (modelData & Qt.TopEdge) !== 0
            property bool bottomEdge: (modelData & Qt.BottomEdge) !== 0
            property bool corner: (leftEdge || rightEdge) && (topEdge || bottomEdge)
            width: corner ? 6 : leftEdge || rightEdge ? 4 : appUi.width - 12
            height: corner ? 6 : topEdge || bottomEdge ? 4 : appUi.height - 12
            x: leftEdge ? 0 : rightEdge ? appUi.width - width : 6
            y: topEdge ? 0 : bottomEdge ? appUi.height - height : 6
            z: 100
            enabled: appUi.visibility !== Window.Maximized && appUi.visibility !== Window.FullScreen
            cursorShape: corner ? (leftEdge === topEdge ? Qt.SizeFDiagCursor : Qt.SizeBDiagCursor)
                                : leftEdge || rightEdge ? Qt.SizeHorCursor : Qt.SizeVerCursor
            onPressed: appUi.startSystemResize(modelData)
        }
    }
    Rectangle {
        id: titleBar
        objectName: "titleBar"
        width: parent.width
        height: appUi.state.authenticated ? 0 : 48
        visible: height > 0
        color: appUi.bg
        z: 30
        MouseArea {
            anchors.fill: parent
            anchors.rightMargin: 144
            onPressed: appUi.startSystemMove()
            onDoubleClicked: appUi.toggleMaximized()
        }
        Text {
            x: 12
            anchors.verticalCenter: parent.verticalCenter
            text: appUi.title
            font.family: "DDetector Quicksand"
            font.pixelSize: 12
            color: appUi.muted
        }
    }
    component WindowButton: Button {
                    HoverHandler { cursorShape: parent.enabled ? Qt.PointingHandCursor : Qt.ArrowCursor }
                    id: windowButton
                    required property int index
                    property string modelData: ["Minimize", "Maximize", "Close"][index]
                    objectName: "window" + modelData + "Button"
                    width: 48
                    height: 48
                    padding: 0
                    Accessible.name: index === 1 && appUi.visibility === Window.Maximized ? "Restore" : modelData
                    onClicked: {
                        if (index === 0) appUi.showMinimized();
                        else if (index === 1) appUi.toggleMaximized();
                        else appUi.close();
                    }
                    background: Rectangle {
                        color: windowButton.hovered ? (windowButton.index === 2 ? "#f23f42" : appUi.surface2) : "transparent"
                    }
                    contentItem: Item {
                        Canvas {
                            anchors.centerIn: parent
                            width: 10
                            height: 10
                            property color strokeColor: windowButton.hovered && windowButton.index === 2 ? "white" : appUi.ink
                            onStrokeColorChanged: requestPaint()
                            onPaint: {
                                const ctx = getContext("2d");
                                ctx.clearRect(0, 0, width, height);
                                ctx.strokeStyle = strokeColor;
                                ctx.lineWidth = 1;
                                ctx.beginPath();
                                if (windowButton.index === 0) {
                                    ctx.moveTo(0, 5.5); ctx.lineTo(10, 5.5);
                                } else if (windowButton.index === 1) {
                                    ctx.rect(0.5, 0.5, 9, 9);
                                } else {
                                    ctx.moveTo(0.5, 0.5); ctx.lineTo(9.5, 9.5);
                                    ctx.moveTo(9.5, 0.5); ctx.lineTo(0.5, 9.5);
                                }
                                ctx.stroke();
                            }
                        }
                    }
        }
    Rectangle {
        id: mainHeader
        visible: !!appUi.state.authenticated
        z: 10
        width: parent.width
        y: titleBar.height
        height: !visible ? 0 : appUi.compactHeader && appUi.privileged ? 86 : 48
        color: appUi.bg
        MouseArea {
            anchors.fill: parent
            onPressed: appUi.startSystemMove()
            onDoubleClicked: appUi.toggleMaximized()
        }
        Rectangle {
            anchors.bottom: parent.bottom
            width: parent.width
            height: 1
            color: appUi.borderColor
        }
        TapHandler {
            acceptedButtons: Qt.RightButton
            onTapped: eventPoint => {
                headerMenu.x = Math.min(eventPoint.position.x, appUi.width - headerMenu.width);
                headerMenu.y = eventPoint.position.y;
                headerMenu.open();
            }
        }
        Row {
            x: appUi.mobile ? 10 : 20
            y: 13
            spacing: 10
            Text {
                text: "D<span style='color:" + appUi.accent + "'>Detector</span>"
                textFormat: Text.RichText
                color: appUi.ink
                font.family: "DDetector Quicksand"
                font.pixelSize: 18
                font.weight: Font.Bold

            }
            Text {
                visible: !appUi.compactHeader && appUi.privileged
                text: backend.tracks.count + " tracks"
                color: appUi.muted
                font.pixelSize: 12
                anchors.verticalCenter: parent.verticalCenter
            }
        }
        RowLayout {
            anchors.right: parent.right
            anchors.rightMargin: appUi.compactHeader ? 10 : 224
            visible: appUi.privileged
            y: appUi.compactHeader ? 48 : 9
            width: appUi.compactHeader ? parent.width - 20 : parent.width - 454
            height: 30
            spacing: 8
            Item {
                Layout.fillWidth: true
                visible: !appUi.compactHeader
            }
            ComboBox {
                id: sorting
                HoverHandler { cursorShape: parent.enabled ? Qt.PointingHandCursor : Qt.ArrowCursor }
                Layout.preferredWidth: 100
                Layout.preferredHeight: 30
                model: ["Default", "By date", "A - Z", "By artist"]
                onActivated: backend.setSort(currentIndex)
                font.pixelSize: 12
                contentItem: Text {
                    text: sorting.displayText
                    font: sorting.font
                    color: appUi.muted
                    verticalAlignment: Text.AlignVCenter
                    horizontalAlignment: Text.AlignHCenter
                    rightPadding: 14
                }
                background: Rectangle {
                    color: appUi.surface
                    radius: 8
                    border.color: appUi.borderColor
                }
                indicator: Text {
                    text: "⌄"
                    color: appUi.muted
                    x: sorting.width - 18
                    y: 5
                }
                delegate: ItemDelegate {
                    HoverHandler { cursorShape: parent.enabled ? Qt.PointingHandCursor : Qt.ArrowCursor }
                    width: sorting.width
                    text: modelData
                    font.pixelSize: 12
                    palette.text: appUi.ink
                    highlighted: sorting.highlightedIndex === index
                    background: Rectangle {
                        color: parent.highlighted ? appUi.surface : appUi.surface2
                    }
                }
                popup: Popup {
                    y: sorting.height + 6
                    width: 120
                    padding: 1
                    background: Rectangle {
                        color: appUi.surface2
                        border.color: appUi.borderColor
                        radius: 8
                    }
                    contentItem: ListView {
                        implicitHeight: contentHeight
                        model: sorting.popup.visible ? sorting.delegateModel : null
                        currentIndex: sorting.highlightedIndex
                        clip: true
                    }
                }
            }
            TextField {
                id: search
                Layout.fillWidth: true
                Layout.maximumWidth: appUi.mobile ? 10000 : 300
                Layout.preferredHeight: 30
                placeholderText: "Search (Enter)"
                color: appUi.ink
                placeholderTextColor: "#666666"
                font.pixelSize: 13
                leftPadding: 12
                rightPadding: 12
                selectByMouse: true
                onAccepted: backend.setSearch(text)
                background: Rectangle {
                    color: appUi.surface
                    radius: 16
                    border.color: search.activeFocus ? appUi.accent : appUi.borderColor
                }
            }
            SmallButton {
                Layout.preferredWidth: appUi.mobile ? 64 : 100
                Layout.preferredHeight: 30
                text: appUi.state.fetching ? "Fetching" : "Fetch"
                enabled: !appUi.state.fetching && !appUi.state.loading
                onClicked: backend.fetch()
                ToolTip.visible: hovered
                ToolTip.text: "Rebuild track list from external source"
            }
        }
    }
    Row {
        anchors.right: parent.right
        anchors.rightMargin: 0
        y: 0
        z: 40
        spacing: 0
        SmallButton {
            id: avatarButton
            objectName: "avatarButton"
            visible: !!appUi.state.authenticated
            width: 34; height: 34
            anchors.verticalCenter: parent.verticalCenter
            padding: 0
            leftPadding: 0
            rightPadding: 0
            background: Rectangle {
                radius: width / 2
                color: avatarButton.hovered ? appUi.accentBg : appUi.surface
                border.color: avatarButton.hovered ? appUi.accentBorder : appUi.borderColor
            }
            Accessible.name: "Account menu for " + appUi.userName
            ToolTip.visible: hovered
            ToolTip.text: appUi.userName
            onClicked: {
                headerMenu.x = appUi.width - headerMenu.width - 10;
                headerMenu.y = mainHeader.y + 44;
                headerMenu.open();
            }
            contentItem: Item {
                Text {
                    anchors.centerIn: parent
                    text: appUi.userName.charAt(0).toUpperCase()
                    color: appUi.ink
                    visible: avatarImage.status !== Image.Ready
                }
                Image {
                    id: avatarImage
                    anchors.fill: parent
                    source: appUi.avatarUrl
                    asynchronous: true
                    fillMode: Image.PreserveAspectCrop
                    visible: false
                }
                Rectangle {
                    id: avatarMask
                    anchors.fill: parent
                    radius: width / 2
                    visible: false
                    layer.enabled: true
                    color: "white"
                }
                MultiEffect {
                    anchors.fill: parent
                    source: avatarImage
                    maskEnabled: true
                    maskSource: avatarMask
                    visible: avatarImage.status === Image.Ready
                }
            }
        }
        Item { width: appUi.state.authenticated ? 12 : 0; height: 48 }
        WindowButton { index: 0; anchors.verticalCenter: parent.verticalCenter }
        WindowButton { index: 1; anchors.verticalCenter: parent.verticalCenter }
        WindowButton { index: 2; anchors.verticalCenter: parent.verticalCenter }
    }
    Dialog {
        id: settingsDialog
        objectName: "settingsDialog"
        anchors.centerIn: Overlay.overlay
        width: Math.min(appUi.width - 32, 380)
        padding: 20
        topPadding: 12
        background: Rectangle {
            color: appUi.bg
            radius: 20
            border.color: appUi.borderColor
        }
        Overlay.modal: Rectangle { color: "#a3000000" }
        header: Rectangle {
            implicitHeight: 70
            color: "transparent"
            Rectangle {
                anchors.fill: parent
                anchors.margins: 8
                color: appUi.surface
                radius: 12
                border.color: appUi.borderColor
            }
            Text {
                anchors.left: parent.left
                anchors.leftMargin: 24
                anchors.verticalCenter: parent.verticalCenter
                text: "Settings"
                color: appUi.ink
                font.pixelSize: 16
            }
            SmallButton {
                anchors.right: parent.right
                anchors.rightMargin: 16
                anchors.verticalCenter: parent.verticalCenter
                width: 30
                height: 30
                text: "×"
                Accessible.name: "Close settings"
                onClicked: settingsDialog.close()
            }
        }
        footer: Item { implicitHeight: 8 }
        title: "Settings"
        modal: true
        focus: true
        standardButtons: Dialog.NoButton
        palette.window: appUi.surface
        palette.windowText: appUi.ink
        palette.text: appUi.ink
        contentItem: ColumnLayout {
            spacing: 12
            Label { text: "Yandex Music"; color: appUi.ink }
            TextField {
                id: musicToken
                objectName: "musicTokenSetting"
                Layout.fillWidth: true
                placeholderText: "OAuth token"
                echoMode: TextInput.Password
                selectByMouse: true
                color: appUi.ink
                background: Rectangle { color: appUi.surface; radius: 8; border.color: appUi.borderColor }
                onAccepted: applyMusicToken.clicked()
            }
            RowLayout {
                SmallButton {
                    id: applyMusicToken
                    text: musicPlayer.busy ? "Checking…" : "Apply token"
                    enabled: !musicPlayer.busy && musicToken.text.trim().length > 0
                    onClicked: { musicPlayer.setToken(musicToken.text); musicToken.clear(); }
                }
                SmallButton {
                    text: "Disconnect"
                    enabled: musicPlayer.enabled || musicPlayer.busy
                    onClicked: { musicPlayer.setToken(""); musicToken.clear(); }
                }
            }
            CheckBox {
                id: hideIgnoredToggle
                objectName: "hideIgnoredSetting"
                text: "Hide ignored tracks"
                checked: backend.hideIgnored
                onToggled: backend.hideIgnored = checked
                spacing: 12
                HoverHandler { cursorShape: parent.enabled ? Qt.PointingHandCursor : Qt.ArrowCursor }
                indicator: Rectangle {
                    implicitWidth: 40
                    implicitHeight: 22
                    x: hideIgnoredToggle.leftPadding
                    y: (hideIgnoredToggle.height - height) / 2
                    radius: 11
                    color: hideIgnoredToggle.checked ? appUi.accentBg : appUi.surface2
                    border.color: hideIgnoredToggle.checked ? appUi.accentBorder : appUi.borderColor
                    Rectangle {
                        width: 16; height: 16; radius: 8
                        x: hideIgnoredToggle.checked ? 21 : 3
                        y: 3
                        color: hideIgnoredToggle.checked ? appUi.accent : appUi.muted
                        Behavior on x { NumberAnimation { duration: 160 } }
                    }
                }
                contentItem: Text {
                    text: hideIgnoredToggle.text
                    font: hideIgnoredToggle.font
                    color: appUi.ink
                    verticalAlignment: Text.AlignVCenter
                    leftPadding: 52
                }
            }
        }
    }
    Item {
        id: panels
        visible: appUi.privileged
        anchors.top: mainHeader.bottom
        anchors.bottom: playerPanel.top
        width: parent.width
        clip: true
        ListView {
            id: trackList
            x: 10
            y: 10
            width: appUi.mobile ? panels.width - 20 : panels.width / 2 - 20
            height: panels.height - (appUi.mobile ? 82 : 20)
            model: backend.tracks
            clip: true
            reuseItems: true
            delegate: TrackRow {
                required property var track
                required property int index
                width: trackList.width
                ui: appUi
                row: track
                number: index + 1
            }
            ScrollBar.vertical: ScrollBar {}
        }
        Rectangle {
            visible: !appUi.mobile
            x: panels.width / 2
            height: panels.height
            width: 1
            color: appUi.borderColor
        }
        Rectangle {
            id: right
            x: appUi.mobile ? 0 : panels.width / 2 + 1
            y: appUi.mobile && !appUi.sheetOpen ? panels.height - 60 : 0
            width: appUi.mobile ? panels.width : panels.width / 2 - 1
            height: panels.height
            color: appUi.bg
            Behavior on y {
                NumberAnimation {
                    duration: 320
                    easing.type: Easing.InOutCubic
                }
            }
            Rectangle {
                visible: appUi.mobile
                width: parent.width
                height: 1
                color: appUi.borderColor
            }
            Button {
                id: sheetToggle
                visible: appUi.mobile
                width: parent.width
                height: appUi.mobile ? 36 : 0
                text: appUi.sheetOpen ? "⌄" : "⌃"
                contentItem: Text {
                    text: parent.text
                    color: appUi.muted
                    font.pixelSize: 20
                    horizontalAlignment: Text.AlignHCenter
                    verticalAlignment: Text.AlignVCenter
                }
                background: Item {}
                onClicked: appUi.sheetOpen = !appUi.sheetOpen
                Accessible.name: appUi.sheetOpen ? "Collapse results" : "Expand results"
            }
            RowLayout {
                id: progress
                x: 20
                y: appUi.mobile ? 36 : 10
                width: parent.width - 40
                height: appUi.mobile ? 24 : 26
                spacing: 10
                Text {
                    text: appUi.state.processing ? "Scanning " + (appUi.state.processed || 0) + " / " + appUi.total : appUi.total ? "Done · " + appUi.total + " tracks" : "No tracks"
                    color: appUi.muted
                    font.pixelSize: 11
                }
                Rectangle {
                    Layout.fillWidth: true
                    height: 2
                    color: appUi.surface2
                    radius: 2
                    Rectangle {
                        height: 2
                        width: parent.width * (appUi.state.processing ? Math.min(1, (appUi.state.processed || 0) / Math.max(1, appUi.total)) : 1)
                        color: appUi.accent
                        radius: 2
                        Behavior on width {
                            NumberAnimation {
                                duration: 350
                            }
                        }
                    }
                }
                Text {
                    visible: (appUi.state.foundCount || 0) > 0
                    text: (appUi.state.foundCount || 0) + " with hits"
                    color: appUi.accent
                    font.pixelSize: 11
                }
            }
            Rectangle {
                x: 10
                y: progress.y + progress.height
                width: parent.width - 20
                height: 1
                color: appUi.borderColor
            }
            ListView {
                id: results
                x: 10
                y: progress.y + progress.height + 10
                width: parent.width - 20
                height: Math.max(0, parent.height - y - 10)
                model: backend.cards
                spacing: 10
                clip: true
                delegate: ResultCard {
                    required property var track
                    width: results.width
                    ui: appUi
                    row: track
                }
                ScrollBar.vertical: ScrollBar {}
                Text {
                    visible: backend.cards.count === 0 && !appUi.state.processing
                    width: parent.width - 48
                    x: 24
                    y: 60
                    horizontalAlignment: Text.AlignHCenter
                    color: appUi.muted
                    opacity: 0.6
                    font.pixelSize: 13
                    text: appUi.total ? "No drug references found" : "No tracks. Press Fetch to load."
                }
            }
        }
        Rectangle {
            anchors.fill: parent
            color: appUi.bg
            visible: !!appUi.state.loading && !appUi.state.processing
            Text {
                anchors.centerIn: parent
                text: "Loading tracks..."
                color: appUi.muted
            }
        }
    }
    component PlayerSlider: Slider {
        id: playerSlider
        implicitHeight: 20
        background: Rectangle {
            x: playerSlider.leftPadding
            y: playerSlider.height / 2 - 2
            width: playerSlider.availableWidth
            height: 4
            radius: 2
            color: "#535353"
            Rectangle {
                width: parent.width * playerSlider.visualPosition
                height: parent.height
                radius: 2
                color: playerSlider.hovered || playerSlider.pressed ? "#1ed760" : "#ffffff"
            }
        }
        handle: Rectangle {
            x: playerSlider.leftPadding + playerSlider.visualPosition * (playerSlider.availableWidth - width)
            y: playerSlider.height / 2 - height / 2
            width: 12; height: 12; radius: 6
            color: "#ffffff"
            visible: playerSlider.hovered || playerSlider.pressed
        }
    }
    component PlayerButton: Button {
        id: playerButton
        property bool primary: false
        property string glyph: "play"
        implicitWidth: 36
        implicitHeight: 36
        HoverHandler { cursorShape: parent.enabled ? Qt.PointingHandCursor : Qt.ArrowCursor }
        opacity: enabled ? 1 : 0.35
        padding: primary ? 8 : 7
        contentItem: Image {
            source: "qrc:/assets/lucide-" + playerButton.glyph + ".svg"
            sourceSize.width: 20
            sourceSize.height: 20
            fillMode: Image.PreserveAspectFit
            layer.enabled: true
            layer.effect: MultiEffect {
                            brightness: 1
                            colorization: 1
                            colorizationColor: playerButton.primary ? "#000000" : playerButton.hovered ? "#ffffff" : "#b3b3b3"
                        }
        }
        background: Rectangle {
                    radius: width / 2
                    color: playerButton.primary ? "#ffffff" : "transparent"
                    scale: playerButton.hovered && playerButton.primary ? 1.06 : 1
                }
    }
    Rectangle {
        id: playerPanel
        objectName: "playerPanel"
        anchors.bottom: parent.bottom
        width: parent.width
        height: 88
        color: appUi.playerSurface
        enabled: musicPlayer.enabled
        Rectangle { width: parent.width; height: 1; color: "#29292d" }
        function timeLabel(ms) {
            const seconds = Math.floor(ms / 1000);
            return Math.floor(seconds / 60) + ":" + (seconds % 60 < 10 ? "0" : "") + seconds % 60;
        }
        Item {
            anchors.fill: parent
            anchors.margins: 16
            opacity: musicPlayer.enabled ? 1 : 0.4
            Rectangle {
                id: playerCover
                anchors.left: parent.left
                anchors.verticalCenter: parent.verticalCenter
                width: 52; height: 52
                radius: 8; color: "#171719"
                Image { anchors.fill: parent; source: musicPlayer.cover; fillMode: Image.PreserveAspectCrop }
                Text { anchors.centerIn: parent; visible: !musicPlayer.cover; text: "♪"; color: "#666666"; font.pixelSize: 24 }
            }
            ColumnLayout {
                anchors.left: playerCover.right
                anchors.leftMargin: 16
                anchors.verticalCenter: parent.verticalCenter
                width: Math.max(0, parent.width * 0.25 - 68)
                Text { Layout.fillWidth: true; text: musicPlayer.title || "DDetector"; color: "#f0f0f4"; elide: Text.ElideRight }
                Text {
                    Layout.fillWidth: true
                    text: musicPlayer.artist || (musicPlayer.enabled ? "Select a track · Play" : "Add a token in Settings")
                    color: "#888888"; font.pixelSize: 11; elide: Text.ElideRight
                }
            }
            ColumnLayout {
                objectName: "playerTimeline"
                anchors.centerIn: parent
                width: parent.width * 0.42
                spacing: 0
                RowLayout {
                    Layout.alignment: Qt.AlignHCenter
                    spacing: 12
                    PlayerButton { glyph: "skip-back"; Accessible.name: "Previous track"; onClicked: musicPlayer.previous() }
                    PlayerButton {
                        primary: true
                        implicitWidth: 32
                        implicitHeight: 32
                        glyph: musicPlayer.playing ? "pause" : "play"
                        Accessible.name: musicPlayer.playing ? "Pause" : "Play"
                        enabled: musicPlayer.enabled && !musicPlayer.busy && musicPlayer.title !== ""
                        onClicked: musicPlayer.toggle()
                    }
                    PlayerButton { glyph: "skip-forward"; Accessible.name: "Next track"; onClicked: musicPlayer.next() }
                }
                RowLayout {
                    Text { text: playerPanel.timeLabel(musicPlayer.position); color: "#888888"; font.pixelSize: 11 }
                    PlayerSlider {
                        Layout.fillWidth: true
                        from: 0; to: Math.max(1, musicPlayer.duration)
                        value: musicPlayer.position
                        enabled: musicPlayer.enabled && musicPlayer.duration > 0
                        Accessible.name: "Playback position"
                        onMoved: musicPlayer.seek(value)

                    }
                    Text { text: playerPanel.timeLabel(musicPlayer.duration); color: "#888888"; font.pixelSize: 11 }
                }
            }
            PlayerButton {
                anchors.right: volumeSlider.left
                anchors.rightMargin: 8
                anchors.verticalCenter: parent.verticalCenter
                glyph: "volume-2"; Accessible.name: "Mute"
                onClicked: musicPlayer.volume = musicPlayer.volume > 0 ? 0 : 0.7
            }
            PlayerSlider {
                id: volumeSlider
                anchors.right: parent.right
                anchors.verticalCenter: parent.verticalCenter
                width: 100
                from: 0; to: 1; value: musicPlayer.volume
                Accessible.name: "Volume"
                onMoved: musicPlayer.volume = value

            }
        }
    }
    SiteView {
        anchors.top: mainHeader.bottom
        anchors.bottom: playerPanel.top
        width: parent.width
        visible: !appUi.privileged
        ui: appUi
        onSignIn: auth.open()
    }
    Rectangle {
        visible: appUi.privileged && (!!appUi.state.error || backend.connectionError !== "")
        anchors.horizontalCenter: parent.horizontalCenter
        anchors.bottom: parent.bottom
        anchors.bottomMargin: playerPanel.height + 12
        width: Math.min(parent.width - 24, errorLabel.implicitWidth + 32)
        height: errorLabel.implicitHeight + 20
        color: appUi.surface2
        border.color: "#66dc5050"
        radius: 8
        Text {
            id: errorLabel
            anchors.centerIn: parent
            width: parent.width - 32
            wrapMode: Text.Wrap
            text: backend.connectionError || appUi.state.error || ""
            color: appUi.ignoredColor
            font.pixelSize: 12
        }
    }
    Popup {
        id: auth
        objectName: "authPopup"
        anchors.centerIn: Overlay.overlay
        width: Math.min(appUi.width - 32, 400)
        height: appUi.width <= 480 ? 320 : 368
        modal: true
        focus: true
        padding: 0
        closePolicy: Popup.CloseOnEscape | Popup.CloseOnPressOutside
        background: Rectangle {
            radius: 20
            color: appUi.surface
            border.color: appUi.borderColor
        }
        Overlay.modal: Rectangle {
            color: "#99000000"
        }
        contentItem: Item {
            Image {
                visible: false
                x: 1
                y: 1
                width: parent.width * 0.474
                height: parent.height - 2
                source: "qrc:/assets/Kagami.webp"
                fillMode: Image.PreserveAspectCrop
                clip: true
            }
            Column {
                x: 24
                width: parent.width - 48
                anchors.verticalCenter: parent.verticalCenter
                spacing: 10
                Text {
                    text: "Sign in"
                    color: appUi.ink
                    font.family: "DDetector Quicksand"
                    font.weight: Font.Bold
                    font.pixelSize: appUi.width <= 480 ? 34 : 40
                    bottomPadding: 18
                }
                Repeater {
                    model: [
                        {
                            provider: "github",
                            name: "GitHub"
                        },
                        {
                            provider: "discord",
                            name: "Discord"
                        },
                        {
                            provider: "spotify",
                            name: "Spotify"
                        }
                    ]
                    SmallButton {
                        id: providerButton
                        required property var modelData
                        width: parent.width
                        height: 46
                        text: backend.pendingProvider === modelData.provider ? "Redirecting…" : "Continue with " + modelData.name
                        enabled: !appUi.state.loginPending && backend.pendingProvider === ""
                        font.family: "DDetector Quicksand"
                        font.pixelSize: appUi.width <= 480 ? 12 : 14
                        font.weight: Font.DemiBold
                        onClicked: backend.login(modelData.provider)
                        contentItem: Row {
                            spacing: 10
                            leftPadding: Math.max(0, (providerButton.width - providerText.implicitWidth - 27) / 2 - providerButton.leftPadding)
                            Image {
                                width: 17
                                height: 17
                                anchors.verticalCenter: parent.verticalCenter
                                source: "qrc:/assets/" + providerButton.modelData.provider + "-dark" + ".svg"
                            }
                            Text {
                                id: providerText
                                text: providerButton.text
                                font: providerButton.font
                                color: appUi.ink
                                anchors.verticalCenter: parent.verticalCenter
                            }
                        }
                        background: Rectangle {
                            radius: 10
                            color: providerButton.hovered ? appUi.accentBg : appUi.surface2
                            border.color: providerButton.hovered ? appUi.accentBorder : appUi.borderColor
                        }
                    }
                }
                Text {
                    width: parent.width
                    wrapMode: Text.Wrap
                    text: appUi.state.error || ""
                    visible: text !== ""
                    color: appUi.ignoredColor
                    font.pixelSize: 13
                }
            }
            SmallButton {
                x: parent.width - 46
                y: 14
                width: 32
                height: 32
                text: "×"
                onClicked: auth.close()
                Accessible.name: "Close"
            }
        }
    }
    Rectangle {
        z: 500
        visible: appUi.toastText !== ""
        y: 9
        anchors.horizontalCenter: parent.horizontalCenter
        width: Math.min(parent.width - 20, toastLabel.implicitWidth + 32)
        height: toastLabel.implicitHeight + 16
        radius: 4
        color: appUi.surface2
        border.color: appUi.borderColor
        Text {
            id: toastLabel
            anchors.centerIn: parent
            text: appUi.toastText
            color: appUi.ink
            font.pixelSize: 12
        }
    }
}

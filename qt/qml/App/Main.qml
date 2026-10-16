pragma ComponentBehavior: Bound
import QtQuick
import QtQuick.Controls
import QtQuick.Layouts
import QtQuick.Effects
import DDetector 1.0

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
    title: "DDetector"
    color: bg
    font.family: "DDetector Nunito"
    font.pixelSize: 13
    font.weight: Font.Bold
    property var backendBridge: null
    property var config: null
    property var player: null
    property var uploader: null
    property var workspace: null
    property string gitTag: "dev"
    property bool developmentToolsEnabled: false
    property var updater: null
    property int rightPage: 0
    property bool gitExpanded: false
    property bool workspaceCloseApproved: false
    property int activeModalCount: 0
    readonly property bool raiseWindowChrome: activeModalCount > 0
    Shortcut {
        sequences: [StandardKey.Paste]
        context: Qt.WindowShortcut
        enabled: !!appUi.player && appUi.player.enabled && !appUi.player.busy
            && appUi.activeModalCount === 0 && !appUi.gitExpanded
            && !(appUi.activeFocusItem instanceof TextInput)
            && !(appUi.activeFocusItem instanceof TextEdit)
        onActivated: appUi.player.playFromClipboard()
    }
    onClosing: function(event) {
        if (appUi.updater && appUi.updater.busy) {
            event.accepted = false;
            notify("Wait for the update operation to finish before closing");
            return;
        }
        if (!!appUi.uploader && appUi.uploader.busy) {
            event.accepted = false;
            notify("Wait for Send to finish before closing");
            return;
        }
        if (workspaceCloseApproved || !workspaceLoader.item) return;
        event.accepted = false;
        (workspaceLoader.item as GitWorkspace).requestClose(function() {
            workspaceCloseApproved = true;
            Qt.callLater(function() { appUi.close(); });
        });
    }
    property color bg: "#000000"
    property color playerSurface: "#0c0c0e"
    property color surface: playerSurface
    property color surface2: "#171719"
    property color borderColor: "#3397979f"
    property color ink: "#f0f0f4"
    property color muted: "#999999"
    property color accent: "#2bfef5"
    property color accentBg: "#142bfef5"
    property color accentBorder: "#332bfef5"
    property color ignoredColor: "#dc5050"
    property var state: appUi.backendBridge.state
    property bool privileged: !!state.authenticated && !!state.hasAccess && !state.isBanned
        readonly property bool checkingAccess: (state.authenticated === undefined || (!!state.authenticated && !state.accessChecked && !state.isBanned)) && !state.error && appUi.backendBridge.connectionError === ""
        readonly property bool showShell: true
        readonly property bool tracksPending: checkingAccess || (!!state.loading && !state.processing && total === 0)
    property string selectedId: ""
    property var expanded: ({})
    property var menuTrack: ({})
    property bool menuFullLyrics: false
    property bool menuFromPlayer: false
    property var toastQueue: []
    property int nextToastId: 0
    property string stateError: state.error || ""
    onStateErrorChanged: { if (privileged && stateError) notify(stateError); }
    property int total: state.trackCount || 0
        property var userMetadata: (state.user || {}).user_metadata || ({})
        property string userName: userMetadata.full_name || userMetadata.name || userMetadata.preferred_username || (state.user || {}).email || "Account"
    property string avatarUrl: preferredAvatar(state.user || ({}))
    function preferredAvatar(user) {
        const providers = ["github", "discord", "spotify"];
        const identities = (user.identities || []).filter(identity => providers.indexOf(identity.provider) >= 0);
        for (const provider of providers) {
            for (const identity of identities) {
                if (identity.provider !== provider) continue;
                const data = identity.identity_data || ({});
                for (const value of [data.avatar_url, data.picture]) {
                    if (typeof value === "string" && value.trim()) return value.trim();
                }
            }
        }
        if (!identities.length) {
            const metadata = user.user_metadata || ({});
            for (const value of [metadata.avatar_url, metadata.picture]) {
                if (typeof value === "string" && value.trim()) return value.trim();
            }
        }
        return "";
    }

    function toggleExpanded(id) {
        let next = Object.assign({}, expanded);
        next[id] = !next[id];
        expanded = next;
    }
    function choose(track) {
        appUi.backendBridge.copyId(track.id);
        selectedId = track.id;
        const index = appUi.backendBridge.cards.indexOf(track.id);
        if (index >= 0) {
            results.positionViewAtIndex(index, ListView.Contain);
        }
    }
    function playTrack(track) {
        if (appUi.player.enabled && !appUi.player.busy)
            appUi.player.playTrack(track.id);
    }
    function context(track, source, x, y, fullLyrics) {
        menuTrack = track;
        menuFullLyrics = fullLyrics;
        menuFromPlayer = source === playerPanel;
        const p = source.mapToItem(appUi.contentItem, x, y);
        trackMenu.x = Math.min(p.x, appUi.width - trackMenu.width - 8);
        trackMenu.y = Math.min(p.y, appUi.height - trackMenu.height - 8);
        trackMenu.open();
    }
    function notify(message) {
        if (!message || message === "Yandex Music token required") return;
        const normalized = String(message).trim().toLowerCase();
        if (normalized === "operation in progress" || normalized === "another operation is running") return;
        for (let i = 0; i < toastModel.count; ++i) {
            if (toastModel.get(i).message === message) return;
        }
        if (toastQueue.indexOf(message) < 0) toastQueue.push(message);
        fillToastSlots();
    }
    function fillToastSlots() {
        while (toastModel.count < 5 && toastQueue.length)
            toastModel.insert(0, { toastId: nextToastId++, message: toastQueue.shift() });
    }
    function removeToast(toastId) {
        for (let i = 0; i < toastModel.count; ++i) {
            if (toastModel.get(i).toastId === toastId) {
                toastModel.remove(i);
                break;
            }
        }
        fillToastSlots();
    }
    Connections {
        target: appUi.backendBridge
        function onToast(message) {
            appUi.notify(message);
        }
        function onConnectionErrorChanged() {
            if (appUi.backendBridge.connectionError) appUi.notify(appUi.backendBridge.connectionError);
        }
        function onStateChanged() {
            if (!appUi.privileged) {
                appUi.selectedId = "";
                appUi.expanded = ({});
                trackMenu.close();
            }
            if (appUi.state.authenticated === false)
                auth.open();
            else if (appUi.state.authenticated)
                auth.close();
        }
    }
    Connections {
        target: appUi.config
        function onErrorChanged() { if (appUi.config.error) appUi.notify(appUi.config.error); }
    }
    Binding {
        target: appUi.player
        property: "visualizerEnabled"
        value: appUi.config.visualizerEnabled
    }
    Connections {
        id: playerConnections
        target: appUi.player
        property string lastError: ""
        function onStateChanged() {
            if (appUi.player.error && appUi.player.error !== playerConnections.lastError)
                appUi.notify(appUi.player.error);
            playerConnections.lastError = appUi.player.error;
        }
    }
    ListModel { id: toastModel; objectName: "notificationModel" }

    component SmallButton: SiteButton {
        implicitHeight: 28
        padding: 6
        leftPadding: 12
        rightPadding: 12
        textColor: appUi.muted
        hoverTextColor: appUi.accent
        surfaceColor: appUi.surface
    }
    SiteMenu {
        id: trackMenu
        width: 190
        surfaceColor: appUi.surface2
        borderColor: appUi.borderColor
        Control {
            width: trackMenu.availableWidth
            padding: 6
            implicitHeight: contentItem.implicitHeight + topPadding + bottomPadding
            contentItem: Column {
                spacing: 2
                Text {
                    textFormat: Text.PlainText
                    width: parent.width
                    text: appUi.menuTrack.title || ""
                    color: appUi.ink
                    font.pixelSize: 12
                    elide: Text.ElideRight
                }
                Text {
                    textFormat: Text.PlainText
                    width: parent.width
                    text: appUi.menuTrack.artist || ""
                    color: appUi.muted
                    font.pixelSize: 11
                    elide: Text.ElideRight
                }
            }
        }
        SiteMenuSeparator { borderColor: appUi.borderColor }
        SiteMenuItem {
            textColor: appUi.ink
            mutedColor: appUi.muted
            accentColor: appUi.accent
            accentBackground: appUi.accentBg
            dangerColor: appUi.ignoredColor
            objectName: "playTrackEntry"
            visible: !appUi.menuFromPlayer
            height: visible ? implicitHeight : 0
            text: "Play"
            glyph: "play"
            green: true
            enabled: appUi.player.enabled && !appUi.player.busy
            onTriggered: appUi.player.playTrack(appUi.menuTrack.id)
        }
        SiteMenuSeparator {
            borderColor: appUi.borderColor
            visible: !appUi.menuFromPlayer
            height: visible ? implicitHeight : 0
        }
        SiteMenuItem {
            textColor: appUi.ink
            mutedColor: appUi.muted
            accentColor: appUi.accent
            accentBackground: appUi.accentBg
            dangerColor: appUi.ignoredColor
            text: "Search on Yandex"
            glyph: "search"
            onTriggered: appUi.backendBridge.searchYandex(appUi.menuTrack.title, appUi.menuTrack.artist)
        }
        SiteMenuItem {
            textColor: appUi.ink
            mutedColor: appUi.muted
            accentColor: appUi.accent
            accentBackground: appUi.accentBg
            dangerColor: appUi.ignoredColor
            text: "Copy track ID"
            glyph: "copy"
            onTriggered: appUi.backendBridge.copyId(appUi.menuTrack.id)
        }
        SiteMenuItem {
            textColor: appUi.ink
            mutedColor: appUi.muted
            accentColor: appUi.accent
            accentBackground: appUi.accentBg
            dangerColor: appUi.ignoredColor
            glyph: "maximize"
            visible: appUi.menuFullLyrics
            height: visible ? implicitHeight : 0
            text: appUi.expanded[appUi.menuTrack.id] ? "Collapse" : "Full lyrics"
            onTriggered: appUi.toggleExpanded(appUi.menuTrack.id)
        }
        SiteMenuSeparator { borderColor: appUi.borderColor }
        SiteMenuItem {
            textColor: appUi.ink
            mutedColor: appUi.muted
            accentColor: appUi.accent
            accentBackground: appUi.accentBg
            dangerColor: appUi.ignoredColor
            text: appUi.menuTrack.ignored ? "Unignore" : "Ignore"
            glyph: appUi.menuTrack.ignored ? "shield-check" : "shield"
            danger: !appUi.menuTrack.ignored
            onTriggered: appUi.backendBridge.ignore(appUi.menuTrack.id, !appUi.menuTrack.ignored)
        }
    }
    Loader {
        id: devToolsLoader
        active: !!appUi.developmentToolsEnabled
        Component.onCompleted: setSource("qrc:/DevTools.qml", { updater: Qt.binding(function() { return appUi.updater; }) })
        onLoaded: headerMenu.insertItem(0, item as SiteMenuItem)
    }
    SiteMenu {
        id: headerMenu
        objectName: "accountMenu"
        width: 210
        surfaceColor: appUi.surface2
        borderColor: appUi.borderColor
        SiteMenuSeparator {
            objectName: "accountDevMenuSeparator"
            visible: appUi.developmentToolsEnabled
            height: visible ? implicitHeight : 0
            borderColor: appUi.borderColor
        }
        SiteMenuItem {
            textColor: appUi.ink
            mutedColor: appUi.muted
            accentColor: appUi.accent
            accentBackground: appUi.accentBg
            dangerColor: appUi.ignoredColor
            objectName: "accountSettingsEntry"
            glyph: "settings"
            text: "Settings"
            onTriggered: settingsDialog.open()
        }

        SiteMenuItem {
            textColor: appUi.ink
            mutedColor: appUi.muted
            accentColor: appUi.accent
            accentBackground: appUi.accentBg
            dangerColor: appUi.ignoredColor
            text: "Sign out"
            glyph: "log-out"
            danger: true
            enabled: !!appUi.state.authenticated
            onTriggered: appUi.backendBridge.logout()
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
        id: mainHeader
        visible: appUi.showShell || !!appUi.state.authenticated
        z: 10
        width: parent.width
        y: 0
        height: !visible ? 0 : 48
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
            x: 16
            y: 10
            height: 32
            spacing: 10
            Item {
                id: windowTitleSlot
                width: windowTitle.width
                height: 32
                Text {
                    id: windowTitle
                    objectName: "windowTitle"
                    parent: appUi.raiseWindowChrome ? Overlay.overlay : windowTitleSlot
                    x: appUi.raiseWindowChrome ? 16 : 0
                    y: appUi.raiseWindowChrome ? (mainHeader.height - height) / 2 + 2 : (windowTitleSlot.height - height) / 2
                    z: 40
                    text: "D<span style='color:" + appUi.accent + "'>Detector</span> <span style='color:white'>" + appUi.gitTag + "</span>"
                    textFormat: Text.RichText
                    color: appUi.ink
                    font.family: "Luckystar"
                    font.pixelSize: 28
                    font.weight: Font.Normal
                    MouseArea {
                        anchors.fill: parent
                        enabled: appUi.raiseWindowChrome
                        onPressed: appUi.startSystemMove()
                        onDoubleClicked: appUi.toggleMaximized()
                    }
                }
            }

            Text {
                textFormat: Text.PlainText
                visible: appUi.showShell
                text: appUi.backendBridge.tracks.count + " tracks"
                color: appUi.muted
                font.pixelSize: 12
                anchors.verticalCenter: parent.verticalCenter
            }
        }
        RowLayout {
            anchors.right: parent.right
            anchors.rightMargin: appUi.config.hideWindowControls ? 8 : windowControls.width + 8
            visible: appUi.showShell
            enabled: appUi.privileged
            y: 9
            width: parent.width - (appUi.config.hideWindowControls ? 0 : windowControls.width) - 238
            height: 30
            spacing: 8
            Item {
                Layout.fillWidth: true
            }
            ComboBox {
                id: sorting
                Accessible.name: "Sort tracks"
                SiteFocusRing { active: sorting.visualFocus }
                objectName: "sortSelect"
                HoverHandler { cursorShape: parent.enabled ? Qt.PointingHandCursor : Qt.ArrowCursor }
                Layout.preferredWidth: 100
                Layout.preferredHeight: 30
                model: ["Default", "By date", "A - Z", "By artist"]
                onActivated: appUi.backendBridge.setSort(currentIndex)
                font.pixelSize: 12
                contentItem: Text { textFormat: Text.PlainText;
                    text: sorting.displayText
                    font: sorting.font
                    color: appUi.ink
                    verticalAlignment: Text.AlignVCenter
                    horizontalAlignment: Text.AlignLeft
                    leftPadding: 12
                    rightPadding: 30
                }
                background: Rectangle {
                    color: appUi.surface
                    radius: 6
                    border.color: sorting.popup.visible || sorting.hovered || sorting.activeFocus ? appUi.accentBorder : appUi.borderColor
                }
                indicator: Image {
                    width: 14; height: 14
                    x: sorting.width - width - 10
                    y: (sorting.height - height) / 2
                    source: "qrc:/assets/lucide-chevron-down.svg"
                    rotation: sorting.popup.visible ? 180 : 0
                    Behavior on rotation { NumberAnimation { duration: 200 } }
                    layer.enabled: true
                    layer.effect: MultiEffect {
                        brightness: 1
                        colorization: 1
                        colorizationColor: sorting.popup.visible ? appUi.accent : appUi.muted
                    }
                }
                delegate: ItemDelegate {
                    id: sortOption
                    required property int index
                    required property string modelData
                    width: sorting.popup.availableWidth
                    implicitHeight: 34
                    padding: 12
                    topPadding: 7
                    bottomPadding: 7
                    highlighted: sorting.highlightedIndex === index
                    HoverHandler { cursorShape: Qt.PointingHandCursor }
                    contentItem: RowLayout {
                        spacing: 8
                        Text {
                            textFormat: Text.PlainText
                            Layout.fillWidth: true
                            text: sortOption.modelData
                            font.family: "DDetector Nunito"
                            font.pixelSize: 13
                            font.weight: Font.Bold
                            color: sortOption.index === sorting.currentIndex || sortOption.hovered || sortOption.highlighted ? appUi.accent : appUi.ink
                        }
                        Image {
                            visible: sortOption.index === sorting.currentIndex
                            Layout.preferredWidth: 16
                            Layout.preferredHeight: 16
                            source: "qrc:/assets/lucide-check.svg"
                            layer.enabled: true
                            layer.effect: MultiEffect { brightness: 1; colorization: 1; colorizationColor: appUi.accent }
                        }
                    }
                    background: Rectangle {
                        radius: 6
                        color: sortOption.hovered || sortOption.highlighted ? appUi.accentBg : "transparent"
                        Behavior on color { ColorAnimation { duration: 150 } }
                    }
                }
                popup: Popup {
                    objectName: "sortPopup"
                    x: (sorting.width - width) / 2
                    y: sorting.height + 6
                    width: Math.max(sorting.width, 140)
                    padding: 8
                    background: Rectangle {
                        color: appUi.surface
                        border.color: appUi.borderColor
                        radius: 12
                    }
                    contentItem: ListView {
                        implicitHeight: Math.min(contentHeight, 360, appUi.height * 0.6)
                        model: sorting.popup.visible ? sorting.delegateModel : null
                        currentIndex: sorting.highlightedIndex
                        clip: true
                        ScrollBar.vertical: ScrollBar {}
                    }
                }
            }
            SiteTextField {
                id: search
                Accessible.name: "Search tracks; press Enter to apply"
                Layout.fillWidth: true
                Layout.maximumWidth: 300
                Layout.preferredHeight: 30
                focusRingEnabled: false
                topPadding: 0
                bottomPadding: 0
                verticalAlignment: TextInput.AlignVCenter
                placeholderText: "Search (Enter)"
                color: appUi.ink
                placeholderTextColor: "#999999"
                font.pixelSize: 13
                leftPadding: 12
                rightPadding: 12
                selectByMouse: true
                onAccepted: appUi.backendBridge.setSearch(text)
                background: Rectangle {
                    color: appUi.surface
                    radius: 16
                    border.color: search.activeFocus ? appUi.accent : appUi.borderColor
                }
            }
            SmallButton {
                Layout.preferredWidth: 100
                Layout.preferredHeight: 30
                text: "Fetch"
                loading: !!appUi.state.fetching
                enabled: !loading && !appUi.state.loading
                onClicked: appUi.backendBridge.fetch()
                SiteToolTip { text: "Rebuild track list from external source" }
            }
        }
    }
    Column {
        anchors.right: parent.right
        anchors.rightMargin: 8
        anchors.bottom: avatarButton.top
        anchors.bottomMargin: 12
        spacing: 8
        z: 40
        visible: appUi.showShell
        SiteButton {
            objectName: "updateDownloadButton"
            width: 32; height: 32
            visible: !!appUi.updater && appUi.updater.available
            text: "Download update"
            glyph: "download"
            glyphSize: 22
            iconOnly: true
            textColor: "#4ade80"
            hoverTextColor: textColor
            accentColor: textColor
            surfaceColor: "transparent"
            hoverColor: appUi.surface2
            borderColor: "transparent"
            loading: !!appUi.updater && appUi.updater.busy
            circularProgress: true
            progress: appUi.updater ? appUi.updater.progress : -1
            SiteToolTip {
                text: !appUi.updater ? "Update" : appUi.updater.busy
                    ? appUi.updater.status + (appUi.updater.progress >= 0 ? " · " + Math.round(appUi.updater.progress * 100) + "%" : "")
                    : appUi.updater.error || "Update to " + appUi.updater.version
            }
            onClicked: {
                if ((!!appUi.uploader && appUi.uploader.busy)
                    || (workspaceLoader.item && appUi.workspace.dirty)) {
                    appUi.notify("Finish Send and save workspace changes before updating");
                    return;
                }
                appUi.updater.install();
            }
        }
    }
    Connections {
        target: appUi.updater
        ignoreUnknownSignals: true
        property bool wasBusy: false
        function onChanged() {
            if (wasBusy && !appUi.updater.busy && appUi.updater.error)
                appUi.notify(appUi.updater.error);
            wasBusy = appUi.updater.busy;
        }
    }
    Row {
        id: windowControls
        parent: appUi.raiseWindowChrome ? Overlay.overlay : appUi.contentItem
        anchors.right: parent.right
        anchors.rightMargin: appUi.config.hideWindowControls ? 8 : 0
        y: 0
        z: 40
        spacing: 0
        height: 48
        SmallButton {
            id: avatarButton
            objectName: "avatarButton"
            parent: appUi.contentItem
            z: 40
            visible: !!appUi.state.authenticated
            width: 32; height: 32
            anchors.right: parent.right
            anchors.rightMargin: 8
            anchors.bottom: playerPanel.top
            anchors.bottomMargin: 8
            padding: 0
            leftPadding: 0
            rightPadding: 0
            background: Rectangle {
                radius: width / 2
                color: avatarButton.hovered ? appUi.accentBg : appUi.surface
                border.color: avatarButton.hovered ? appUi.accentBorder : appUi.borderColor
            }
            Accessible.name: "Account menu for " + appUi.userName
            SiteToolTip { text: appUi.userName }
            TapHandler {
                acceptedButtons: Qt.RightButton
                onTapped: avatarButton.clicked()
            }
            onClicked: {
                headerMenu.x = appUi.width - headerMenu.width - 10;
                const point = avatarButton.mapToItem(appUi.contentItem, 0, 0);
                headerMenu.y = Math.max(8, point.y - headerMenu.implicitHeight - 8);
                headerMenu.open();
            }
            contentItem: Item {
                id: avatarContent
                clip: true
                Text {
                    textFormat: Text.PlainText
                    anchors.centerIn: parent
                    text: appUi.userName.charAt(0).toUpperCase()
                    color: appUi.ink
                    visible: !avatarImage.ready
                }
                AvatarImage {
                    id: avatarImage
                    objectName: "accountAvatarImage"
                    anchors.fill: avatarContent
                    source: appUi.avatarUrl
                }

            }
        }
        Item { width: 0; height: 48 }
        WindowButton { targetWindow: appUi; index: 0; visible: !appUi.config.hideWindowControls; anchors.verticalCenter: parent.verticalCenter }
        WindowButton { targetWindow: appUi; index: 1; visible: !appUi.config.hideWindowControls; anchors.verticalCenter: parent.verticalCenter }
        WindowButton { targetWindow: appUi; index: 2; visible: !appUi.config.hideWindowControls; anchors.verticalCenter: parent.verticalCenter }
    }
    SiteDialog {
        id: settingsDialog
        objectName: "settingsDialog"
        anchors.centerIn: Overlay.overlay
        width: Math.min(appUi.width - 32, 560)
        icon: "qrc:/assets/lucide-settings.svg"
        title: "Settings"
        surface: appUi.surface
        surface2: appUi.surface2
        borderColor: appUi.borderColor
        ink: appUi.ink
        muted: appUi.muted
        accentBg: appUi.accentBg
        accentBorder: appUi.accentBorder
        closeButtonObjectName: "closeSettingsButton"
        closeButtonAccessibleName: "Close settings"
        contentItem: ColumnLayout {
            spacing: 8
            SiteSettingsCard {
                Layout.fillWidth: true
                title: "Git repository"
                glyph: "git-branch"
                SiteTextField {
                        id: repositoryPathInput
                        font.pixelSize: appUi.font.pixelSize
                        Accessible.name: "Git repository path"
                        Layout.fillWidth: true
                        implicitHeight: 36
                        leftPadding: 12
                        rightPadding: 12
                        text: ""
                        Component.onCompleted: text = appUi.config.repositoryPath || ""
                        Connections {
                            target: appUi.config
                            function onRepositoryPathChanged() {
                                if (!repositoryPathInput.activeFocus)
                                    repositoryPathInput.text = appUi.config.repositoryPath || "";
                            }
                        }
                        placeholderText: Qt.platform.os === "windows" ? "D:/FckCensorData" : "/run/media/admin/DATA/FckCensorData/"
                        color: appUi.ink
                        placeholderTextColor: appUi.muted
                        selectionColor: appUi.accentBg
                        selectedTextColor: appUi.ink
                        selectByMouse: true
                        enabled: !appUi.uploader || !appUi.uploader.busy
                        objectName: "repositoryPathInput"
                        function applyPath(automatic = false) {
                            const path = text.trim();
                            if (!!appUi.uploader && appUi.uploader.busy) return;
                            if (automatic && (!appUi.config.repositoryDirectoryExists(path)
                                || (!!appUi.workspace && (appUi.workspace.dirty || appUi.workspace.terminalRunning)))) return;
                            if (path === appUi.config.repositoryPath || changeRepositoryDialog.visible) return;
                            if (!!appUi.workspace && appUi.workspace.dirty) {
                                appUi.notify("Save or discard file changes before changing the repository");
                                return;
                            }
                            if (!!appUi.workspace && appUi.workspace.terminalRunning) {
                                changeRepositoryDialog.pendingPath = path;
                                changeRepositoryDialog.open();
                            } else {
                                appUi.config.repositoryPath = path;
                                if (appUi.config.repositoryPath !== path)
                                    text = appUi.config.repositoryPath || "";
                            }
                        }
                        onTextEdited: repositoryApplyTimer.restart()
                        onEditingFinished: { repositoryApplyTimer.stop(); applyPath(); }
                        onAccepted: { repositoryApplyTimer.stop(); applyPath(); }
                        Timer {
                            id: repositoryApplyTimer
                            interval: 500
                            repeat: false
                            onTriggered: repositoryPathInput.applyPath(true)
                        }

                }
            }
            SiteSettingsCard {
                objectName: "musicTokenCard"
                Layout.fillWidth: true
                title: "Yandex Music"
                glyph: "music-standard"
                SiteTextField {
                        id: musicToken
                        font.pixelSize: appUi.font.pixelSize
                        Accessible.name: "Yandex Music OAuth token"
                        objectName: "musicTokenSetting"
                        Layout.fillWidth: true
                        implicitHeight: 36
                        leftPadding: 12
                        rightPadding: 12
                        placeholderText: "OAuth token"
                        placeholderTextColor: appUi.muted
                        echoMode: TextInput.Password
                        selectByMouse: true
                        color: appUi.ink
                        selectionColor: appUi.accentBg
                        selectedTextColor: appUi.ink

                        text: appUi.config.musicToken
                        onTextEdited: tokenApplyTimer.restart()
                        onAccepted: { tokenApplyTimer.stop(); appUi.config.musicToken = text.trim(); }
                        onEditingFinished: { tokenApplyTimer.stop(); appUi.config.musicToken = text.trim(); }
                        Timer {
                            id: tokenApplyTimer
                            interval: 800
                            onTriggered: appUi.config.musicToken = musicToken.text.trim()
                        }
                }
            }
            Rectangle {
                objectName: "hideIgnoredCard"
                Layout.fillWidth: true
                implicitHeight: 56
                radius: 14
                color: appUi.surface2
                border.color: appUi.borderColor
                SiteToggleRow {
                    id: hideIgnoredToggle
                    objectName: "hideIgnoredSetting"
                    anchors.fill: parent
                    text: "Hide ignored tracks"
                    glyph: "shield"
                    checked: appUi.backendBridge.hideIgnored
                    onToggled: appUi.backendBridge.hideIgnored = checked
                }
            }
            Rectangle {
                Layout.fillWidth: true
                implicitHeight: 56
                radius: 14
                color: appUi.surface2
                border.color: appUi.borderColor
                SiteToggleRow {
                    id: hideWindowControlsToggle
                    objectName: "hideWindowControlsSetting"
                    anchors.fill: parent
                    text: "Hide window controls"
                    glyph: "panel-top"
                    checked: appUi.config.hideWindowControls
                    onToggled: appUi.config.hideWindowControls = checked
                }
            }
                        Rectangle {
                Layout.fillWidth: true
                implicitHeight: 56
                radius: 14
                color: appUi.surface2
                border.color: appUi.borderColor
                SiteToggleRow {
                    objectName: "visualizerSetting"
                    anchors.fill: parent
                    text: "Spectral visualizer"
                    glyph: "music-standard"
                    checked: appUi.config.visualizerEnabled
                    onToggled: appUi.config.visualizerEnabled = checked
                }
            }
            Rectangle {
                Layout.fillWidth: true
                implicitHeight: 56
                radius: 14
                color: appUi.surface2
                border.color: appUi.borderColor
                SiteToggleRow {
                    objectName: "approveSoundSetting"
                    anchors.fill: parent
                    text: "Approve sound"
                    glyph: "volume-2"
                    checked: appUi.config.approveSoundEnabled
                    onToggled: appUi.config.approveSoundEnabled = checked
                }
            }
        }
    }
    SiteDialog {
        id: changeRepositoryDialog
        property string pendingPath: ""
        anchors.centerIn: Overlay.overlay
        title: "Change Git repository?"
        icon: "qrc:/assets/lucide-folder.svg"
        width: Math.min(appUi.width - 32, 400)
        surface: appUi.surface
        surface2: appUi.surface2
        borderColor: appUi.borderColor
        ink: appUi.ink
        muted: appUi.muted
        accentBg: appUi.accentBg
        accentBorder: appUi.accentBorder
        onCloseRequested: reject()
        contentItem: ColumnLayout {
            spacing: 16
            SiteQuote {
                Layout.fillWidth: true
                text: "The current terminal will be stopped, including any running commands. Continue?"
            }
            RowLayout {
                Layout.fillWidth: true
                Item { Layout.fillWidth: true }
                SiteButton { text: "No"; onClicked: changeRepositoryDialog.reject() }
                SiteButton { text: "Yes"; accented: true; onClicked: changeRepositoryDialog.accept() }
            }
        }
        onAccepted: {
            if (appUi.workspace.dirty || (!!appUi.uploader && appUi.uploader.busy)) {
                appUi.notify("Save file changes and wait for Send before changing the repository");
                return;
            }
            appUi.workspace.stopTerminal();
            appUi.config.repositoryPath = pendingPath;
        }
        onRejected: repositoryPathInput.text = appUi.config.repositoryPath
    }

    Item {
        id: panels
        readonly property real leftPanelWidth: width * 0.40
        visible: appUi.showShell
        enabled: appUi.privileged
        anchors.top: mainHeader.bottom
        anchors.bottom: playerPanel.top
        width: parent.width
        clip: true
        ListView {
            id: trackList
            visible: !(appUi.rightPage === 1 && appUi.gitExpanded)
            x: 10
            y: 10
            width: panels.leftPanelWidth - 20
            height: panels.height - 20
            model: appUi.showShell ? appUi.backendBridge.tracks : null
            cacheBuffer: 0
            topMargin: 6
            bottomMargin: 6
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
        SiteSpinner {
            objectName: "tracksLoadingOverlay"
            anchors.centerIn: trackList
            visible: appUi.tracksPending
            accentColor: appUi.accent
            label: appUi.checkingAccess ? "Checking access" : "Loading tracks"
        }
        ScrollEdgeShadow {
            anchors.fill: trackList
            flickable: trackList
        }
        Rectangle {
            x: panels.leftPanelWidth
            height: panels.height
            width: 1
            color: appUi.borderColor
        }
        Rectangle {
            id: rightPanel
            z: 2
            x: appUi.rightPage === 1 && appUi.gitExpanded ? 0 : panels.leftPanelWidth + 1
            y: 0
            width: panels.width - x
            height: panels.height
            color: appUi.bg
            MouseArea {
                anchors.fill: parent
                acceptedButtons: Qt.AllButtons
            }
            Rectangle {
                anchors.fill: activityBar
                color: appUi.surface
                Rectangle {
                    anchors.left: parent.left
                    width: 1
                    height: parent.height
                    color: appUi.borderColor
                    z: 1
                }
            }
            Column {
                id: activityBar
                anchors.right: parent.right
                anchors.top: parent.top
                anchors.bottom: parent.bottom
                width: 48
                spacing: 0

                Repeater {
                    model: ["music", "git-branch", "search"]
                    Button {
                        id: activityButton
                        SiteFocusRing { active: activityButton.visualFocus }
                        objectName: index === 0 ? "lyricsPageButton" : index === 1 ? "gitPageButton" : "censorPageButton"
                        required property string modelData
                        required property int index
                        width: 48; height: 48
                        HoverHandler {
                            cursorShape: appUi.rightPage === activityButton.index ? Qt.ArrowCursor : Qt.PointingHandCursor
                        }
                        padding: 0
                        contentItem: Item {
                            Image {
                                anchors.centerIn: parent
                                width: 24; height: 24
                                source: "qrc:/assets/lucide-" + activityButton.modelData + ".svg"
                                layer.enabled: true
                                layer.effect: MultiEffect { brightness: 1; colorization: 1; colorizationColor: appUi.rightPage === activityButton.index ? appUi.accent : activityButton.hovered ? appUi.ink : appUi.muted }
                            }
                            Label {
                                objectName: "censorBetaLabel"
                                visible: activityButton.index === 2
                                anchors.left: parent.left
                                anchors.bottom: parent.bottom
                                anchors.leftMargin: 3
                                anchors.bottomMargin: 2
                                text: "β"
                                color: appUi.rightPage === activityButton.index ? appUi.accent : appUi.muted
                                font.pixelSize: 11
                                font.weight: Font.DemiBold
                            }
                        }
                        background: Rectangle {
                            color: activityButton.hovered || appUi.rightPage === activityButton.index ? appUi.surface2 : appUi.surface
                            Rectangle { width: 2; height: parent.height; anchors.right: parent.right; color: appUi.rightPage === activityButton.index ? appUi.accent : "transparent" }
                            Rectangle { width: 1; height: parent.height; anchors.left: parent.left; color: appUi.borderColor }
                        }
                        SiteToolTip { text: activityButton.index === 0 ? "Lyrics" : activityButton.index === 1 ? "Git" : "Censor Detector" }
                        Accessible.name: index === 0 ? "Lyrics" : index === 1 ? "Git" : "Censor Detector"
                        onClicked: appUi.rightPage = index
                        onDoubleClicked: {
                            if (index === 1) {
                                appUi.rightPage = 1
                                appUi.gitExpanded = !appUi.gitExpanded
                            }
                        }
                    }
                }
            }
            Loader {
                id: workspaceLoader
                objectName: "gitWorkspaceLoader"
                property bool visited: false
                onVisibleChanged: if (visible) visited = true
                active: visited || visible
                x: 0; y: 0
                width: parent.width - activityBar.width
                height: parent.height - y
                visible: appUi.showShell && appUi.rightPage === 1
                sourceComponent: Component {
                    GitWorkspace {
                        objectName: "gitWorkspaceView"
                        pageActive: workspaceLoader.visible
                        expanded: appUi.gitExpanded
                        bg: appUi.bg
                        workspaceBackend: appUi.workspace
                        onNotify: function(message) { appUi.notify(message); }
                    }
                }
            }
            Loader {
                objectName: "censorDetectorLoader"
                property bool visited: false
                onVisibleChanged: if (visible) visited = true
                anchors.left: parent.left
                anchors.right: activityBar.left
                anchors.top: parent.top
                anchors.bottom: parent.bottom
                active: visited || visible
                visible: appUi.showShell && appUi.rightPage === 2
                sourceComponent: Component {
                    CensorDetector {
                        objectName: "censorDetectorView"
                        pageActive: parent && parent.visible
                        bridge: appUi.backendBridge
                        uploadBackend: !!appUi.uploader ? appUi.uploader : null
                        approveSoundEnabled: appUi.config.approveSoundEnabled
                        volume: appUi.config.volume
                    }
                }
            }
            RowLayout {
                id: progress
                visible: appUi.rightPage === 0
                x: 20
                y: 10
                width: parent.width - 40 - activityBar.width
                height: 26
                spacing: 10
                Text {
                    textFormat: Text.PlainText
                    text: appUi.state.processing ? "Scanning " + (appUi.state.processed || 0) + " / " + appUi.total : appUi.total ? "Done · " + appUi.total + " tracks" : "No tracks"
                    color: appUi.muted
                    font.pixelSize: 11
                }
                Rectangle {
                    Layout.fillWidth: true
                    implicitHeight: 2
                    color: appUi.surface2
                    radius: 2
                    Rectangle {
                        height: 2
                        width: parent.width * (appUi.state.processing ? Math.min(1, (appUi.state.processed || 0) / Math.max(1, appUi.total)) : 1)
                        color: appUi.accent
                        radius: 2

                    }
                }
                Text {
                    textFormat: Text.PlainText
                    visible: (appUi.state.foundCount || 0) > 0
                    text: (appUi.state.foundCount || 0) + " with hits"
                    color: appUi.accent
                    font.pixelSize: 11
                }
            }
            Rectangle {
                x: 10
                visible: appUi.rightPage === 0
                y: progress.y + progress.height
                width: parent.width - 20 - activityBar.width
                height: 1
                color: appUi.borderColor
            }
            ListView {
                id: results
                visible: appUi.rightPage === 0
                x: 10
                y: progress.y + progress.height + 10
                width: parent.width - 20 - activityBar.width
                height: Math.max(0, parent.height - y - 10)
                model: appUi.showShell ? appUi.backendBridge.cards : null
                cacheBuffer: 0
                topMargin: 6
                bottomMargin: 6
                spacing: 10
                clip: true
                reuseItems: true
                delegate: ResultCard {
                    required property var track
                    width: results.width
                    ui: appUi
                    row: track
                }
                ScrollBar.vertical: ScrollBar {}
                Text {
                    textFormat: Text.PlainText
                    visible: appUi.backendBridge.cards.count === 0 && !appUi.state.processing && !appUi.tracksPending
                    width: parent.width - 48
                    x: 24
                    y: 60
                    horizontalAlignment: Text.AlignHCenter
                    color: appUi.muted
                    font.pixelSize: 13
                    text: appUi.total ? "No drug references found" : "No tracks. Press Fetch to load"
                }
            }
            ScrollEdgeShadow {
                visible: appUi.rightPage === 0
                anchors.fill: results
                flickable: results
            }
        }
        SiteSpinner {
            x: rightPanel.x + results.x + (results.width - width) / 2
            y: rightPanel.y + results.y + (results.height - height) / 2
            visible: appUi.tracksPending && appUi.rightPage === 0
            accentColor: appUi.accent
            label: "Loading results"
        }
    }
    component PlayerSlider: Slider {
        id: playerSlider
        implicitHeight: 20
        SiteFocusRing { active: playerSlider.visualFocus }
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
                color: playerSlider.hovered || playerSlider.pressed ? appUi.accent : "#ffffff"
            }
        }
        handle: Rectangle {
            x: playerSlider.leftPadding + playerSlider.visualPosition * (playerSlider.availableWidth - width)
            y: playerSlider.height / 2 - height / 2
            width: 12; height: 12; radius: 6
            color: "#ffffff"
            visible: playerSlider.hovered || playerSlider.pressed || playerSlider.visualFocus
        }
    }
    component PlayerButton: SiteButton {
        id: playerButton
        property bool primary: false
        glyph: "play"
        implicitWidth: 36
        implicitHeight: 36
        HoverHandler { cursorShape: parent.enabled ? Qt.PointingHandCursor : Qt.ArrowCursor }
        opacity: enabled ? 1 : 0.35
        padding: primary ? 6 : 7
        leftPadding: padding
        rightPadding: padding
        topPadding: padding
        bottomPadding: padding
        contentItem: Image {
            source: "qrc:/assets/lucide-" + playerButton.glyph + ".svg"
            sourceSize.width: playerButton.primary ? 24 : 20
            sourceSize.height: playerButton.primary ? 24 : 20
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
        visible: true
        height: 88
        color: appUi.playerSurface
        enabled: appUi.privileged && appUi.player.enabled
        TapHandler {
            acceptedButtons: Qt.RightButton
            enabled: appUi.player.trackId !== ""
            onTapped: point => {
                const track = Object.assign({}, appUi.backendBridge.trackById(appUi.player.trackId), {
                    id: appUi.player.trackId,
                    title: appUi.player.title,
                    artist: appUi.player.artist
                });
                appUi.context(track, playerPanel, point.position.x, point.position.y, false);
            }
        }
        SpectrumVisualizer {
            objectName: "spectrumVisualizer"
            anchors.fill: parent
            visible: appUi.config.visualizerEnabled
            accent: appUi.accent
            levels: appUi.player.spectrumLevels
            peaks: appUi.player.spectrumPeaks
        }
        Rectangle { width: parent.width; height: 1; color: "#29292d" }
        function timeLabel(ms) {
            const seconds = Math.floor(ms / 1000);
            return Math.floor(seconds / 60) + ":" + (seconds % 60 < 10 ? "0" : "") + seconds % 60;
        }
        Item {
            anchors.fill: parent
            anchors.margins: 16
            opacity: appUi.player.enabled ? 1 : 0.4
            Rectangle {
                id: playerCover
                anchors.left: parent.left
                anchors.verticalCenter: parent.verticalCenter
                width: 52; height: 52
                radius: 8; color: "#171719"
                layer.enabled: GraphicsInfo.api !== GraphicsInfo.Software
                layer.effect: MultiEffect {
                    maskEnabled: true
                    maskSource: playerCoverMask
                }
                Rectangle {
                    id: playerCoverMask
                    anchors.fill: parent
                    radius: playerCover.radius
                    color: "white"
                    visible: false
                    layer.enabled: true
                }
                Image { id: playerCoverImage; anchors.fill: parent; source: appUi.player.cover ? "image://trackcovers/" + encodeURIComponent(appUi.player.cover) : ""; sourceSize: Qt.size(104, 104); asynchronous: true; fillMode: Image.PreserveAspectCrop }
                Image { anchors.centerIn: parent; width: 20; height: 20; visible: playerCoverImage.status !== Image.Ready; source: "qrc:/assets/track-placeholder.svg" }
            }
            ColumnLayout {
                anchors.left: playerCover.right
                anchors.leftMargin: 16
                anchors.verticalCenter: parent.verticalCenter
                width: Math.max(0, parent.width * 0.25 - 68)
                Text { textFormat: Text.PlainText; Layout.fillWidth: true; text: appUi.player.title || "DDetector"; color: "#f0f0f4"; elide: Text.ElideRight }
                Text {
                    textFormat: Text.PlainText
                    Layout.fillWidth: true
                    text: appUi.player.artist || (appUi.player.enabled ? "Select a track" : "Add a token in Settings")
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
                    PlayerButton { glyph: "skip-back"; padding: 8; Accessible.name: "Previous track"; onClicked: appUi.player.previous() }
                    PlayerButton {
                        primary: true
                        padding: 7
                        implicitWidth: 32
                        implicitHeight: 32
                        glyph: appUi.player.playing ? "pause" : "play"
                        Accessible.name: appUi.player.playing ? "Pause" : "Play"
                        enabled: appUi.player.enabled && !appUi.player.busy && appUi.player.title !== ""
                        onClicked: appUi.player.toggle()
                    }
                    PlayerButton { glyph: "skip-forward"; padding: 8; Accessible.name: "Next track"; onClicked: appUi.player.next() }
                }
                RowLayout {
                    Text { textFormat: Text.PlainText; text: playerPanel.timeLabel(appUi.player.position); color: "#888888"; font.pixelSize: 11 }
                    PlayerSlider {
                        Layout.fillWidth: true
                        from: 0; to: Math.max(1, appUi.player.duration)
                        value: appUi.player.position
                        enabled: appUi.player.enabled && appUi.player.duration > 0
                        Accessible.name: "Playback position"
                        onMoved: appUi.player.seek(value)

                    }
                    Text { textFormat: Text.PlainText; text: playerPanel.timeLabel(appUi.player.duration); color: "#888888"; font.pixelSize: 11 }
                }
            }
            PlayerButton {
                anchors.right: volumeButton.left
                anchors.rightMargin: 12
                anchors.verticalCenter: parent.verticalCenter
                glyph: "music-standard"
                Accessible.name: "Send"
                SiteToolTip { text: "Send track" }
                enabled: appUi.player.trackId !== "" && (!!appUi.uploader) && !appUi.uploader.busy
                onClicked: sendDialog.openTrack(appUi.player.trackId, appUi.player.title, appUi.player.artist, appUi.player.cover ? "image://trackcovers/" + encodeURIComponent(appUi.player.cover) : "", appUi.player.metadata)
            }
            PlayerButton {
                id: volumeButton
                anchors.right: volumeSlider.left
                anchors.rightMargin: 8
                anchors.verticalCenter: parent.verticalCenter
                glyph: appUi.player.volume > 0 ? "volume-2" : "volume-off"
                                Accessible.name: appUi.player.volume > 0 ? "Mute" : "Unmute"
                onClicked: appUi.player.volume = appUi.player.volume > 0 ? 0 : 0.7
            }
            PlayerSlider {
                id: volumeSlider
                anchors.right: parent.right
                anchors.verticalCenter: parent.verticalCenter
                width: 100
                from: 0; to: 1; value: appUi.player.volume
                Accessible.name: "Volume"
                onMoved: appUi.player.volume = value

            }
        }
    }
    SendDialog {
        id: sendDialog
        uploadBackend: !!appUi.uploader ? appUi.uploader : null
        approveSoundEnabled: appUi.config.approveSoundEnabled
        volume: appUi.config.volume
        onSent: function(action) { appUi.notify("Send: " + action); }
    }
    SiteView {
        ui: appUi
    }

    SiteDialog {
        id: auth
        objectName: "authPopup"
        Component.onCompleted: { if (appUi.state.authenticated === false) open(); }
        header: Item { implicitHeight: 0 }
        footer: Item { implicitHeight: 0 }
        width: Math.min(appUi.width - 32, 704)
        height: Math.max(368, authPanel.height + 100 + 2)
        modal: true
        focus: true
        padding: 0
        topPadding: 0
        closePolicy: appUi.state.authenticated === false ? Popup.NoAutoClose : Popup.CloseOnEscape | Popup.CloseOnPressOutside
        background: Rectangle {
            radius: 20
            color: appUi.surface
            border.color: appUi.borderColor
        }
        Overlay.modal: Rectangle {
            color: "#99000000"
            MouseArea {
                objectName: "authOverlayDragArea"
                anchors.fill: parent
                onPressed: appUi.startSystemMove()
            }
        }
        contentItem: Item {
            id: authContent
            layer.enabled: GraphicsInfo.api !== GraphicsInfo.Software
            layer.effect: MultiEffect {
                maskEnabled: true
                maskSource: authMask
            }
            Rectangle {
                id: authMask
                anchors.fill: parent
                radius: 20
                layer.enabled: true
                visible: false
            }
            Item {
                id: authArtwork
                objectName: "authArtwork"
                x: 1
                y: 1
                width: (parent.width - 2) * 0.9 / 1.9
                height: parent.height - 2
                clip: true
                Image {
                    width: Math.max(parent.width, parent.height * implicitWidth / Math.max(1, implicitHeight))
                    height: parent.height
                    x: (parent.width - width) * 0.2
                    source: "qrc:/assets/Kagami.webp"
                    fillMode: Image.PreserveAspectCrop
                }
            }
            Column {
                id: authPanel
                readonly property real horizontalPadding: 32
                x: (authArtwork.visible ? authArtwork.x + authArtwork.width : 1) + horizontalPadding
                width: parent.width - x - horizontalPadding - 1
                anchors.verticalCenter: parent.verticalCenter
                spacing: 10
                Text {
                    textFormat: Text.PlainText
                    objectName: "authTitle"
                    text: "Sign in"
                    color: appUi.ink
                    font.family: "Luckystar"
                    font.weight: Font.Normal
                    font.pixelSize: 40
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
                        objectName: "login-" + modelData.provider
                        width: parent.width
                        height: 46
                        text: appUi.backendBridge.pendingProvider === modelData.provider ? "Redirecting…" : "Continue with " + modelData.name
                        enabled: !appUi.state.loginPending && appUi.backendBridge.pendingProvider === ""
                        font.family: "DDetector Quicksand"
                        font.pixelSize: 14
                        font.weight: Font.DemiBold
                        onClicked: appUi.backendBridge.login(modelData.provider)
                        contentItem: Item {
                            TextMetrics {
                                id: providerLabelMetrics
                                font: providerButton.font
                                text: "Continue with Discord"
                            }
                            Item {
                                width: Math.min(parent.width, providerLabelMetrics.width + 27)
                                height: parent.height
                                anchors.centerIn: parent
                                Item {
                                    id: providerIcon
                                    width: 17
                                    height: 17
                                    anchors.left: parent.left
                                    anchors.verticalCenter: parent.verticalCenter
                                    Image {
                                        // Keep the 24-unit logo at 17px while giving its SVG edges transparent room.
                                        width: parent.width * 27 / 24
                                        height: parent.height * 27 / 24
                                        anchors.centerIn: parent
                                        anchors.alignWhenCentered: false
                                        sourceSize.width: Math.ceil(width * 2)
                                        sourceSize.height: Math.ceil(height * 2)
                                        fillMode: Image.PreserveAspectFit
                                        smooth: true
                                        source: "qrc:/assets/" + providerButton.modelData.provider + "-dark" + ".svg"
                                    }
                                }
                                Text {
                                    textFormat: Text.PlainText
                                    id: providerText
                                    text: providerButton.text
                                    font: providerButton.font
                                    color: appUi.ink
                                    anchors.left: providerIcon.right
                                    anchors.leftMargin: 10
                                    anchors.right: parent.right
                                    anchors.verticalCenter: parent.verticalCenter
                                    horizontalAlignment: Text.AlignHCenter
                                }
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
                    textFormat: Text.PlainText
                    width: parent.width
                    wrapMode: Text.Wrap
                    text: appUi.state.error || ""
                    visible: text !== "" && !text.startsWith("Lyrics acquisition failed")
                    color: appUi.ignoredColor
                    font.pixelSize: 13
                }
            }

        }
    }
    NotificationStack {
        ui: appUi
        notificationModel: toastModel
        z: 500
        x: 16
        anchors.bottom: playerPanel.top
        anchors.bottomMargin: 16
        onExpired: function(toastId) { appUi.removeToast(toastId); }
    }
}

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
    property int rightPage: 0
    property bool workspaceCloseApproved: false
    onClosing: function(event) {
        if (typeof localUploader !== "undefined" && localUploader.busy) {
            event.accepted = false;
            notify("Wait for Send to finish before closing.");
            return;
        }
        if (workspaceCloseApproved) return;
        event.accepted = false;
        workspaceView.requestClose(function() {
            workspaceCloseApproved = true;
            Qt.callLater(function() { appUi.close(); });
        });
    }
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
        readonly property bool checkingAccess: (state.authenticated === undefined || (!!state.authenticated && !state.accessChecked && !state.isBanned)) && !state.error && backend.connectionError === ""
        readonly property bool showShell: privileged || checkingAccess
        readonly property bool tracksPending: checkingAccess || (!!state.loading && !state.processing && total === 0)
    property string selectedId: ""
    property bool sheetOpen: false
    property var expanded: ({})
    property var menuTrack: ({})
    property bool menuFullLyrics: false
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
        if (!message || message === "Yandex Music token required") return;
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
        target: backend
        function onToast(message) {
            appUi.notify(message);
        }
        function onConnectionErrorChanged() {
            if (backend.connectionError) appUi.notify(backend.connectionError);
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
        target: appConfig
        function onErrorChanged() { if (appConfig.error) appUi.notify(appConfig.error); }
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
    ListModel { id: toastModel }

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
        SiteMenuSeparator { borderColor: appUi.borderColor }
        SiteMenuItem {
            textColor: appUi.ink
            mutedColor: appUi.muted
            accentColor: appUi.accent
            accentBackground: appUi.accentBg
            dangerColor: appUi.ignoredColor
            objectName: "playTrackEntry"
            text: "Play"
            glyph: "play"
            green: true
            enabled: musicPlayer.enabled && !musicPlayer.busy
            onTriggered: musicPlayer.playTrack(appUi.menuTrack.id)
        }
        SiteMenuSeparator { borderColor: appUi.borderColor }
        SiteMenuItem {
            textColor: appUi.ink
            mutedColor: appUi.muted
            accentColor: appUi.accent
            accentBackground: appUi.accentBg
            dangerColor: appUi.ignoredColor
            text: "Search on Yandex"
            glyph: "search"
            onTriggered: backend.searchYandex(appUi.menuTrack.title, appUi.menuTrack.artist)
        }
        SiteMenuItem {
            textColor: appUi.ink
            mutedColor: appUi.muted
            accentColor: appUi.accent
            accentBackground: appUi.accentBg
            dangerColor: appUi.ignoredColor
            text: "Copy track ID"
            glyph: "copy"
            onTriggered: backend.copyId(appUi.menuTrack.id)
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
            onTriggered: backend.ignore(appUi.menuTrack.id, !appUi.menuTrack.ignored)
        }
    }
    SiteMenu {
        id: headerMenu
        objectName: "accountMenu"
        width: 210
        surfaceColor: appUi.surface
        cornerRadius: 12
        borderColor: appUi.borderColor
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
        height: appUi.showShell || appUi.state.authenticated ? 0 : 48
        visible: height > 0
        color: appUi.bg
        z: 30
        MouseArea {
            anchors.fill: parent
            anchors.rightMargin: windowControls.width
            onPressed: appUi.startSystemMove()
            onDoubleClicked: appUi.toggleMaximized()
        }
        Text {
            x: 8
            anchors.verticalCenter: parent.verticalCenter
            text: "D<span style='color:" + appUi.accent + "'>Detector</span>"
            textFormat: Text.RichText
            font.family: "DDetector Quicksand"
            font.pixelSize: 18
            font.weight: Font.Bold
            color: appUi.ink
        }
    }
    component WindowButton: Button {
                    HoverHandler { cursorShape: parent.enabled ? Qt.PointingHandCursor : Qt.ArrowCursor }
                    id: windowButton
                    required property int index
                    property string modelData: ["Minimize", "Maximize", "Close"][index]
                    objectName: "window" + modelData + "Button"
                    width: activityBar.width
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
        visible: appUi.showShell || !!appUi.state.authenticated
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
            x: 16
            y: 8
            height: 32
            spacing: 10
            Text {
                anchors.verticalCenter: parent.verticalCenter
                text: "D<span style='color:" + appUi.accent + "'>Detector</span>"
                textFormat: Text.RichText
                color: appUi.ink
                font.family: "DDetector Quicksand"
                font.pixelSize: 18
                font.weight: Font.Bold

            }
            Text {
                visible: !appUi.compactHeader && appUi.showShell
                text: backend.tracks.count + " tracks"
                color: appUi.muted
                font.pixelSize: 12
                anchors.verticalCenter: parent.verticalCenter
            }
        }
        RowLayout {
            anchors.right: parent.right
            anchors.rightMargin: appConfig.hideWindowControls ? 8 : appUi.compactHeader ? 10 : windowControls.width + 8
            visible: appUi.showShell
            enabled: appUi.privileged
            y: appUi.compactHeader ? 48 : 9
            width: appUi.compactHeader ? parent.width - (appConfig.hideWindowControls ? 10 : 20) : parent.width - (appConfig.hideWindowControls ? 0 : windowControls.width) - 238
            height: 30
            spacing: 8
            Item {
                Layout.fillWidth: true
                visible: !appUi.compactHeader
            }
            ComboBox {
                id: sorting
                objectName: "sortSelect"
                HoverHandler { cursorShape: parent.enabled ? Qt.PointingHandCursor : Qt.ArrowCursor }
                Layout.preferredWidth: 100
                Layout.preferredHeight: 30
                model: ["Default", "By date", "A - Z", "By artist"]
                onActivated: backend.setSort(currentIndex)
                font.pixelSize: 12
                contentItem: Text {
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
                SiteToolTip { text: "Rebuild track list from external source" }
            }
        }
    }
    Row {
        id: windowControls
        anchors.right: parent.right
        anchors.rightMargin: appConfig.hideWindowControls ? 8 : 0
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
            onClicked: {
                headerMenu.x = appUi.width - headerMenu.width - 10;
                const point = avatarButton.mapToItem(appUi.contentItem, 0, 0);
                headerMenu.y = Math.max(8, point.y - headerMenu.implicitHeight - 8);
                headerMenu.open();
            }
            contentItem: Item {
                clip: true
                Text {
                    anchors.centerIn: parent
                    text: appUi.userName.charAt(0).toUpperCase()
                    color: appUi.ink
                    visible: !avatarImage.ready
                }
                AvatarImage {
                    id: avatarImage
                    objectName: "accountAvatarImage"
                    anchors.fill: parent
                    source: appUi.avatarUrl
                }

            }
        }
        Item { width: 0; height: 48 }
        WindowButton { index: 0; visible: !appConfig.hideWindowControls; anchors.verticalCenter: parent.verticalCenter }
        WindowButton { index: 1; visible: !appConfig.hideWindowControls; anchors.verticalCenter: parent.verticalCenter }
        WindowButton { index: 2; visible: !appConfig.hideWindowControls; anchors.verticalCenter: parent.verticalCenter }
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
            Rectangle {
                Layout.fillWidth: true
                implicitHeight: repositoryCardContent.implicitHeight + 32
                radius: 14
                color: appUi.surface2
                border.color: appUi.borderColor
                ColumnLayout {
                    id: repositoryCardContent
                    anchors.fill: parent
                    anchors.margins: 16
                    spacing: 12
                    Label { text: "Git repository"; color: appUi.ink; font.pixelSize: 13 }
                    TextField {
                        id: repositoryPathInput
                        Layout.fillWidth: true
                        implicitHeight: 36
                        leftPadding: 12
                        rightPadding: 12
                        text: appConfig.repositoryPath || ""
                        placeholderText: Qt.platform.os === "windows" ? "D:/FckCensorData" : "/run/media/admin/DATA/FckCensorData/"
                        color: appUi.ink
                        placeholderTextColor: appUi.muted
                        selectionColor: appUi.accentBg
                        selectedTextColor: appUi.ink
                        selectByMouse: true
                        enabled: typeof localUploader === "undefined" || !localUploader.busy
                        function applyPath() {
                            const path = text.trim();
                            if (path === appConfig.repositoryPath || changeRepositoryDialog.visible) return;
                            if (typeof gitWorkspace !== "undefined" && gitWorkspace.dirty) {
                                appUi.notify("Save or discard file changes before changing the repository.");
                                return;
                            }
                            if (typeof gitWorkspace !== "undefined" && gitWorkspace.terminalRunning) {
                                changeRepositoryDialog.pendingPath = path;
                                changeRepositoryDialog.open();
                            } else appConfig.repositoryPath = path;
                        }
                        onEditingFinished: applyPath()
                        onAccepted: applyPath()
                        background: Rectangle {
                            color: appUi.surface
                            radius: 8
                            border.color: repositoryPathInput.activeFocus ? appUi.accentBorder : appUi.borderColor
                        }
                    }
                }
            }
            Rectangle {
                objectName: "musicTokenCard"
                Layout.fillWidth: true
                implicitHeight: tokenCardContent.implicitHeight + 32
                radius: 14
                color: appUi.surface2
                border.color: appUi.borderColor
                ColumnLayout {
                    id: tokenCardContent
                    anchors.fill: parent
                    anchors.margins: 16
                    spacing: 12
                    Label {
                        text: "Yandex Music"
                        color: appUi.ink
                        font.pixelSize: 13
                    }
                    TextField {
                        id: musicToken
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
                        background: Rectangle {
                            color: appUi.surface
                            radius: 8
                            border.color: musicToken.activeFocus ? appUi.accentBorder : appUi.borderColor
                        }
                        text: appConfig.musicToken
                        onTextEdited: tokenApplyTimer.restart()
                        onAccepted: { tokenApplyTimer.stop(); appConfig.musicToken = text.trim(); }
                        onEditingFinished: { tokenApplyTimer.stop(); appConfig.musicToken = text.trim(); }
                        Timer {
                            id: tokenApplyTimer
                            interval: 800
                            onTriggered: appConfig.musicToken = musicToken.text.trim()
                        }
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
                CheckBox {
                    id: hideIgnoredToggle
                    objectName: "hideIgnoredSetting"
                    anchors.fill: parent
                    padding: 16
                    text: "Hide ignored tracks"
                    checked: backend.hideIgnored
                    onToggled: backend.hideIgnored = checked
                    HoverHandler { cursorShape: parent.enabled ? Qt.PointingHandCursor : Qt.ArrowCursor }
                    indicator: Rectangle {
                        implicitWidth: 40
                        implicitHeight: 22
                        x: hideIgnoredToggle.width - width - hideIgnoredToggle.rightPadding
                        y: (hideIgnoredToggle.height - height) / 2
                        radius: 11
                        color: hideIgnoredToggle.checked ? appUi.accentBg : appUi.surface
                        border.color: hideIgnoredToggle.checked ? appUi.accentBorder : appUi.borderColor
                        Rectangle {
                            width: 16; height: 16; radius: 8
                            x: hideIgnoredToggle.checked ? 21 : 3
                            y: 3
                            color: hideIgnoredToggle.checked ? appUi.accent : appUi.muted
                            Behavior on x { NumberAnimation { duration: 160 } }
                        }
                    }
                    contentItem: RowLayout {
                        spacing: 12
                        Image {
                            Layout.preferredWidth: 18
                            Layout.preferredHeight: 18
                            source: "qrc:/assets/lucide-shield.svg"
                            layer.enabled: true
                            layer.effect: MultiEffect { brightness: 1; colorization: 1; colorizationColor: appUi.muted }
                        }
                        Text {
                            Layout.fillWidth: true
                            Layout.rightMargin: 52
                            text: hideIgnoredToggle.text
                            font: hideIgnoredToggle.font
                            color: appUi.ink
                            elide: Text.ElideRight
                            verticalAlignment: Text.AlignVCenter
                        }
                    }
                    background: Item {}
                }
            }
            Rectangle {
                Layout.fillWidth: true
                implicitHeight: 56
                radius: 14
                color: appUi.surface2
                border.color: appUi.borderColor
                CheckBox {
                    id: hideWindowControlsToggle
                    objectName: "hideWindowControlsSetting"
                    anchors.fill: parent
                    padding: 16
                    text: "Hide window controls"
                    checked: appConfig.hideWindowControls
                    onToggled: appConfig.hideWindowControls = checked
                    HoverHandler { cursorShape: Qt.PointingHandCursor }
                    indicator: Rectangle {
                        implicitWidth: 40; implicitHeight: 22
                        x: hideWindowControlsToggle.width - width - hideWindowControlsToggle.rightPadding
                        y: (hideWindowControlsToggle.height - height) / 2
                        radius: 11
                        color: hideWindowControlsToggle.checked ? appUi.accentBg : appUi.surface
                        border.color: hideWindowControlsToggle.checked ? appUi.accentBorder : appUi.borderColor
                        Rectangle {
                            width: 16; height: 16; radius: 8
                            x: hideWindowControlsToggle.checked ? 21 : 3
                            y: 3
                            color: hideWindowControlsToggle.checked ? appUi.accent : appUi.muted
                            Behavior on x { NumberAnimation { duration: 160 } }
                        }
                    }
                    contentItem: RowLayout {
                        spacing: 12
                        Image {
                            Layout.preferredWidth: 18; Layout.preferredHeight: 18
                            source: "qrc:/assets/lucide-maximize.svg"
                            layer.enabled: true
                            layer.effect: MultiEffect { brightness: 1; colorization: 1; colorizationColor: appUi.muted }
                        }
                        Text {
                            Layout.fillWidth: true
                            Layout.rightMargin: 52
                            text: hideWindowControlsToggle.text
                            font: hideWindowControlsToggle.font
                            color: appUi.ink
                            elide: Text.ElideRight
                            verticalAlignment: Text.AlignVCenter
                        }
                    }
                    background: Item {}
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
            Label {
                Layout.fillWidth: true
                text: "The current terminal will be stopped, including any running commands. Continue?"
                color: appUi.ink
                wrapMode: Text.Wrap
            }
            RowLayout {
                Layout.fillWidth: true
                Item { Layout.fillWidth: true }
                SiteButton { text: "No"; onClicked: changeRepositoryDialog.reject() }
                SiteButton { text: "Yes"; accented: true; onClicked: changeRepositoryDialog.accept() }
            }
        }
        onAccepted: {
            if (gitWorkspace.dirty || (typeof localUploader !== "undefined" && localUploader.busy)) {
                appUi.notify("Save file changes and wait for Send before changing the repository.");
                return;
            }
            gitWorkspace.stopTerminal();
            appConfig.repositoryPath = pendingPath;
        }
        onRejected: repositoryPathInput.text = appConfig.repositoryPath
    }
    component ScrollEdgeShadow: Item {
        z: 1
        Rectangle {
            anchors.top: parent.top
            width: parent.width
            height: 8
            gradient: Gradient {
                GradientStop { position: 0; color: "#ff000000" }
                GradientStop { position: 1; color: "#00000000" }
            }
        }
        Rectangle {
            anchors.bottom: parent.bottom
            width: parent.width
            height: 8
            gradient: Gradient {
                GradientStop { position: 0; color: "#00000000" }
                GradientStop { position: 1; color: "#ff000000" }
            }
        }
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
            x: 10
            y: 10
            width: appUi.mobile ? panels.width - 20 : panels.leftPanelWidth - 20
            height: panels.height - (appUi.mobile ? 82 : 20)
            model: backend.tracks
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
        }
        Rectangle {
            visible: !appUi.mobile
            x: panels.leftPanelWidth
            height: panels.height
            width: 1
            color: appUi.borderColor
        }
        Rectangle {
            id: right
            x: appUi.mobile ? 0 : panels.leftPanelWidth + 1
            y: appUi.mobile && !appUi.sheetOpen ? panels.height - 60 : 0
            width: appUi.mobile ? panels.width : panels.width - panels.leftPanelWidth - 1
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
                contentItem: Item {
                    Image {
                        anchors.centerIn: parent
                        width: 20; height: 20
                        source: "qrc:/assets/lucide-" + (appUi.sheetOpen ? "chevron-down" : "chevron-up") + ".svg"
                        layer.enabled: true
                        layer.effect: MultiEffect { brightness: 1; colorization: 1; colorizationColor: appUi.muted }
                    }
                }
                background: Item {}
                onClicked: appUi.sheetOpen = !appUi.sheetOpen
                Accessible.name: appUi.sheetOpen ? "Collapse results" : "Expand results"
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
                    model: ["music", "git-branch"]
                    Button {
                        id: activityButton
                        objectName: index === 0 ? "lyricsPageButton" : "gitPageButton"
                        required property string modelData
                        required property int index
                        width: 48; height: 48
                        padding: 0
                        contentItem: Item {
                            Image {
                                anchors.centerIn: parent
                                width: 24; height: 24
                                source: "qrc:/assets/lucide-" + activityButton.modelData + ".svg"
                                layer.enabled: true
                                layer.effect: MultiEffect { brightness: 1; colorization: 1; colorizationColor: appUi.rightPage === activityButton.index ? appUi.accent : activityButton.hovered ? appUi.ink : appUi.muted }
                            }
                        }
                        background: Rectangle {
                            color: activityButton.hovered || appUi.rightPage === activityButton.index ? appUi.surface2 : appUi.surface
                            Rectangle { width: 2; height: parent.height; anchors.right: parent.right; color: appUi.rightPage === activityButton.index ? appUi.accent : "transparent" }
                            Rectangle { width: 1; height: parent.height; anchors.left: parent.left; color: appUi.borderColor }
                        }
                        SiteToolTip { text: activityButton.index === 0 ? "Lyrics" : "Git" }
                        Accessible.name: index === 0 ? "Lyrics" : "Git"
                        onClicked: appUi.rightPage = index
                    }
                }
            }
            GitWorkspace {
                id: workspaceView
                objectName: "gitWorkspaceView"
                x: 0; y: appUi.mobile ? 36 : 0
                width: parent.width - activityBar.width
                height: parent.height - y
                visible: appUi.rightPage === 1
                workspaceBackend: typeof gitWorkspace !== "undefined" ? gitWorkspace : null
                onNotify: function(message) { appUi.notify(message); }
            }
            RowLayout {
                id: progress
                visible: appUi.rightPage === 0
                x: 20
                y: appUi.mobile ? 36 : 10
                width: parent.width - 40 - activityBar.width
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
                model: backend.cards
                topMargin: 6
                bottomMargin: 6
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
                    visible: backend.cards.count === 0 && !appUi.state.processing && !appUi.tracksPending
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
            ScrollEdgeShadow {
                visible: appUi.rightPage === 0
                anchors.fill: results
            }
        }
        SiteSpinner {
            x: right.x + results.x + (results.width - width) / 2
            y: right.y + results.y + (results.height - height) / 2
            visible: appUi.tracksPending && appUi.rightPage === 0
            accentColor: appUi.accent
            label: "Loading results"
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
                color: playerSlider.hovered || playerSlider.pressed ? appUi.accent : "#ffffff"
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
        padding: primary ? 6 : 7
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
        visible: appUi.showShell && (appUi.checkingAccess || musicPlayer.enabled)
        height: visible ? 88 : 0
        color: appUi.playerSurface
        enabled: visible && appUi.privileged
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
                Image { id: playerCoverImage; anchors.fill: parent; source: musicPlayer.cover ? "image://trackcovers/" + encodeURIComponent(musicPlayer.cover) : ""; sourceSize: Qt.size(104, 104); asynchronous: true; fillMode: Image.PreserveAspectCrop }
                Image { anchors.centerIn: parent; width: 20; height: 20; visible: playerCoverImage.status !== Image.Ready; source: "qrc:/assets/track-placeholder.svg" }
            }
            ColumnLayout {
                anchors.left: playerCover.right
                anchors.leftMargin: 16
                anchors.verticalCenter: parent.verticalCenter
                width: Math.max(0, parent.width * 0.25 - 68)
                Text { Layout.fillWidth: true; text: musicPlayer.title || "DDetector"; color: "#f0f0f4"; elide: Text.ElideRight }
                Text {
                    Layout.fillWidth: true
                    text: musicPlayer.artist || (musicPlayer.enabled ? "Select a track" : "Add a token in Settings")
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
                anchors.right: volumeButton.left
                anchors.rightMargin: 12
                anchors.verticalCenter: parent.verticalCenter
                glyph: "music-standard"
                Accessible.name: "Send"
                SiteToolTip { text: "Send track" }
                enabled: musicPlayer.trackId !== "" && (typeof localUploader !== "undefined") && !localUploader.busy
                onClicked: sendDialog.openTrack(musicPlayer.trackId, musicPlayer.title, musicPlayer.artist, musicPlayer.cover ? "image://trackcovers/" + encodeURIComponent(musicPlayer.cover) : "")
            }
            PlayerButton {
                id: volumeButton
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
    SendDialog {
        id: sendDialog
        uploadBackend: typeof localUploader !== "undefined" ? localUploader : null
        onSent: function(action) { appUi.notify("Send: " + action); }
    }
    SiteView {
        anchors.top: mainHeader.bottom
        anchors.bottom: playerPanel.top
        width: parent.width
        visible: !appUi.showShell
        ui: appUi
        onSignIn: auth.open()
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
                contentItem: Item {
                    Image {
                        anchors.centerIn: parent
                        width: 12; height: 12
                        source: "qrc:/assets/lucide-x.svg"
                        layer.enabled: true
                        layer.effect: MultiEffect { brightness: 1; colorization: 1; colorizationColor: parent.parent.hovered ? appUi.accent : appUi.muted }
                    }
                }
                onClicked: auth.close()
                Accessible.name: "Close"
            }
        }
    }
    Column {
        id: toastStack
        objectName: "notificationStack"
        z: 500
        x: 16
        anchors.bottom: playerPanel.top
        anchors.bottomMargin: 16
        spacing: 8
        width: Math.min(appUi.width - 32, 380)
        // Update height and row positions together so inserting at the top
        // keeps existing cards stationary relative to the player.
        Repeater {
            model: toastModel
            delegate: Rectangle {
                id: toastCard
                required property int toastId
                required property string message
                property bool open: false
                objectName: "notificationToast"
                // Column owns item positions; animate only the visual offset.
                transform: Translate {
                    x: toastCard.open ? 0 : -toastCard.width - 16
                    Behavior on x {
                        NumberAnimation { duration: 220; easing.type: Easing.OutCubic }
                    }
                }
                width: Math.min(toastStack.width, toastLabel.implicitWidth + 32)
                height: toastLabel.implicitHeight + 24
                radius: 8
                opacity: open ? 1 : 0
                color: appUi.surface2
                border.color: appUi.borderColor
                Accessible.role: Accessible.AlertMessage
                Accessible.name: message
                Behavior on opacity { NumberAnimation { duration: 180 } }
                Component.onCompleted: open = true
                Timer {
                    interval: 3500
                    running: true
                    onTriggered: {
                        toastCard.open = false;
                        exitTimer.start();
                    }
                }
                Timer {
                    id: exitTimer
                    interval: 220
                    onTriggered: appUi.removeToast(toastCard.toastId)
                }
                Text {
                    id: toastLabel
                    anchors.centerIn: parent
                    width: Math.min(toastStack.width - 32, implicitWidth)
                    wrapMode: Text.Wrap
                    text: toastCard.message
                    color: appUi.ink
                    font.pixelSize: 12
                }
            }
        }
    }
}

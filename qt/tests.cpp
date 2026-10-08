#include "bridge.h"
#include "player.h"
#include "avatarimage.h"
#include "workspace.h"
#include "terminal.h"
#include "syntaxhighlighter.h"
#include "uploader.h"
#include <QAbstractItemModelTester>
#include <QClipboard>
#include <QFile>
#include <QSettings>
#include <sys/stat.h>
#include <QDirIterator>
#include <QFontDatabase>
#include <QGuiApplication>
#include <QJsonDocument>
#include <QCryptographicHash>
#include <QPersistentModelIndex>
#include <QQmlApplicationEngine>
#include <QQmlContext>
#include <QQuickStyle>
#include <QQuickWindow>
#include <QQuickItem>

static QQuickItem *visualChild(QQuickItem *root, const QString &name) {
    if (root->objectName() == name) return root;
    for (auto child : root->childItems())
        if (auto found = visualChild(child, name)) return found;
    return nullptr;
}
#include <QSignalSpy>
#include <QTcpServer>
#include <QTcpSocket>
#include <QTemporaryDir>
#include <QtTest>

class MockBackend final : public QTcpServer {
public:
    QJsonObject state;
    QList<QByteArray> requests;
    int notModified = 0;
    int getDelayMs = 0;
    MockBackend() {
        QFile fixture(":/test-fixtures/state.json");
        if (!fixture.open(QIODevice::ReadOnly)) qFatal("Test fixture is missing");
        state = QJsonDocument::fromJson(fixture.readAll()).object();
        listen(QHostAddress::LocalHost, 0);
        connect(this, &QTcpServer::newConnection, this, [this] {
            while (hasPendingConnections()) {
                auto socket = nextPendingConnection();
                auto buffer = std::make_shared<QByteArray>();
                connect(socket, &QTcpSocket::readyRead, socket, [this, socket, buffer] {
                    *buffer += socket->readAll();
                    const int headers = buffer->indexOf("\r\n\r\n");
                    if (headers < 0) return;
                    int length = 0;
                    for (auto line : buffer->left(headers).split('\n'))
                        if (line.toLower().startsWith("content-length:")) length = line.mid(15).trimmed().toInt();
                    if (buffer->size() < headers + 4 + length) return;
                    requests.append(*buffer);
                    const bool get = buffer->startsWith("GET /api/state ");
                    if (buffer->startsWith("POST /api/ignore ")) {
                        const auto body = QJsonDocument::fromJson(buffer->mid(headers + 4, length)).object();
                        auto tracks = state.value("tracks").toArray();
                        for (int i = 0; i < tracks.size(); ++i) {
                            auto row = tracks[i].toObject();
                            if (row.value("id").toInteger() == body.value("track_id").toInteger()) {
                                row.insert("ignored", body.value("ignored"));
                                tracks[i] = row;
                            }
                        }
                        state.insert("tracks", tracks);
                    }
                    auto response = QJsonDocument(get ? state : QJsonObject{{"ok", true}, {"browser_opened", true}}).toJson(QJsonDocument::Compact);
                    const QByteArray tag = "\"" + QCryptographicHash::hash(response, QCryptographicHash::Sha256).toHex() + "\"";
                    const bool unchanged = get && buffer->contains("If-None-Match: " + tag + "\r\n");
                    if (unchanged) { ++notModified; response.clear(); }
                    const QByteArray wire = QByteArray(unchanged ? "HTTP/1.1 304 Not Modified\r\n" : "HTTP/1.1 200 OK\r\n")
                        + (get ? "ETag: " + tag + "\r\n" : QByteArray())
                        + "Content-Type: application/json\r\nConnection: close\r\nContent-Length: " + QByteArray::number(response.size()) + "\r\n\r\n" + response;
                    QTimer::singleShot(get ? getDelayMs : 0, socket, [socket, wire] {
                        socket->write(wire);
                        socket->disconnectFromHost();
                    });
                });
                connect(socket, &QTcpSocket::disconnected, socket, &QObject::deleteLater);
            }
        });
    }
    QUrl url() const { return QUrl("http://127.0.0.1:" + QString::number(serverPort())); }
};

struct LocalContexts {
    GitWorkspace workspace;
    Uploader uploader;

    explicit LocalContexts(Config &config) {
        const auto applyRepository = [this, &config] {
            workspace.setRepositoryPath(config.repositoryPath());
            uploader.setDirectory(workspace.rootPath());
        };
        applyRepository();
        QObject::connect(&config, &Config::settingsChanged, &workspace, applyRepository);
        QObject::connect(&uploader, &Uploader::completed, &workspace,
                         [this](const QString &) { workspace.refreshTree(); });
    }

    void attach(QQmlApplicationEngine &engine) {
        engine.rootContext()->setContextProperty("gitWorkspace", &workspace);
        engine.rootContext()->setContextProperty("localUploader", &uploader);
    }
};

class NativeTests final : public QObject {
    Q_OBJECT
    QTemporaryDir settings;
private slots:
    void initTestCase() {
        QVERIFY(settings.isValid());
        QSettings::setDefaultFormat(QSettings::IniFormat);
        QSettings::setPath(QSettings::IniFormat, QSettings::UserScope, settings.path());
        QQuickStyle::setStyle("Basic");
        qmlRegisterType<SyntaxHighlighter>("DDetector.Syntax", 1, 0, "SyntaxHighlighter");
        qmlRegisterType<EmbeddedTerminal>("DDetector.Terminal", 1, 0, "EmbeddedTerminal");
        qmlRegisterUncreatableType<TerminalSession>("DDetector.Terminal", 1, 0, "TerminalSession", "Owned by GitWorkspace");
        qmlRegisterType<AvatarImage>("DDetector", 1, 0, "AvatarImage");
        QDirIterator fonts(":/assets", {"*.ttf", "*.otf"}, QDir::Files);
        while (fonts.hasNext()) QFontDatabase::addApplicationFont(fonts.next());
        QFont font("DDetector Nunito"); font.setWeight(QFont::Bold); font.setPixelSize(13);
        QGuiApplication::setFont(font);
    }
    void localYandexTokenLifecycle() {
        MockBackend server;
        server.state.insert("authenticated", false);
        server.state.insert("hasAccess", false);
        Config config{QString()};
        config.setMusicToken("test-yandex-token");
        Bridge bridge(server.url(), "test-random-key", nullptr, &config);
        QTRY_VERIFY(!server.requests.isEmpty());
        const auto countPath = [&server](const QByteArray &path) {
            int count = 0;
            for (const auto &request : server.requests) if (request.startsWith(path)) ++count;
            return count;
        };
        QCOMPARE(countPath("POST /api/yandex/token "), 0);
        server.state.insert("authenticated", true);
        server.state.insert("hasAccess", true);
        QTRY_COMPARE(countPath("POST /api/yandex/token "), 1);
        QTRY_COMPARE(countPath("POST /api/load "), 1);
        for (const auto &request : server.requests) {
            if (request.startsWith("POST /api/yandex/token ")) {
                QVERIFY(request.contains("\"access_token\":\"test-yandex-token\""));
                QVERIFY(request.contains("Authorization: Bearer test-random-key"));
            }
        }
        server.state.insert("authenticated", false);
        server.state.insert("hasAccess", false);
        bridge.logout();
        QVERIFY(!bridge.state().value("authenticated").toBool());
        QCOMPARE(bridge.tracks()->count(), 0);
        QTRY_COMPARE(countPath("POST /api/yandex/token "), 2);
        QVERIFY(config.musicToken() == "test-yandex-token");
    }
    void configPersistence() {
        QTemporaryDir directory;
        QVERIFY(directory.isValid());
        const auto path = directory.path() + "/private/Config.json";
        Config config(path);
        QSignalSpy changed(&config, &Config::settingsChanged);
        QVERIFY(config.repositoryPath().isEmpty());
        const auto repositoryPath = directory.path() + "/repository with spaces";
        config.setRepositoryPath("  " + repositoryPath + "  ");
        QCOMPARE(config.repositoryPath(), repositoryPath);
        config.setRepositoryPath(repositoryPath);
        QCOMPARE(changed.count(), 1);
        config.setMusicToken("test-private-token");
        config.setHideIgnored(true);
        QVERIFY(!config.hideWindowControls());
        config.setHideWindowControls(true);
        config.setVolume(0.75);
        QVERIFY(config.error().isEmpty());
        QCOMPARE(changed.count(), 5);
        Config restored(path);
        QCOMPARE(restored.repositoryPath(), repositoryPath);
        QCOMPARE(restored.musicToken(), QString("test-private-token"));
        QVERIFY(restored.hideIgnored());
        QVERIFY(restored.hideWindowControls());
        QCOMPARE(restored.volume(), 0.75);
        struct stat info {};
        QVERIFY(::stat(QFile::encodeName(path).constData(), &info) == 0);
        QCOMPARE(info.st_mode & 0777, mode_t(0600));
        QVERIFY(::stat(QFile::encodeName(directory.path() + "/private").constData(), &info) == 0);
        QCOMPARE(info.st_mode & 0777, mode_t(0700));
        QFile file(path);
        QVERIFY(file.open(QIODevice::ReadOnly));
        QCOMPARE(QJsonDocument::fromJson(file.readAll()).object().value("repositoryPath").toString(), repositoryPath);
        file.close();
        config.setRepositoryPath(QString());
        Config cleared(path);
        QVERIFY(cleared.repositoryPath().isEmpty());
        QCOMPARE(changed.count(), 6);
        QVERIFY(file.open(QIODevice::WriteOnly | QIODevice::Truncate));
        const QByteArray invalid = "{\"musicToken\":\"test-private-token\",\"volume\":\"bad\"}";
        QCOMPARE(file.write(invalid), invalid.size()); file.close();
        Config broken(path);
        QVERIFY(!broken.error().isEmpty());
        QVERIFY(!broken.error().contains("test-private-token"));
        QVERIFY(broken.musicToken().isEmpty());
        broken.setMusicToken("replacement");
        QVERIFY(file.open(QIODevice::ReadOnly)); QCOMPARE(file.readAll(), invalid); file.close();
        Config memory{QString()};
        memory.setMusicToken("memory-only"); QVERIFY(memory.error().isEmpty());
    }
    void modelDiffPreservesIndexes() {
        TrackModel model;
        QAbstractItemModelTester tester(&model, QAbstractItemModelTester::FailureReportingMode::QtTest);
        const QJsonObject a{{"id", 1}, {"title", "A"}}, b{{"id", 2}, {"title", "B"}};
        model.sync({a, b});
        QPersistentModelIndex persistent(model.index(0));
        QSignalSpy changed(&model, &QAbstractItemModel::dataChanged);
        QSignalSpy reset(&model, &QAbstractItemModel::modelReset);
        model.sync({a, b});
        QCOMPARE(changed.count(), 0); QCOMPARE(reset.count(), 0);
        auto updated = a; updated.insert("status", "found");
        model.sync({b, updated});
        QCOMPARE(persistent.row(), 1); QCOMPARE(changed.count(), 1); QCOMPARE(reset.count(), 0);
        model.sync({updated});
        QCOMPARE(persistent.row(), 0); QCOMPARE(model.count(), 1);
    }
    void filterSortAndSettings() {
        Config config{QString()};
        MockBackend server;
        Bridge bridge(server.url(), "test-random-key", nullptr, &config);
        QTRY_COMPARE(bridge.tracks()->count(), 6);
        bridge.setHideIgnored(false);
        QCOMPARE(bridge.tracks()->count(), 6); QCOMPARE(bridge.cards()->count(), 3);
        bridge.setSearch("demo artist");
        QCOMPARE(bridge.tracks()->count(), 3); QCOMPARE(bridge.cards()->count(), 3);
        bridge.setSearch("102"); QCOMPARE(bridge.tracks()->count(), 1);
        bridge.setSearch(""); bridge.setSort(3);
        QCOMPARE(bridge.tracks()->data(bridge.tracks()->index(0), Qt::UserRole).toMap().value("artist").toString(), QString("Demo Artist"));
        bridge.setSearch("reference"); bridge.setSort(3);
        QCOMPARE(bridge.tracks()->count(), 1);
        bridge.setSearch(""); bridge.setHideIgnored(true);
        QCOMPARE(bridge.tracks()->count(), 5); QCOMPARE(bridge.cards()->count(), 2);
        Bridge restored(server.url(), "test-random-key", nullptr, &config);
        QVERIFY(restored.hideIgnored());
        QVERIFY(bridge.metaObject()->indexOfProperty("theme") < 0);
        bridge.setHideIgnored(false);
        bridge.setSort(1);
        QCOMPARE(bridge.tracks()->indexOf("101"), 0);
        QCOMPARE(bridge.tracks()->data(bridge.tracks()->index(0), Qt::UserRole).toMap().value("date").toString(), QString("10/08/2026"));
        bridge.ignore("101", true); QTRY_COMPARE(bridge.cards()->data(bridge.cards()->index(0), Qt::UserRole).toMap().value("ignored").toBool(), true);
    }
    void configOnlyRebuildsForHideIgnored() {
        Config config{QString()};
        MockBackend server;
        Bridge bridge(server.url(), "test-random-key", nullptr, &config);
        QTRY_COMPARE(bridge.tracks()->count(), 6);
        QCOMPARE(bridge.tracks()->count(), 6);
        // Clear the public model as a probe: rebuilding would restore source rows.
        bridge.tracks()->sync({});
        config.setVolume(0.42);
        config.setMusicToken("test-token");
        QCOMPARE(bridge.tracks()->count(), 0);
        config.setHideIgnored(true);
        QCOMPARE(bridge.tracks()->count(), 5);
        config.setHideIgnored(false);
        QCOMPARE(bridge.tracks()->count(), 6);
    }
    void bearerPollingAndAccess() {
        MockBackend server;
        server.state.insert("access_token", "must-not-reach-qml");
        auto user = server.state.value("user").toObject();
                user.insert("refresh_token", "must-not-reach-qml");
                user.insert("user_metadata", QJsonObject{{"full_name", "Test User"}, {"picture", "https://example.com/avatar.png"}, {"avatar_url", "file:///etc/passwd"}, {"secret", "must-not-reach-qml"}});
                user.insert("identities", QJsonArray{
                    QJsonObject{{"provider", "discord"}, {"access_token", "secret"}, {"identity_data", QJsonObject{
                        {"avatar_url", "https://example.com/identity.png"}, {"picture", "https://user:password@example.com/avatar"}, {"email", "private"}}}},
                    QJsonObject{{"provider", "github"}, {"identity_data", QJsonObject{{"picture", "file:///etc/passwd"}}}}
                });
                server.state.insert("user", user);
        Config config{QString()};
        Bridge bridge(server.url(), "test-random-key", nullptr, &config);
        bridge.setHideIgnored(false);
        QTRY_COMPARE(bridge.tracks()->count(), 6);
        QVERIFY(!bridge.state().contains("access_token"));
        QVERIFY(!bridge.state().value("user").toMap().contains("refresh_token"));
        const auto metadata = bridge.state().value("user").toMap().value("user_metadata").toMap();
                QCOMPARE(metadata.value("full_name").toString(), QString("Test User"));
                QCOMPARE(metadata.value("picture").toString(), QString("https://example.com/avatar.png"));
                QVERIFY(!metadata.contains("avatar_url"));
                QVERIFY(!metadata.contains("secret"));
                const auto identities = bridge.state().value("user").toMap().value("identities").toList();
                QCOMPARE(identities.size(), 2);
                const auto identity = identities.first().toMap();
                QCOMPARE(identity.value("provider").toString(), QString("discord"));
                QVERIFY(!identity.contains("access_token"));
                const auto identityData = identity.value("identity_data").toMap();
                QCOMPARE(identityData.size(), 1);
                QCOMPARE(identityData.value("avatar_url").toString(), QString("https://example.com/identity.png"));
                QVERIFY(identities.last().toMap().value("identity_data").toMap().isEmpty());
                QVERIFY(bridge.metaObject()->indexOfProperty("key") < 0);
        QSignalSpy changed(bridge.tracks(), &QAbstractItemModel::dataChanged);
        QSignalSpy reset(bridge.tracks(), &QAbstractItemModel::modelReset);
        QSignalSpy stateChanged(&bridge, &Bridge::stateChanged);
        const auto before = server.requests.size();
        QTRY_VERIFY(server.requests.size() > before);
        QTest::qWait(30);
        QCOMPARE(changed.count(), 0); QCOMPARE(reset.count(), 0); QCOMPARE(stateChanged.count(), 0);
        QVERIFY(server.notModified > 0);
        QVERIFY(server.requests.last().contains("If-None-Match: "));
        bridge.login("github"); bridge.load(); bridge.fetch(); bridge.ignore("101", true); bridge.logout();
        QTRY_VERIFY(server.requests.size() >= before + 6);
        QList<QByteArray> paths;
        for (const auto &req : server.requests) {
            QVERIFY(req.contains("Authorization: Bearer test-random-key\r\n"));
            paths.append(req.split('\n').first());
        }
        for (const auto &path : {"login", "logout", "load", "fetch", "ignore"}) {
            bool found = false;
            for (const auto &req : server.requests) if (req.startsWith(QByteArray("POST /api/") + path + " ")) found = true;
            QVERIFY2(found, path);
        }
        server.state.insert("isBanned", true);
        QTRY_COMPARE(bridge.tracks()->count(), 0); QCOMPARE(bridge.cards()->count(), 0);
        server.state.insert("isBanned", false); server.state.insert("hasAccess", false);
        QTRY_VERIFY(!bridge.state().value("hasAccess").toBool()); QCOMPARE(bridge.tracks()->count(), 0);
    }
    void postRefreshDuringConditionalPoll() {
        MockBackend server;
        Config config{QString()};
        Bridge bridge(server.url(), "test-random-key", nullptr, &config);
        QTRY_COMPARE(bridge.tracks()->count(), 6);
        server.getDelayMs = 250;
        const int before = server.requests.size();
        QTRY_VERIFY(server.requests.size() > before);
        QVERIFY(server.requests.last().contains("If-None-Match: "));
        server.state.insert("error", "changed during poll");
        bridge.load();
        QTRY_COMPARE(bridge.state().value("error").toString(), QString("changed during poll"));
        bool postSeen = false, forcedSeen = false;
        for (int i = before; i < server.requests.size(); ++i) {
            const auto req = server.requests[i];
            if (req.startsWith("POST /api/load ")) postSeen = true;
            if (postSeen && req.startsWith("GET /api/state ") && !req.contains("If-None-Match:")) forcedSeen = true;
        }
        QVERIFY(forcedSeen);
    }
    void nativeQmlInteractions() {
        Config config{QString()};
        MockBackend server;
        Bridge bridge(server.url(), "test-random-key", nullptr, &config);
        QTRY_COMPARE(bridge.tracks()->count(), 6);
        bridge.setHideIgnored(false);
        MusicPlayer musicPlayer;
        LocalContexts local(config);
        QQmlApplicationEngine engine;
        local.attach(engine);
        engine.rootContext()->setContextProperty("appConfig", &config);
        engine.rootContext()->setContextProperty("musicPlayer", &musicPlayer);
        engine.rootContext()->setContextProperty("backend", &bridge);
        QSignalSpy warnings(&engine, &QQmlEngine::warnings);
        engine.load(QUrl("qrc:/Main.qml"));
        QVERIFY(!engine.rootObjects().isEmpty());
        auto window = qobject_cast<QQuickWindow *>(engine.rootObjects().first());
        QVERIFY(window);
        QCOMPARE(window->minimumWidth(), 1200);
        QCOMPARE(window->minimumHeight(), 750);
        // Exercise responsive layouts independently of the production window limit.
        window->setMinimumSize(QSize(320, 320));
        window->resize(390, 740);
        QTest::qWait(100);
        QCOMPARE(window->property("mobile").toBool(), true);
                for (const auto &name : {"windowMinimizeButton", "windowMaximizeButton", "windowCloseButton", "avatarButton"}) {
                    auto button = window->findChild<QObject *>(name);
                    QVERIFY(button);
                    QVERIFY(button->property("visible").toBool());
                }
                auto avatar = window->findChild<QObject *>("avatarButton");
                QCOMPARE(avatar->property("leftPadding").toInt(), 0);
                QCOMPARE(avatar->property("rightPadding").toInt(), 0);
                auto avatarContent = avatar->property("contentItem").value<QObject *>();
                QVERIFY(avatarContent);
                QCOMPARE(avatarContent->property("width").toInt(), 32);
                QCOMPARE(avatarContent->property("height").toInt(), 32);
                QVERIFY(QMetaObject::invokeMethod(avatar, "clicked"));
                auto account = window->findChild<QObject *>("accountMenu");
                QVERIFY(account); QCOMPARE(account->property("visible").toBool(), true);
                QVERIFY(QMetaObject::invokeMethod(account, "close"));
                QVERIFY(window->flags().testFlag(Qt::FramelessWindowHint));
                auto titleBar = window->findChild<QObject *>("titleBar");
                QVERIFY(titleBar); QCOMPARE(titleBar->property("height").toInt(), 0);
                auto settingsEntry = account->findChild<QObject *>("accountSettingsEntry");
                QVERIFY(settingsEntry);
                QVERIFY(QMetaObject::invokeMethod(settingsEntry, "triggered"));
                auto dialog = window->findChild<QObject *>("settingsDialog");
                QVERIFY(dialog); QCOMPARE(dialog->property("visible").toBool(), true);
                QVERIFY(!window->findChild<QObject *>("themeSetting"));
                auto hide = window->findChild<QObject *>("hideIgnoredSetting");
                QVERIFY(hide); hide->setProperty("checked", true);
                QVERIFY(QMetaObject::invokeMethod(hide, "toggled"));
                QVERIFY(bridge.hideIgnored());
                bridge.setHideIgnored(false);
                auto hideControls = window->findChild<QObject *>("hideWindowControlsSetting");
                QVERIFY(hideControls);
                hideControls->setProperty("checked", true);
                QVERIFY(QMetaObject::invokeMethod(hideControls, "toggled"));
                QVERIFY(config.hideWindowControls());
                for (const auto name : {"windowMinimizeButton", "windowMaximizeButton", "windowCloseButton"}) {
                    auto button = window->findChild<QObject *>(name);
                    QVERIFY(button);
                    QTRY_VERIFY(!button->property("visible").toBool());
                }
                config.setHideWindowControls(false);
                QVERIFY(QMetaObject::invokeMethod(dialog, "close"));
        auto track = bridge.tracks()->data(bridge.tracks()->index(1), Qt::UserRole);
        QVERIFY(QMetaObject::invokeMethod(window, "choose", Q_ARG(QVariant, track)));
        QCOMPARE(window->property("selectedId").toString(), QString("102"));
        QCOMPARE(window->property("sheetOpen").toBool(), true);
        QCOMPARE(QGuiApplication::clipboard()->text(), QString("102"));
        QVERIFY(QMetaObject::invokeMethod(window, "toggleExpanded", Q_ARG(QVariant, QVariant("102"))));
        QTest::qWait(400);
        QCOMPARE(window->property("bg").value<QColor>(), QColor("#000000"));
        QCOMPARE(window->property("surface").value<QColor>(), QColor("#0c0c0e"));
        QCOMPARE(window->property("surface2").value<QColor>(), QColor("#171719"));
        QCOMPARE(window->property("accent").value<QColor>(), QColor("#2bfef5"));
        QVERIFY(!window->grabWindow().isNull());
                const auto screenshots = qEnvironmentVariable("DDETECTOR_TEST_SCREENSHOT_DIR");
                if (!screenshots.isEmpty()) QVERIFY(window->grabWindow().save(screenshots + "/mobile-expanded-dark.png"));
        window->resize(1200, 800); QTest::qWait(50);
        QCOMPARE(window->property("mobile").toBool(), false);
        QCOMPARE(engine.rootContext()->contextProperty("gitWorkspace").value<QObject *>(),
                 static_cast<QObject *>(&local.workspace));
        QCOMPARE(engine.rootContext()->contextProperty("localUploader").value<QObject *>(),
                 static_cast<QObject *>(&local.uploader));
        QQuickItem *workspaceView = nullptr;
        QQuickItem *lyricsButton = nullptr;
        QQuickItem *gitButton = nullptr;
        QObject *sendDialog = nullptr;
        for (auto object : window->findChildren<QObject *>()) {
            if (object->objectName() == "gitWorkspaceView")
                workspaceView = qobject_cast<QQuickItem *>(object);
            if (object->metaObject()->indexOfProperty("uploadBackend") >= 0)
                sendDialog = object;
        }
        const auto findPageButtons = [&](auto &&visit, QQuickItem *item) -> void {
            if (item->metaObject()->indexOfSignal("clicked()") >= 0) {
                if (item->objectName() == "lyricsPageButton") lyricsButton = item;
                if (item->objectName() == "gitPageButton") gitButton = item;
            }
            for (auto child : item->childItems()) visit(visit, child);
        };
        findPageButtons(findPageButtons, window->contentItem());
        QVERIFY(workspaceView); QVERIFY(lyricsButton); QVERIFY(gitButton);
        QCOMPARE(workspaceView->property("workspaceBackend").value<QObject *>(),
                 static_cast<QObject *>(&local.workspace));
        QCOMPARE(window->property("rightPage").toInt(), 0);
        QVERIFY(!workspaceView->isVisible());
        QVERIFY(QMetaObject::invokeMethod(gitButton, "clicked"));
        QCOMPARE(window->property("rightPage").toInt(), 1);
        QTRY_VERIFY(workspaceView->isVisible());
        QVERIFY(QMetaObject::invokeMethod(lyricsButton, "clicked"));
        QCOMPARE(window->property("rightPage").toInt(), 0);
        QTRY_VERIFY(!workspaceView->isVisible());

        QVERIFY(sendDialog);
        QCOMPARE(sendDialog->property("uploadBackend").value<QObject *>(),
                 static_cast<QObject *>(&local.uploader));
        const auto snapshot = track.toMap();
        QVERIFY(!snapshot.isEmpty());
        QVERIFY(QMetaObject::invokeMethod(sendDialog, "openTrack",
                                         Q_ARG(QVariant, snapshot.value("id")),
                                         Q_ARG(QVariant, snapshot.value("title")),
                                         Q_ARG(QVariant, snapshot.value("artist")),
                                         Q_ARG(QVariant, QVariant(QString()))));
        QTRY_VERIFY(sendDialog->property("visible").toBool());
        QCOMPARE(sendDialog->property("trackId").toString(), snapshot.value("id").toString());
        QCOMPARE(sendDialog->property("originalTitle").toString(), snapshot.value("title").toString());
        QVERIFY(!sendDialog->property("busy").toBool());
        QVERIFY(!local.uploader.busy());
        QVERIFY(QMetaObject::invokeMethod(sendDialog, "close"));
        QTRY_VERIFY(!sendDialog->property("visible").toBool());
        QCOMPARE(warnings.count(), 0);
    }
    void preferredAvatarPriority() {
        Config config{QString()};
        MockBackend server;
        Bridge bridge(server.url(), "test-random-key", nullptr, &config);
        QTRY_COMPARE(bridge.tracks()->count(), 6);
        MusicPlayer musicPlayer;
        LocalContexts local(config);
        QQmlApplicationEngine engine;
        local.attach(engine);
        engine.rootContext()->setContextProperty("appConfig", &config);
        engine.rootContext()->setContextProperty("backend", &bridge);
        engine.rootContext()->setContextProperty("musicPlayer", &musicPlayer);
        engine.load(QUrl("qrc:/Main.qml"));
        QVERIFY(!engine.rootObjects().isEmpty());
        auto window = engine.rootObjects().first();
        const auto identity = [](const QString &provider, const QString &url) {
            return QVariantMap{{"provider", provider}, {"identity_data", QVariantMap{{"avatar_url", url}}}};
        };
        const QVariantMap metadata{{"avatar_url", "https://example.com/metadata.png"}};
        const QList<QPair<QVariantMap, QString>> cases{
            {{{"user_metadata", metadata}, {"identities", QVariantList{identity("spotify", "https://example.com/spotify.png"), identity("discord", "https://example.com/discord.png"), identity("github", "https://example.com/github.png")}}}, "https://example.com/github.png"},
            {{{"user_metadata", metadata}, {"identities", QVariantList{identity("spotify", "https://example.com/spotify.png"), identity("discord", "https://example.com/discord.png")}}}, "https://example.com/discord.png"},
            {{{"user_metadata", metadata}, {"identities", QVariantList{identity("github", "")}}}, ""},
            {{{"user_metadata", metadata}}, "https://example.com/metadata.png"}
        };
        for (const auto &test : cases) {
            QVariant result;
            QVERIFY(QMetaObject::invokeMethod(window, "preferredAvatar", Q_RETURN_ARG(QVariant, result),
                                             Q_ARG(QVariant, QVariant(test.first))));
            QCOMPARE(result.toString(), test.second);
        }
        auto image = window->findChild<QQuickItem *>("accountAvatarImage");
        QVERIFY(image);
        QVERIFY(qobject_cast<AvatarImage *>(image));
        QCOMPARE(image->width(), 32.0);
        QCOMPARE(image->height(), 32.0);
        QVERIFY(!image->property("ready").toBool());
    }
    void musicPlayerUi_data() {
        QTest::addColumn<QString>("feature");
        QTest::newRow("inactive-panel") << QString("panel");
        QTest::newRow("password-token-field") << QString("token");
        QTest::newRow("play-menu-entry") << QString("menu");
    }
    void musicPlayerUi() {
        QFETCH(QString, feature);
        Config config{QString()};
        MockBackend server;
        Bridge bridge(server.url(), "test-random-key", nullptr, &config);
        QTRY_COMPARE(bridge.tracks()->count(), 6);
        bridge.setHideIgnored(false);
        MusicPlayer musicPlayer;
        LocalContexts local(config);
        QQmlApplicationEngine engine;
        local.attach(engine);
        engine.rootContext()->setContextProperty("appConfig", &config);
        engine.rootContext()->setContextProperty("backend", &bridge);
        engine.rootContext()->setContextProperty("musicPlayer", &musicPlayer);
        QSignalSpy warnings(&engine, &QQmlEngine::warnings);
        engine.load(QUrl("qrc:/Main.qml"));
        QVERIFY(!engine.rootObjects().isEmpty());
        auto window = qobject_cast<QQuickWindow *>(engine.rootObjects().first());
        QVERIFY(window);
        QCOMPARE(engine.rootContext()->contextProperty("musicPlayer").value<QObject *>(),
                 static_cast<QObject *>(&musicPlayer));
        QVERIFY(!musicPlayer.enabled());
        QVERIFY(!musicPlayer.busy());
        QVERIFY(!musicPlayer.playing());
        if (feature == "panel") {
            auto panel = window->findChild<QQuickItem *>("playerPanel");
            QVERIFY2(panel, "Main.qml must expose the player panel with objectName: playerPanel");
            QVERIFY(!panel->isVisible());
            QCOMPARE(panel->height(), 0.0);
            QVERIFY(!panel->isEnabled());
            auto timeline = window->findChild<QQuickItem *>("playerTimeline");
            QVERIFY(timeline);
            for (const int width : {1200, 1440}) {
                window->setWidth(width);
                QCoreApplication::processEvents();
                const auto center = timeline->mapToItem(panel, QPointF(timeline->width() / 2, timeline->height() / 2));
                QVERIFY(qAbs(center.x() - panel->width() / 2) < 1);
            }
        } else if (feature == "token") {
            auto dialog = window->findChild<QObject *>("settingsDialog");
            QVERIFY(dialog);
            QVERIFY(QMetaObject::invokeMethod(dialog, "open"));
            QTRY_VERIFY(dialog->property("visible").toBool());
            QObject *tokenField = nullptr;
            auto content = dialog->property("contentItem").value<QObject *>();
            QVERIFY(content);
            for (auto object : content->findChildren<QObject *>()) {
                if (object->metaObject()->indexOfProperty("echoMode") >= 0
                    && (object->objectName().contains("token", Qt::CaseInsensitive)
                        || object->property("placeholderText").toString().contains("token", Qt::CaseInsensitive))) {
                    tokenField = object;
                    break;
                }
            }
            QVERIFY2(tokenField, "Settings must contain a named or labelled OAuth token field");
            // TextInput.Password is 2; Qt Quick's enum has no public C++ header.
            QCOMPARE(tokenField->property("echoMode").toInt(), 2);
            QVERIFY(tokenField->property("text").toString().isEmpty());
            auto item = qobject_cast<QQuickItem *>(tokenField);
            QVERIFY(item); QVERIFY(item->isVisible());
            for (auto object : content->findChildren<QObject *>()) {
                QVERIFY(object->property("text").toString() != "Apply token");
                QVERIFY(object->property("text").toString() != "Disconnect");
            }
            tokenField->setProperty("text", "test-token");
            QVERIFY(QMetaObject::invokeMethod(tokenField, "textEdited"));
            QTRY_COMPARE(config.musicToken(), QString("test-token"));
            tokenField->setProperty("text", "");
            QVERIFY(QMetaObject::invokeMethod(tokenField, "accepted"));
            QCOMPARE(config.musicToken(), QString());
            QVERIFY(QMetaObject::invokeMethod(dialog, "close"));
        } else {
            const auto track = bridge.tracks()->data(bridge.tracks()->index(0), Qt::UserRole);
            window->setProperty("menuTrack", track);
            QObject *playEntry = nullptr;
            for (auto object : window->findChildren<QObject *>()) {
                if (object->property("text").toString() == "Play"
                    && object->metaObject()->indexOfSignal("triggered()") >= 0) {
                    playEntry = object;
                    break;
                }
            }
            QVERIFY2(playEntry, "Track menu must contain a Play entry");
            QVERIFY(!playEntry->property("enabled").toBool());
        }
        QCOMPARE(warnings.count(), 0);
        QVERIFY(!musicPlayer.enabled());
        QVERIFY(!musicPlayer.playing());
    }
    void cachedTracksRemainVisibleWhileLoading() {
        MockBackend server;
        server.state.insert("loading", true);
        server.state.insert("processing", false);
        Config config{QString()};
        Bridge bridge(server.url(), "test-random-key", nullptr, &config);
        QTRY_COMPARE(bridge.tracks()->count(), 6);
        MusicPlayer musicPlayer;
        LocalContexts local(config);
        QQmlApplicationEngine engine;
        local.attach(engine);
        engine.rootContext()->setContextProperty("appConfig", &config);
        engine.rootContext()->setContextProperty("musicPlayer", &musicPlayer);
        engine.rootContext()->setContextProperty("backend", &bridge);
        engine.load(QUrl("qrc:/Main.qml"));
        QVERIFY(!engine.rootObjects().isEmpty());
        auto window = qobject_cast<QQuickWindow *>(engine.rootObjects().first());
        QVERIFY(window);
        auto overlay = window->findChild<QQuickItem *>("tracksLoadingOverlay");
        QVERIFY(overlay);
        QVERIFY(!overlay->isVisible());
        server.state.insert("tracks", QJsonArray{});
        QTRY_COMPARE(bridge.tracks()->count(), 0);
        QTRY_VERIFY(overlay->isVisible());
        server.state.insert("processing", true);
        QTRY_VERIFY(!overlay->isVisible());
    }
    void accessDeniedModal() {
        MockBackend server;
        server.state.insert("authenticated", true);
        server.state.insert("hasAccess", false);
        server.state.insert("accessChecked", true);
        server.state.insert("error", "DDetector access denied");
        Config config{QString()};
        Bridge bridge(server.url(), "test-random-key", nullptr, &config);
        QTRY_VERIFY(bridge.state().contains("authenticated"));
        MusicPlayer musicPlayer;
        LocalContexts local(config);
        QQmlApplicationEngine engine;
        local.attach(engine);
        engine.rootContext()->setContextProperty("appConfig", &config);
        engine.rootContext()->setContextProperty("musicPlayer", &musicPlayer);
        engine.rootContext()->setContextProperty("backend", &bridge);
        QSignalSpy warnings(&engine, &QQmlEngine::warnings);
        engine.load(QUrl("qrc:/Main.qml"));
        QVERIFY(!engine.rootObjects().isEmpty());
        auto window = qobject_cast<QQuickWindow *>(engine.rootObjects().first());
        QVERIFY(window);
        auto popup = window->findChild<QObject *>("accessPopup");
        QVERIFY(popup);
        QTRY_VERIFY(popup->property("visible").toBool());
        auto artwork = visualChild(window->contentItem(), "accessArtwork");
        QVERIFY(artwork);
        QVERIFY(artwork->isVisible());
        QCOMPARE(artwork->property("source").toUrl(), QUrl("qrc:/assets/Kagami-access-denied.jpg"));
        QTRY_COMPARE(artwork->property("status").toInt(), 1); // Image.Ready
        auto message = visualChild(window->contentItem(), "accessMessage");
        QVERIFY(message);
        QCOMPARE(message->property("text").toString(), QString("Access denied"));
        QCOMPARE(message->property("font").value<QFont>().family(), QString("Luckystar"));
        auto logout = visualChild(window->contentItem(), "accessSwitchAccount");
        QVERIFY(logout);
        QCOMPARE(logout->property("text").toString(), QString("Sign in with another account"));
        QCOMPARE(logout->height(), 46.0);
        QCOMPARE(logout->width(), message->width());
        QVERIFY(QMetaObject::invokeMethod(logout, "clicked"));
        QTRY_VERIFY(std::any_of(server.requests.cbegin(), server.requests.cend(), [](const QByteArray &request) {
            return request.startsWith("POST /api/logout ");
        }));
        QCOMPARE(warnings.count(), 0);
    }
    void unavailableAndAuthModal() {
        MockBackend server;
        server.state.insert("authenticated", false); server.state.insert("hasAccess", false);
        server.state.insert("tracks", QJsonArray{});
        Config config{QString()};
        Bridge bridge(server.url(), "test-random-key", nullptr, &config);
        QTRY_VERIFY(bridge.state().contains("authenticated"));
        MusicPlayer musicPlayer;
        LocalContexts local(config);
        QQmlApplicationEngine engine;
        local.attach(engine);
        engine.rootContext()->setContextProperty("appConfig", &config);
        engine.rootContext()->setContextProperty("musicPlayer", &musicPlayer);
        engine.rootContext()->setContextProperty("backend", &bridge);
        QSignalSpy warnings(&engine, &QQmlEngine::warnings);
        engine.load(QUrl("qrc:/Main.qml"));
        QVERIFY(!engine.rootObjects().isEmpty());
        auto window = qobject_cast<QQuickWindow *>(engine.rootObjects().first());
        QVERIFY(window);
        QCOMPARE(window->property("privileged").toBool(), false);
        QQuickItem *github = nullptr;
                QTRY_VERIFY((github = visualChild(window->contentItem(), "login-github")) && github->isVisible());
                QVERIFY(!window->findChild<QObject *>("avatarButton")->property("visible").toBool());
                auto popup = window->findChild<QObject *>("authPopup");
        QVERIFY(popup);
        QTRY_VERIFY(popup->property("visible").toBool());
        QVERIFY(window->property("showShell").toBool());
        auto artwork = visualChild(window->contentItem(), "authArtwork");
        QVERIFY(artwork);
        QVERIFY(artwork->isVisible());
        auto title = visualChild(window->contentItem(), "authTitle");
        QVERIFY(title);
        QCOMPARE(title->property("font").value<QFont>().family(), QString("Luckystar"));
        QCOMPARE(popup->property("closePolicy").toInt(), 0);
        QTest::keyClick(window, Qt::Key_Escape);
        QTest::qWait(100);
        QVERIFY(popup->property("visible").toBool());
        QTest::mouseClick(window, Qt::LeftButton, Qt::NoModifier, QPoint(5, 100));
        QTest::qWait(100);
        QVERIFY(popup->property("visible").toBool());
        const auto screenshots = qEnvironmentVariable("DDETECTOR_TEST_SCREENSHOT_DIR");
        if (!screenshots.isEmpty()) QVERIFY(window->grabWindow().save(screenshots + "/auth-desktop.png"));
        window->resize(390, 740); QTest::qWait(100);
        QVERIFY(!artwork->isVisible());
        if (!screenshots.isEmpty()) QVERIFY(window->grabWindow().save(screenshots + "/auth-mobile.png"));
        auto discord = visualChild(window->contentItem(), "login-discord");
        QVERIFY(discord); QVERIFY(QMetaObject::invokeMethod(discord, "clicked"));
        bridge.login("spotify");
        QCOMPARE(bridge.pendingProvider(), QString("discord"));
        QTRY_VERIFY(server.requests.size() >= 2);
        int logins = 0;
        for (const auto &req : server.requests) if (req.startsWith("POST /api/login ")) {
            ++logins; QVERIFY(req.contains("\"provider\":\"discord\""));
        }
        QCOMPARE(logins, 1);
        QCOMPARE(warnings.count(), 0);
    }
};
QTEST_MAIN(NativeTests)
#include "tests.moc"

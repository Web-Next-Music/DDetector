#include "bridge.h"
#include "player.h"
#include <QAbstractItemModelTester>
#include <QClipboard>
#include <QFile>
#include <QDirIterator>
#include <QFontDatabase>
#include <QGuiApplication>
#include <QJsonDocument>
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
    MockBackend() {
        QFile demo(":/assets/demo.json");
                if (!demo.open(QIODevice::ReadOnly)) qFatal("Bundled fixture is missing");
        state = QJsonDocument::fromJson(demo.readAll()).object();
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
                    auto response = QJsonDocument(get ? state : QJsonObject{{"ok", true}, {"browser_opened", true}}).toJson(QJsonDocument::Compact);
                    socket->write("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nConnection: close\r\nContent-Length: " + QByteArray::number(response.size()) + "\r\n\r\n" + response);
                    socket->disconnectFromHost();
                });
                connect(socket, &QTcpSocket::disconnected, socket, &QObject::deleteLater);
            }
        });
    }
    QUrl url() const { return QUrl("http://127.0.0.1:" + QString::number(serverPort())); }
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
        QDirIterator fonts(":/assets", {"*.ttf", "*.otf"}, QDir::Files);
        while (fonts.hasNext()) QFontDatabase::addApplicationFont(fonts.next());
        QFont font("DDetector Nunito"); font.setWeight(QFont::Bold); font.setPixelSize(13);
        QGuiApplication::setFont(font);
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
        Bridge bridge({}, {}, true);
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
        Bridge restored({}, {}, true);
        QVERIFY(restored.hideIgnored());
        QVERIFY(bridge.metaObject()->indexOfProperty("theme") < 0);
        bridge.setHideIgnored(false);
        bridge.setSort(1);
        QCOMPARE(bridge.tracks()->indexOf("101"), 0);
        QCOMPARE(bridge.tracks()->data(bridge.tracks()->index(0), Qt::UserRole).toMap().value("date").toString(), QString("10/08/2026"));
        bridge.ignore("101", true); QCOMPARE(bridge.cards()->data(bridge.cards()->index(0), Qt::UserRole).toMap().value("ignored").toBool(), true);
    }
    void bearerPollingAndAccess() {
        MockBackend server;
        server.state.insert("access_token", "must-not-reach-qml");
        auto user = server.state.value("user").toObject();
                user.insert("refresh_token", "must-not-reach-qml");
                user.insert("user_metadata", QJsonObject{{"full_name", "Test User"}, {"picture", "https://example.com/avatar.png"}, {"avatar_url", "file:///etc/passwd"}, {"secret", "must-not-reach-qml"}});
                server.state.insert("user", user);
        Bridge bridge(server.url(), "test-random-key", false);
        bridge.setHideIgnored(false);
        QTRY_COMPARE(bridge.tracks()->count(), 6);
        QVERIFY(!bridge.state().contains("access_token"));
        QVERIFY(!bridge.state().value("user").toMap().contains("refresh_token"));
        const auto metadata = bridge.state().value("user").toMap().value("user_metadata").toMap();
                QCOMPARE(metadata.value("full_name").toString(), QString("Test User"));
                QCOMPARE(metadata.value("picture").toString(), QString("https://example.com/avatar.png"));
                QVERIFY(!metadata.contains("avatar_url"));
                QVERIFY(!metadata.contains("secret"));
                QVERIFY(bridge.metaObject()->indexOfProperty("key") < 0);
        QSignalSpy changed(bridge.tracks(), &QAbstractItemModel::dataChanged);
        QSignalSpy reset(bridge.tracks(), &QAbstractItemModel::modelReset);
        QSignalSpy stateChanged(&bridge, &Bridge::stateChanged);
        const auto before = server.requests.size();
        QTRY_VERIFY(server.requests.size() > before);
        QTest::qWait(30);
        QCOMPARE(changed.count(), 0); QCOMPARE(reset.count(), 0); QCOMPARE(stateChanged.count(), 0);
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
    void nativeQmlInteractions() {
        Bridge bridge({}, {}, true);
        bridge.setHideIgnored(false);
        MusicPlayer musicPlayer;
        QQmlApplicationEngine engine;
        engine.rootContext()->setContextProperty("musicPlayer", &musicPlayer);
        engine.rootContext()->setContextProperty("backend", &bridge);
        engine.rootContext()->setContextProperty("demoMode", true);
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
                QCOMPARE(avatarContent->property("width").toInt(), 34);
                QCOMPARE(avatarContent->property("height").toInt(), 34);
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
        QCOMPARE(warnings.count(), 0);
    }
    void musicPlayerUi_data() {
        QTest::addColumn<QString>("feature");
        QTest::newRow("inactive-panel") << QString("panel");
        QTest::newRow("password-token-field") << QString("token");
        QTest::newRow("play-menu-entry") << QString("menu");
    }
    void musicPlayerUi() {
        QFETCH(QString, feature);
        Bridge bridge({}, {}, true);
        bridge.setHideIgnored(false);
        MusicPlayer musicPlayer;
        QQmlApplicationEngine engine;
        engine.rootContext()->setContextProperty("backend", &bridge);
        engine.rootContext()->setContextProperty("musicPlayer", &musicPlayer);
        engine.rootContext()->setContextProperty("demoMode", true);
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
            QVERIFY(panel->isVisible());
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
    void unavailableAndAuthModal() {
        MockBackend server;
        server.state.insert("authenticated", false); server.state.insert("hasAccess", false);
        server.state.insert("tracks", QJsonArray{});
        Bridge bridge(server.url(), "test-random-key", false);
        QTRY_VERIFY(bridge.state().contains("authenticated"));
        MusicPlayer musicPlayer;
        QQmlApplicationEngine engine;
        engine.rootContext()->setContextProperty("musicPlayer", &musicPlayer);
        engine.rootContext()->setContextProperty("backend", &bridge);
        engine.rootContext()->setContextProperty("demoMode", false);
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
        QVERIFY(popup); QVERIFY(QMetaObject::invokeMethod(popup, "open"));
        QTest::qWait(200);
        QCOMPARE(popup->property("visible").toBool(), true);
        const auto screenshots = qEnvironmentVariable("DDETECTOR_TEST_SCREENSHOT_DIR");
        if (!screenshots.isEmpty()) QVERIFY(window->grabWindow().save(screenshots + "/auth-desktop.png"));
        window->resize(390, 740); QTest::qWait(100);
        if (!screenshots.isEmpty()) QVERIFY(window->grabWindow().save(screenshots + "/auth-mobile.png"));
        QVERIFY(QMetaObject::invokeMethod(popup, "close"));
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

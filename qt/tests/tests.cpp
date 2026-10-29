#include "bridge.h"
#include "posixshutdown.h"
#include <QApplication>
#include <QProcess>
#include <cstdio>
#include "player.h"
#include "avatarimage.h"
#include "workspace.h"
#include "terminal.h"
#include "syntaxhighlighter.h"
#include "lyricslayout.h"
#include "uploader.h"
#include "repositorycoordinator.h"
#include <optional>
#include <QElapsedTimer>
#include <algorithm>
#include <QAbstractItemModelTester>
#include <QClipboard>
#include <QDesktopServices>
#include <QFile>
#include <QDataStream>
#include <QMediaPlayer>
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
#include <QQmlProperty>
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
    bool holdConnection = false;
    std::function<std::optional<QByteArray>(const QByteArray &, QTcpSocket *)> wireHook;
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
                    if (socket->property("handled").toBool()) return;
                    socket->setProperty("handled", true);
                    requests.append(*buffer);
                    if (wireHook) {
                        const auto wire = wireHook(*buffer, socket);
                        if (wire) {
                            socket->write(*wire);
                            if (!holdConnection) socket->disconnectFromHost();
                            return;
                        }
                    }
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

    RepositoryCoordinator repositories;
    explicit LocalContexts(Config &config) : repositories(config, workspace, uploader) {}

    void attach(QQmlApplicationEngine &engine, Config &config, Bridge &bridge, MusicPlayer &player) {
        engine.rootContext()->setContextProperty("gitWorkspace", &workspace);
        engine.rootContext()->setContextProperty("localUploader", &uploader);
        engine.rootContext()->setContextProperty("appConfig", &config);
        engine.rootContext()->setContextProperty("backend", &bridge);
        engine.rootContext()->setContextProperty("musicPlayer", &player);
        engine.setInitialProperties({
            {"config", QVariant::fromValue(&config)},
            {"backendBridge", QVariant::fromValue(&bridge)},
            {"player", QVariant::fromValue(&player)},
            {"workspace", QVariant::fromValue(&workspace)},
            {"uploader", QVariant::fromValue(&uploader)},
            {"gitTag", QStringLiteral("dev")},
            {"developmentToolsEnabled", false},
            {"updater", QVariant::fromValue(static_cast<QObject *>(nullptr))}
        });
    }
};

class NativeTests final : public QObject {
    Q_OBJECT
    QTemporaryDir settings;
public:
    static int runSignalFlushChild(const QString &path) {
        PosixShutdown shutdown;
        if (!shutdown.isInstalled()) return 10;
        Config config(path);
        config.m_saveDebounce.setInterval(60000);
        bool quitting = false, flushed = false;
        QObject::connect(qApp, &QCoreApplication::aboutToQuit, &config, [&] {
            quitting = true;
            flushed = config.flush();
        });
        QTimer::singleShot(0, &config, [&] {
            config.setMusicToken("signal-pending-token");
            config.setVolume(0.73);
            if (QFile::exists(path)) { qApp->exit(11); return; }
            std::fputs("READY\n", stdout);
            std::fflush(stdout);
        });
        QTimer::singleShot(10000, qApp, [] { qApp->exit(12); });
        const int result = qApp->exec();
        return result == 0 && shutdown.wasRequested() && quitting && flushed ? 0 : 13;
    }
private slots:
    void censorBridgeAndView() {
        MockBackend server;
        QJsonObject censor{{"configured", true}, {"paused", false}, {"settings", QJsonObject{}},
            {"jobs", QJsonArray{QJsonObject{{"id", "job-1"}, {"status", "running"}, {"stage", "analysis"},
                {"request", QJsonObject{{"id", "request-1"}, {"track_id", 42}, {"title", "Track"}, {"artist", "Artist"}}},
                {"result", QJsonObject{{"duration_seconds", 12}, {"waveform", QJsonArray{0.2, 0.8}}}},
                {"candidates", QJsonArray{QJsonObject{{"id", "candidate-1"}, {"title", "Original"}}}}}}}};
        QJsonObject command;
        bool unavailable = false;
        server.wireHook = [&](const QByteArray &request, QTcpSocket *) -> std::optional<QByteArray> {
            if (!request.startsWith("GET /api/censor/state ") && !request.startsWith("POST /api/censor/control ")) return std::nullopt;
            const bool get = request.startsWith("GET ");
            if (!get) command = QJsonDocument::fromJson(request.mid(request.indexOf("\r\n\r\n") + 4)).object();
            const QByteArray data = QJsonDocument(censor).toJson(QJsonDocument::Compact);
            return QByteArray(unavailable ? "HTTP/1.1 404 Not Found\r\n" : "HTTP/1.1 200 OK\r\n")
                + "Connection: close\r\nContent-Type: application/json\r\nContent-Length: " + QByteArray::number(data.size()) + "\r\n\r\n" + data;
        };
        Bridge bridge(server.url(), "censor-test-key");
        QTRY_VERIFY(bridge.censorState().value("configured").toBool());
        QTRY_VERIFY(!bridge.state().isEmpty());
        const auto normalState = bridge.state();
        bridge.censorCommand("approve", {{"id", "job-1"}, {"candidate_id", "candidate-1"}, {"action", "stop"}});
        QTRY_COMPARE(command.value("action").toString(), QString("approve"));
        QCOMPARE(command.value("candidate_id").toString(), QString("candidate-1"));
        QTRY_VERIFY(!bridge.censorBusy());
        QCOMPARE(bridge.state(), normalState);
        for (const QString &action : {"fetch", "start", "pause", "resume", "stop", "retry", "analyze", "search", "approve", "reject", "defer", "rollback", "settings"}) {
            bridge.censorCommand(action, {{"id", "job-1"}});
            QTRY_COMPARE(command.value("action").toString(), action);
            QTRY_VERIFY(!bridge.censorBusy());
        }
        QQmlEngine engine;
        QQmlComponent component(&engine, QUrl("qrc:/CensorDetector.qml"));
        QVERIFY2(component.isReady(), qPrintable(component.errorString()));
        QScopedPointer<QObject> view(component.createWithInitialProperties({
            {"bridge", QVariant::fromValue(&bridge)}, {"width", 640}, {"height", 700}}));
        QVERIFY2(view, qPrintable(component.errorString()));
        QVERIFY(view->setProperty("selectedId", "job-1"));
        QCOMPARE(view->property("job").toMap().value("id").toString(), QString("job-1"));
        QVERIFY(view->setProperty("candidateId", "candidate-1"));
        QCOMPARE(view->property("candidate").toMap().value("id").toString(), QString("candidate-1"));
        auto progress = view->findChild<QQuickItem *>("censorAnalysisProgress");
        auto settings = view->findChild<QQuickItem *>("censorSettingsPanel");
        QVERIFY(progress && settings);
        auto scrollViewport = view->findChild<QQuickItem *>("censorScrollViewport");
        auto scrollShadow = view->findChild<QQuickItem *>("censorScrollEdgeShadow");
        auto topShadow = scrollShadow ? scrollShadow->findChild<QQuickItem *>("scrollTopEdgeShadow") : nullptr;
        auto bottomShadow = scrollShadow ? scrollShadow->findChild<QQuickItem *>("scrollBottomEdgeShadow") : nullptr;
        QVERIFY(scrollViewport && scrollShadow && topShadow && bottomShadow);
        QTRY_VERIFY(scrollShadow->isVisible());
        auto viewItem = qobject_cast<QQuickItem *>(view.data());
        QVERIFY(viewItem);
        QCOMPARE(scrollViewport->mapToItem(viewItem, QPointF()), QPointF(10, 10));
        QCOMPARE(scrollViewport->width(), viewItem->width() - 20);
        QCOMPARE(scrollViewport->height(), viewItem->height() - 20);
        QCOMPARE(scrollShadow->mapToItem(viewItem, QPointF()), QPointF(10, 10));
        QCOMPARE(scrollShadow->width(), scrollViewport->width());
        QCOMPARE(scrollShadow->height(), scrollViewport->height());
        QVERIFY(!scrollShadow->isEnabled());
        QCOMPARE(scrollShadow->property("shadowColor").value<QColor>(), QColor("#0c0c0e"));
        QCOMPARE(topShadow->height(), 24.0);
        QCOMPARE(bottomShadow->height(), 24.0);
        QCOMPARE(topShadow->y(), 0.0);
        QCOMPARE(bottomShadow->y() + bottomShadow->height(), scrollShadow->height());
        QTRY_VERIFY(progress->isVisible());
        QVERIFY(progress->mapToItem(qobject_cast<QQuickItem *>(view.data()), QPointF()).y()
                < settings->mapToItem(qobject_cast<QQuickItem *>(view.data()), QPointF()).y());
        unavailable = true;
        bridge.censorRequest();
        QTRY_VERIFY(bridge.censorState().value("error").toString().contains("unavailable"));
        QCOMPARE(bridge.connectionError(), QString());
        QCOMPARE(bridge.state(), normalState);
        unavailable = false;
        censor.insert("configured", false);
        bridge.censorRequest();
        QTRY_VERIFY(!bridge.censorState().value("configured").toBool());
        QVERIFY(!view->property("configured").toBool());
        QVERIFY(bridge.censorState().value("error").toString().isEmpty());
    }
    void repositoryDirectoryValidation() {
        Config config(QString{});
        QTemporaryDir directory;
        QVERIFY(directory.isValid());
        QVERIFY(config.repositoryDirectoryExists(directory.path()));
        QVERIFY(config.repositoryDirectoryExists("  " + directory.path() + "  "));
        QVERIFY(!config.repositoryDirectoryExists(directory.path() + "/missing"));
        QVERIFY(!config.repositoryDirectoryExists(""));
        QVERIFY(!config.repositoryDirectoryExists("relative/path"));
    }
    void nativeRepositoryConfiguration() {
            MockBackend server;
            Config config;
            QTemporaryDir firstRoot, secondRoot;
            QVERIFY(firstRoot.isValid()); QVERIFY(secondRoot.isValid());
            for (const auto &path : {firstRoot.path(), secondRoot.path()}) {
                QProcess git; git.start("git", {"init", "-q", path});
                QVERIFY(git.waitForFinished()); QCOMPARE(git.exitCode(), 0);
            }
            config.setRepositoryPath(firstRoot.path() + "/");
            QStringList actions;
            QString configuredPath;
            bool repositoryReady = false;
            QJsonObject lastCommand;
            server.wireHook = [&](const QByteArray &request, QTcpSocket *) -> std::optional<QByteArray> {
                if (!request.startsWith("GET /api/censor/state ") && !request.startsWith("POST /api/censor/control ")) return std::nullopt;
                if (request.startsWith("POST ")) {
                    const auto command = QJsonDocument::fromJson(request.mid(request.indexOf("\r\n\r\n") + 4)).object();
                    lastCommand = command;
                    actions.append(command.value("action").toString());
                    if (actions.last() == "native_configure") {
                        configuredPath = command.value("repository_path").toString();
                        repositoryReady = true;
                    }
                }
                const auto data = QJsonDocument(QJsonObject{{"native", true}, {"configured", true}, {"native_repository_ready", repositoryReady}, {"repo_path", configuredPath}, {"jobs", QJsonArray{}}}).toJson(QJsonDocument::Compact);
                return "HTTP/1.1 200 OK\r\nConnection: close\r\nContent-Type: application/json\r\nContent-Length: " + QByteArray::number(data.size()) + "\r\n\r\n" + data;
            };
            Bridge bridge(server.url(), "native-test-key", nullptr, &config);
            QTRY_VERIFY(bridge.censorState().value("native").toBool());
            bridge.censorRequest();
            QTRY_VERIFY(!bridge.m_censorPolling);
            QVERIFY(actions.isEmpty());
            QVERIFY(configuredPath.isEmpty());
            GitWorkspace workspace;
            Uploader uploader;
            RepositoryCoordinator repositories(config, workspace, uploader, &bridge);
            bridge.setNativePublicationValidator([] { return QString("Terminal is running"); });
            QCOMPARE(workspace.repositoryPath(), config.repositoryPath());
            QVERIFY(uploader.directory().isEmpty());
            // Saved paths are validated without opening the Git workspace.
            QTRY_VERIFY(!workspace.resolving());
            QCOMPARE(workspace.rootPath(), QFileInfo(firstRoot.path()).canonicalFilePath());
            bridge.configureNativeRepository();
            QVERIFY(actions.isEmpty());
            bridge.setNativePublicationValidator([] { return QString(); });
            QTRY_COMPARE(configuredPath, QFileInfo(firstRoot.path()).canonicalFilePath());
            QTRY_VERIFY(!bridge.censorBusy());
            QCOMPARE(actions, QStringList{"native_configure"});
            config.setRepositoryPath(secondRoot.path() + "/");
            QTRY_COMPARE(configuredPath, QFileInfo(secondRoot.path()).canonicalFilePath());
            QTRY_VERIFY(!bridge.censorBusy());
            bridge.censorCommand("approve", {{"id", "review"}, {"candidate_id", "candidate"}});
            QTRY_VERIFY(actions.contains("approve"));
            QCOMPARE(actions, QStringList({"native_configure", "native_configure", "approve"}));
                        QTRY_VERIFY(!bridge.censorBusy());
                        const int beforeRecovery = actions.size();
                        repositoryReady = false;
                        QTRY_VERIFY(actions.size() > beforeRecovery);
                        QTRY_VERIFY(!bridge.m_nativeConfiguredPath.isEmpty());
                        QTRY_VERIFY(bridge.censorState().value("native_repository_ready").toBool());
                        QTRY_VERIFY(!bridge.censorBusy());
                        QCOMPARE(actions.last(), QString("native_configure"));
                        bridge.m_nativeConfiguredPath.clear();
                        const int beforeFallback = actions.size();
                        bridge.censorCommand("approve", {{"id", "review"}, {"candidate_id", "candidate"}});
                        QTRY_COMPARE(actions.size(), beforeFallback + 2);
                        QCOMPARE(actions.at(beforeFallback), QString("native_configure"));
                        QCOMPARE(actions.last(), QString("approve"));
                        QCOMPARE(configuredPath, QFileInfo(secondRoot.path()).canonicalFilePath());
                        QTRY_VERIFY(!bridge.censorBusy());
                        bridge.censorCommand("check_track", {{"track_id", "9007199254740991"}});
                        QTRY_COMPARE(actions.last(), QString("check_track"));
                        QTRY_VERIFY(!bridge.censorBusy());
                        QVERIFY(lastCommand.value("track_id").isDouble());
                        QCOMPARE(lastCommand.value("track_id").toInteger(), qint64(9007199254740991LL));
                        const int validCount = actions.size();
                        QSignalSpy finished(&bridge, &Bridge::censorCommandFinished);
                        for (const auto &id : QStringList{"0", "-1", "1.5", "9007199254740992", "9223372036854775807", "invalid"})
                            bridge.censorCommand("check_track", {{"track_id", id}});
                        QCOMPARE(finished.count(), 6);
                        QCOMPARE(actions.size(), validCount);
            QQmlEngine engine;
            QQmlComponent playerComponent(&engine);
            playerComponent.setData("import QtQml; QtObject { property string trackId: \"42\" }", QUrl());
            QScopedPointer<QObject> player(playerComponent.create());
            QVERIFY(player);
            QQmlComponent component(&engine, QUrl("qrc:/CensorDetector.qml"));
            QScopedPointer<QObject> view(component.createWithInitialProperties({{"bridge", QVariant::fromValue(&bridge)}, {"normalPlayer", QVariant::fromValue(player.data())}}));
            QVERIFY2(view, qPrintable(component.errorString()));
            bridge.m_censorState.insert("repository_error", "Repository root is unavailable");
            bridge.m_censorState.insert("error", "Analysis failed");
            emit bridge.censorStateChanged();
            QVERIFY(!view->findChild<QObject *>("censorRepositoryError"));
            QVERIFY(!view->findChild<QObject *>("censorOperationError"));
            bridge.m_censorState.remove("repository_error");
            emit bridge.censorStateChanged();
            auto reason = [&] {
                QVariant result;
                QMetaObject::invokeMethod(view.data(), "disabledReason", Q_RETURN_ARG(QVariant, result), Q_ARG(QVariant, QString("check_track")));
                return result.toString();
            };
            QVERIFY(view->property("jobs").toList().isEmpty());
            QVERIFY(reason().isEmpty());
            QVERIFY(QMetaObject::invokeMethod(view.data(), "command", Q_ARG(QVariant, QString("check_track")), Q_ARG(QVariant, QVariantMap{})));
            QTRY_COMPARE(lastCommand.value("track_id").toInteger(), qint64(42));
            QTRY_VERIFY(!bridge.censorBusy());
            player->setProperty("trackId", "9007199254740993");
            QVERIFY(reason().contains("safe integer"));
            player->setProperty("trackId", "");
            QVERIFY(!reason().isEmpty());
            bridge.m_censorState.insert("native", false);
            emit bridge.censorStateChanged();
            QVERIFY(!reason().isEmpty());
            bridge.m_nativeConfiguredPath.clear();
            bridge.m_nativeAttemptedPath.clear();
            const int remoteCount = actions.size();
            bridge.configureNativeRepository();
            QCOMPARE(actions.size(), remoteCount);
        }
        void nativePublicationSafety() {
            MockBackend server;
            Bridge bridge(server.url(), "native-test-key");
            bridge.m_nativeRecoveryPending = false;
            bridge.m_censorState = {{"native", false}, {"busy", true}, {"running", true}};
            QVERIFY(!bridge.nativePublicationBusy());
            bridge.m_censorState.insert("native", true);
            QVERIFY(bridge.nativePublicationBusy());
            bridge.m_censorState.insert("busy", false);
            bridge.m_censorState.insert("running", false);
            bridge.m_censorState.insert("jobs", QJsonArray{QJsonObject{{"status", "local_saved"}}});
            QVERIFY(!bridge.nativePublicationBusy());
            bridge.m_censorState.insert("jobs", QJsonArray{QJsonObject{{"status", "publishing"}}});
            QVERIFY(bridge.nativePublicationBusy());
        }
        void censorActualDtoAndCandidateSelection() {
        MockBackend server;
        const QJsonObject strongComparison{{"comparable", true}, {"offset_seconds", 7}, {"correlation", .9},
            {"confidence", .95}, {"confidence_class", "strong_match"}, {"overlap_seconds", 12},
            {"spectral_similarity", .98}, {"spectral_match_fraction", .95}};
        const QJsonObject interval{{"start_seconds", 3}, {"end_seconds", 4}, {"reason", "restored vocal"},
            {"status", "reference_restoration_candidate"}, {"restoration", QJsonObject{
                {"confidence", .8}, {"reference_start_seconds", 1}, {"reference_end_seconds", 2}}}};
        const QJsonObject best{{"duration_seconds", 12}, {"comparison", strongComparison},
            {"waveform", QJsonObject{{"samples", QJsonArray{.25, .5}}}},
            {"suspicious_intervals", QJsonArray{interval}}, {"intervals_truncated", false},
            {"warnings", QJsonArray{"Candidate analysis completed"}}};
        auto selectedComparison = strongComparison;
        selectedComparison.insert("offset_seconds", -2);
        selectedComparison.insert("confidence", .3);
        QJsonObject selectedResult{{"duration_seconds", 12}, {"comparison", selectedComparison},
            {"suspicious_intervals", QJsonArray{}}, {"intervals_truncated", false}};
        QJsonObject review{{"id", "review"}, {"status", "review_required"}, {"stage", "review"},
            {"metadata_warning", "MusicBrainz metadata unavailable; audio search continues. <literal>"},
            {"request", QJsonObject{{"id", "request-review"}, {"track_id", 42}, {"title", "Review"}}},
            {"result", best}, {"candidates", QJsonArray{
                QJsonObject{{"id", "best"}, {"title", "Best"}, {"result", best}, {"full_audio", true}, {"downloadable", true}, {"url", "https://127.0.0.1:1/best.mp3"}},
                QJsonObject{{"id", "selected"}, {"title", "Selected"}, {"result", selectedResult}, {"full_audio", true}, {"downloadable", true}, {"url", "https://127.0.0.1:1/selected.mp3"}},
                QJsonObject{{"id", "unanalyzed"}, {"title", "Unanalyzed"}, {"error", "Comparison failed"}, {"full_audio", true}, {"downloadable", true}, {"url", "https://127.0.0.1:1/unanalyzed.mp3"}}}}};
        QJsonObject failed1{{"id", "failed-1"}, {"status", "failed"}, {"error", "Audio unavailable"},
            {"request", QJsonObject{{"id", "request-1"}, {"title", "Failed one"}}}};
        auto failed2 = failed1; failed2.insert("id", "failed-2");
        auto uncertain = review;
        uncertain.insert("id", "uncertain"); uncertain.insert("error", "Publication not acknowledged");
        uncertain.insert("publication_key", "saved-key"); uncertain.insert("selected_candidate", "best");
        const QJsonObject stats{{"total", 91}, {"pending", 10}, {"running", 2}, {"censorship_found", 9}, {"suitable_mp3", 6}, {"review_required", 8},
            {"succeeded", 20}, {"failed", 5}, {"rejected", 4}, {"deferred", 3}, {"stopped", 7}, {"rolled_back", 1}};
        QJsonObject censor{{"configured", true}, {"running", false}, {"busy", false}, {"paused", false},
            {"metadata_configured", false}, {"metadata_warning", "MusicBrainz metadata disabled: set FCKCENSOR_MUSICBRAINZ_USER_AGENT. <literal>"},
            {"rollback_supported", false}, {"settings", QJsonObject{{"workers", 3}, {"auto_replace", false}}},
            {"stats", stats}, {"jobs", QJsonArray{review, failed1, failed2, uncertain}}};
        QList<QJsonObject> commands;
        server.wireHook = [&](const QByteArray &request, QTcpSocket *) -> std::optional<QByteArray> {
            if (!request.startsWith("GET /api/censor/state ") && !request.startsWith("POST /api/censor/control ")) return std::nullopt;
            if (request.startsWith("POST ")) {
                const auto command = QJsonDocument::fromJson(request.mid(request.indexOf("\r\n\r\n") + 4)).object();
                commands.append(command);
                if (command.value("action") == "retry") {
                    auto jobs = censor.value("jobs").toArray();
                    for (int n = 0; n < jobs.size(); ++n) {
                        auto j = jobs[n].toObject();
                        if (j.value("id") == command.value("id")) { j.insert("status", "pending"); j.remove("error"); jobs[n] = j; }
                    }
                    censor.insert("jobs", jobs);
                }
            }
            const auto data = QJsonDocument(censor).toJson(QJsonDocument::Compact);
            return "HTTP/1.1 200 OK\r\nConnection: close\r\nContent-Type: application/json\r\nContent-Length: "
                + QByteArray::number(data.size()) + "\r\n\r\n" + data;
        };
        Bridge bridge(server.url(), "censor-test-key");
        QTRY_VERIFY(bridge.censorState().value("configured").toBool());
        QQmlEngine engine;
        QSignalSpy warnings(&engine, &QQmlEngine::warnings);
        QQmlComponent component(&engine, QUrl("qrc:/CensorDetector.qml"));
        QScopedPointer<QObject> view(component.createWithInitialProperties({{"bridge", QVariant::fromValue(&bridge)}, {"width", 640}, {"height", 700}}));
        QVERIFY2(view, qPrintable(component.errorString()));
        auto reason = [&](const QString &action) {
            QVariant value;
            if (!QMetaObject::invokeMethod(view.data(), "disabledReason", Q_RETURN_ARG(QVariant, value), Q_ARG(QVariant, action))) return QString("invoke failed");
            return value.toString();
        };
        auto publishState = [&] { bridge.m_censorState = censor; emit bridge.censorStateChanged(); };
        const QStringList removedNames{"censorMetadataStatus", "censorMetadataWarning", "censorJobMetadataWarning",
            "censorAnalysisWarnings", "censorExcerpt-0", "censorExcerpt-1", "censorCandidateDetails-page", "censorCandidateMetadata-page"};
        for (const auto &name : removedNames)
            QVERIFY2(!visualChild(qobject_cast<QQuickItem *>(view.data()), name), qPrintable(name));
        for (auto it = stats.begin(); it != stats.end(); ++it)
            QVERIFY(!visualChild(qobject_cast<QQuickItem *>(view.data()), "censorStat-" + it.key()));
        auto playbackButton = view->findChild<QQuickItem *>("censorPlaybackButton");
        QVERIFY(playbackButton);
        QCOMPARE(playbackButton->property("glyph").toString(), QString("play"));
        QVERIFY(view->setProperty("selectedId", "review"));
        QVERIFY(!visualChild(qobject_cast<QQuickItem *>(view.data()), "censorJobMetadataWarning"));
        QVERIFY(reason("analyze").isEmpty()); // Metadata warnings must not disable audio work.
        QCOMPARE(view->property("offsetMs").toDouble(), 0.0); // No candidate selected: never borrow best alignment.
        auto switchCandidate = [&](int step) {
            return QMetaObject::invokeMethod(view.data(), "switchCandidate", Q_ARG(QVariant, step));
        };
        QVERIFY(switchCandidate(1));
        QCOMPARE(view->property("candidateId").toString(), QString("best"));
        QCOMPARE(view->property("candidateIndex").toInt(), 0);
        QCOMPARE(view->property("comparison").toMap(), strongComparison.toVariantMap());
        QCOMPARE(view->property("candidateAudioUrl").toUrl(), QUrl("https://127.0.0.1:1/best.mp3"));
        QVERIFY(switchCandidate(-1)); // First previous selects the original.
        QVERIFY(view->property("candidateId").toString().isEmpty());
        QCOMPARE(view->property("candidateIndex").toInt(), -1);
        QVERIFY(view->property("comparison").toMap().isEmpty());
        QVERIFY(switchCandidate(-1)); // Original previous selects the last candidate.
        QCOMPARE(view->property("candidateId").toString(), QString("unanalyzed"));
        QCOMPARE(view->property("candidateIndex").toInt(), 2);
        QVERIFY(view->property("comparison").toMap().isEmpty());
        QCOMPARE(view->property("offsetMs").toDouble(), 0.0);
        QCOMPARE(reason("approve"), QString("Comparison failed"));
        QVERIFY(switchCandidate(1)); // Last next selects the original.
        QVERIFY(view->property("candidateId").toString().isEmpty());
        QCOMPARE(view->property("candidateIndex").toInt(), -1);
        QVERIFY(switchCandidate(1)); // Original next selects the first.
        QCOMPARE(view->property("candidateId").toString(), QString("best"));
        QVERIFY(switchCandidate(1));
        QCOMPARE(view->property("candidateId").toString(), QString("selected"));
        QCOMPARE(view->property("candidateIndex").toInt(), 1);
        QCOMPARE(view->property("comparison").toMap(), selectedComparison.toVariantMap());
        QCOMPARE(view->property("candidateAudioUrl").toUrl(), QUrl("https://127.0.0.1:1/selected.mp3"));
        QCOMPARE(view->property("offsetMs").toDouble(), -2000.0);
        QCOMPARE(view->property("result").toMap().value("comparison").toMap().value("confidence").toDouble(), .3);
        QVERIFY(!reason("approve").isEmpty()); // Strong best candidate cannot authorize the weaker selected one.
        QVariant position;
        QVERIFY(QMetaObject::invokeMethod(view.data(), "candidatePosition", Q_RETURN_ARG(QVariant, position), Q_ARG(QVariant, 4000)));
        QCOMPARE(position.toDouble(), 6000.0);
        QCOMPARE(view->property("waveformData").toMap().value("samples").toList().size(), 2); // Real source waveform fallback.
        const QJsonArray nativeWaveform{QJsonObject{{"start_seconds", 0}, {"end_seconds", 12}, {"min", -.1}, {"max", .6}, {"rms", .2}}};
        selectedResult.insert("waveform", nativeWaveform);
        selectedComparison.insert("confidence", .95);
        selectedResult.insert("comparison", selectedComparison);
        auto candidates = review.value("candidates").toArray();
        auto selected = candidates[1].toObject(); selected.insert("result", selectedResult); candidates[1] = selected;
        review.insert("candidates", candidates);
        auto jobs = censor.value("jobs").toArray(); jobs[0] = review; censor.insert("jobs", jobs); publishState();
        QCOMPARE(view->property("offsetMs").toDouble(), -2000.0);
        QCOMPARE(view->property("waveformData").toList().first().toMap().value("max").toDouble(), .6);
        QCOMPARE(view->property("waveformDuration").toDouble(), 12.0);
        QVERIFY(reason("approve").isEmpty());
        auto canvas = visualChild(qobject_cast<QQuickItem *>(view.data()), "censorWaveform");
        QVERIFY(canvas);
        QCOMPARE(canvas->property("samples").toList().first().toMap().value("min").toDouble(), -.1);
        QVERIFY(!canvas->property("intervals").isValid()); // Legacy detection data must not create overlays.
        QVERIFY(view->setProperty("candidateId", "unanalyzed"));
        QCOMPARE(view->property("offsetMs").toDouble(), 0.0);
        QVERIFY(view->property("result").toMap().isEmpty());
        QCOMPARE(reason("approve"), QString("Comparison failed"));
        QVERIFY(switchCandidate(-1));
        QCOMPARE(view->property("candidateId").toString(), QString("selected"));
        QVERIFY(switchCandidate(-1));
        QCOMPARE(view->property("candidateId").toString(), QString("best"));
        QVERIFY(reason("approve").isEmpty());
        QCOMPARE(view->property("offsetMs").toDouble(), 7000.0);
        for (const auto &name : removedNames)
            QVERIFY2(!visualChild(qobject_cast<QQuickItem *>(view.data()), name), qPrintable(name));
        censor.insert("metadata_configured", true); censor.remove("metadata_warning"); publishState();
        QVERIFY(!visualChild(qobject_cast<QQuickItem *>(view.data()), "censorMetadataStatus"));
        QVERIFY(reason("approve").isEmpty());
        QVERIFY(reason("rollback").contains("does not support"));
        QVERIFY(!view->findChild<QObject *>("censorAutoReplace"));
        censor.insert("running", true); publishState();
        QVERIFY(reason("settings").isEmpty());
        QVERIFY(view->findChild<QObject *>("censorWorkers")->property("enabled").toBool());
        censor.insert("running", false); censor.insert("busy", true); publishState();
        QVERIFY(!reason("analyze").isEmpty()); QVERIFY(reason("pause").isEmpty()); QVERIFY(reason("stop").isEmpty());
        censor.insert("busy", false); publishState();
        QVERIFY(view->setProperty("selectedId", "uncertain"));
        QVERIFY(view->setProperty("candidateId", "selected"));
        QVERIFY(reason("approve").contains("same previously selected"));
        QVERIFY(!reason("retry").isEmpty());
        QVERIFY(view->setProperty("candidateId", "best"));
        QVERIFY(reason("approve").isEmpty());
        QVERIFY(view->setProperty("selectedId", ""));
        QVERIFY(QMetaObject::invokeMethod(view.data(), "retryErrors"));
        QTRY_COMPARE(commands.size(), 2);
        QTRY_VERIFY(!bridge.censorBusy());
        QCOMPARE(commands[0].value("action").toString(), QString("retry"));
        QCOMPARE(commands[0].value("id").toString(), QString("failed-1"));
        QCOMPARE(commands[1].value("id").toString(), QString("failed-2"));
        QVERIFY(!commands[0].contains("candidate_id"));
        const QJsonArray reviewOnly{
            QJsonObject{{"id", "page"}, {"title", "Provider page"}, {"url", "https://provider.example/track/1"}, {"provider", "Page provider"}, {"duration_seconds", 42}, {"full_audio", false}, {"downloadable", false}, {"result", best}, {"metadata", QJsonObject{{"kind", "page"}, {"content_type", "text/html"}, {"note", "<b>literal</b>"}}}},
            QJsonObject{{"id", "preview"}, {"url", "https://provider.example/preview.mp3"}, {"full_audio", false}, {"downloadable", true}, {"result", best}, {"metadata", QJsonObject{{"kind", "preview"}}}},
            QJsonObject{{"id", "stream"}, {"url", "https://provider.example/stream"}, {"full_audio", true}, {"downloadable", false}, {"result", best}},
            QJsonObject{{"id", "mislabelled"}, {"url", "https://provider.example/page"}, {"full_audio", true}, {"downloadable", true}, {"result", best}, {"metadata", QJsonObject{{"kind", " page "}}}},
            QJsonObject{{"id", "html"}, {"url", "https://provider.example/html"}, {"full_audio", true}, {"downloadable", true}, {"result", best}, {"metadata", QJsonObject{{"content_type", " Text/HTML "}}}}};
        candidates = review.value("candidates").toArray();
        for (const auto &c : reviewOnly) candidates.append(c);
        review.insert("candidates", candidates);
        jobs = censor.value("jobs").toArray(); jobs[0] = review; censor.insert("jobs", jobs); publishState();
        QVERIFY(view->setProperty("selectedId", "review"));
        auto referencePlayer = view->findChild<QObject *>("censorCandidatePlayer");
        QVERIFY(referencePlayer);
        for (const auto &c : reviewOnly) {
            view->setProperty("candidateId", c.toObject().value("id").toString());
            QVERIFY(view->property("candidateAudioUrl").toUrl().isEmpty());
            QVERIFY(referencePlayer->property("source").toUrl().isEmpty());
            QVERIFY(reason("approve").contains("review-only"));
        }
        QVERIFY(view->setProperty("candidateId", "best"));
        QVERIFY(switchCandidate(-1));
        QVERIFY(view->property("candidateId").toString().isEmpty());
        QCOMPARE(view->property("candidateIndex").toInt(), -1);
        QVERIFY(switchCandidate(-1));
        QCOMPARE(view->property("candidateId").toString(), QString("html"));
        QCOMPARE(view->property("comparison").toMap(), strongComparison.toVariantMap());
        QVERIFY(view->property("candidateAudioUrl").toUrl().isEmpty());
        QVERIFY(referencePlayer->property("source").toUrl().isEmpty());
        QVERIFY(!view->property("listenCandidate").toBool());
        QVERIFY(reason("approve").contains("review-only"));
        QVERIFY(switchCandidate(1));
        QVERIFY(view->property("candidateId").toString().isEmpty());
        QVERIFY(switchCandidate(1));
        QCOMPARE(view->property("candidateId").toString(), QString("best"));
        QCOMPARE(view->property("candidateAudioUrl").toUrl(), QUrl("https://127.0.0.1:1/best.mp3"));
        QCOMPARE(referencePlayer->property("source").toUrl(), view->property("candidateAudioUrl").toUrl());
        QVERIFY(view->property("listenCandidate").toBool());
        QVERIFY(reason("approve").isEmpty());
        QVERIFY(view->setProperty("candidateId", "page"));
        QVariant details, metadata;
        const QVariant page = reviewOnly.first().toObject().toVariantMap();
        QVERIFY(QMetaObject::invokeMethod(view.data(), "candidateDetails", Q_RETURN_ARG(QVariant, details), Q_ARG(QVariant, page)));
        QVERIFY(QMetaObject::invokeMethod(view.data(), "candidateMetadata", Q_RETURN_ARG(QVariant, metadata), Q_ARG(QVariant, page)));
        QVERIFY(details.toString().contains("Web page"));
        QVERIFY(details.toString().contains("Page provider"));
        QVERIFY(details.toString().contains("42.00 s"));
        QVERIFY(metadata.toString().contains("note: <b>literal</b>"));
        view->setProperty("candidateId", "best");
        auto settings = censor.value("settings").toObject(); settings.insert("min_confidence", .99); censor.insert("settings", settings); publishState();
        QVERIFY(reason("approve").contains("configured minimum"));
        settings.insert("min_confidence", .1); censor.insert("settings", settings); publishState();
        QVERIFY(reason("approve").isEmpty());
        QQmlComponent playerComponent(&engine);
        playerComponent.setData("import QtQml; QtObject { property bool playing: true; property int toggles: 0; function toggle() { playing = false; toggles++; } }", QUrl());
        QScopedPointer<QObject> normalPlayer(playerComponent.create());
        QVERIFY(normalPlayer);
        view->setProperty("normalPlayer", QVariant::fromValue(normalPlayer.data()));
        view->setProperty("playing", true);
        QCOMPARE(normalPlayer->property("playing").toBool(), true);
        QCOMPARE(normalPlayer->property("toggles").toInt(), 0); // Intent alone must not pause the main player.
        view->setProperty("playing", false);
        auto waveformSeek = visualChild(qobject_cast<QQuickItem *>(view.data()), "censorWaveformSeek");
        QVERIFY(waveformSeek);
        QCOMPARE(view->property("waveformDuration").toDouble(), 12.0);
        QVERIFY(!waveformSeek->property("enabled").toBool()); // Empty original URL cannot enable seeking.
        for (double fraction : {-1.0, 0.5, 2.0}) {
            QVariant target;
            QVERIFY(QMetaObject::invokeMethod(view.data(), "waveformTarget", Q_RETURN_ARG(QVariant, target), Q_ARG(QVariant, fraction)));
            QCOMPARE(target.toDouble(), fraction < 0 ? 0.0 : fraction > 1 ? 12000.0 : 6000.0);
        }
        for (double fraction : {qQNaN(), qInf(), -qInf()}) {
            QVariant target;
            QVERIFY(QMetaObject::invokeMethod(view.data(), "waveformTarget", Q_RETURN_ARG(QVariant, target), Q_ARG(QVariant, fraction)));
            QCOMPARE(target.toDouble(), 0.0);
        }

        QVERIFY(view->setProperty("selectedId", "failed-1"));
        QCOMPARE(view->property("waveformDuration").toDouble(), 0.0);
        QVERIFY(!waveformSeek->property("enabled").toBool());
        for (double fraction : {-1.0, 0.5, 2.0}) {
            QVariant target;
            QVERIFY(QMetaObject::invokeMethod(view.data(), "waveformTarget", Q_RETURN_ARG(QVariant, target), Q_ARG(QVariant, fraction)));
            QCOMPARE(target.toDouble(), 0.0);
        }
        for (double fraction : {-0.5, 0.5, 1.5}) {
            QVERIFY(QMetaObject::invokeMethod(view.data(), "waveformSeek", Q_ARG(QVariant, fraction)));
            QCOMPARE(referencePlayer->property("position").toLongLong(), 0LL);
            QVERIFY(!view->property("playing").toBool());
        }
        for (int step : {1, -1}) {
            QVERIFY(switchCandidate(step));
            QCOMPARE(view->property("selectedId").toString(), QString("failed-1"));
            QVERIFY(view->property("candidateId").toString().isEmpty());
            QCOMPARE(view->property("candidateIndex").toInt(), -1);
            QVERIFY(view->property("comparison").toMap().isEmpty());
            QVERIFY(view->property("candidateAudioUrl").toUrl().isEmpty());
            QVERIFY(referencePlayer->property("source").toUrl().isEmpty());
            QVERIFY(!view->property("listenCandidate").toBool());
            QVERIFY(!reason("approve").isEmpty());
        }
        QVERIFY(view->setProperty("selectedId", "review"));
        QVERIFY(switchCandidate(-1)); // No selection: previous selects the last candidate.
        QCOMPARE(view->property("candidateId").toString(), QString("html"));
        QCOMPARE(view->property("selectedId").toString(), QString("review"));
        QVERIFY(view->property("candidateAudioUrl").toUrl().isEmpty());
        QVERIFY(reason("approve").contains("review-only"));
        QCOMPARE(warnings.count(), 0);
    }
    void censorOriginalPlaybackSurvivesDetailRefresh() {
        QTemporaryDir directory;
        QVERIFY(directory.isValid());
        QFile wav(directory.filePath("original.wav"));
        QVERIFY(wav.open(QIODevice::WriteOnly));
        QDataStream pcm(&wav);
        pcm.setByteOrder(QDataStream::LittleEndian);
        const quint32 sampleRate = 16000;
        const quint32 dataSize = sampleRate * 30 * 2;
        pcm.writeRawData("RIFF", 4); pcm << quint32(36 + dataSize);
        pcm.writeRawData("WAVEfmt ", 8); pcm << quint32(16) << quint16(1) << quint16(1)
            << sampleRate << quint32(sampleRate * 2) << quint16(2) << quint16(16);
        pcm.writeRawData("data", 4); pcm << dataSize;
        for (quint32 i = 0; i < dataSize / 2; ++i)
            pcm << qint16((i % 80 < 40) ? 1000 : -1000);
        QCOMPARE(pcm.status(), QDataStream::Ok);
        wav.close();
        const QUrl source = QUrl::fromLocalFile(wav.fileName());
        QQmlEngine engine;
        QQmlComponent playerComponent(&engine);
        playerComponent.setData(R"(
            import QtQml
            QtObject {
                property string trackId: "42"
                property string title: "Synthetic original"
                property string artist: "Fixture"
                property url streamUrl
                property var spectrumLevels: []
                property var spectrumPeaks: []
                property bool playing: true
                property int pauses: 0
                signal stateChanged()
                function pause() { playing = false; pauses++; }
            }
        )", QUrl());
        QScopedPointer<QObject> mainPlayer(playerComponent.createWithInitialProperties({{"streamUrl", source}}));
        QVERIFY2(mainPlayer, qPrintable(playerComponent.errorString()));
        QQmlComponent component(&engine, QUrl("qrc:/CensorDetector.qml"));
        const QVariantMap job{{"id", "detail"}, {"status", "review_required"},
            {"request", QVariantMap{{"track_id", 42}, {"title", "Fixture"}, {"original_url", source}}},
            {"candidates", QVariantList{}}};
        QScopedPointer<QObject> view(component.createWithInitialProperties({
            {"normalPlayer", QVariant::fromValue(mainPlayer.data())},
            {"censorState", QVariantMap{{"configured", true}, {"jobs", QVariantList{job}}}},
            {"width", 640}, {"height", 700}}));
        QVERIFY2(view, qPrintable(component.errorString()));
        QCOMPARE(view->property("selectedId").toString(), QString("detail"));
        QVERIFY(view->property("candidateId").toString().isEmpty());
        auto original = view->findChild<QMediaPlayer *>("censorOriginalPlayer");
        QVERIFY(original);
        QTRY_VERIFY(original->duration() > 0);
        QCOMPARE(original->source(), source);
        QVERIFY(view->setProperty("playing", true));
        QCOMPARE(mainPlayer->property("pauses").toInt(), 0);
        QVERIFY(view->setProperty("playing", false));
        QVERIFY(QMetaObject::invokeMethod(view.data(), "togglePlayback"));
        QTRY_COMPARE(original->playbackState(), QMediaPlayer::PlayingState);
        QTRY_COMPARE(mainPlayer->property("pauses").toInt(), 1);
        QVERIFY(!mainPlayer->property("playing").toBool());
        const auto initialPosition = original->position();
        // A missing detail is not a new track, and an empty stream URL must not clear the source.
        QVERIFY(mainPlayer->setProperty("streamUrl", QUrl()));
        QVERIFY(view->setProperty("job", QVariant()));
        QVERIFY(QMetaObject::invokeMethod(mainPlayer.data(), "stateChanged"));
        QCOMPARE(view->property("selectedId").toString(), QString("detail"));
        QCOMPARE(original->source(), source);
        QCOMPARE(original->playbackState(), QMediaPlayer::PlayingState);
        QVERIFY(view->property("playing").toBool());
        QVERIFY(view->property("candidate").toMap().isEmpty());
        QTRY_VERIFY(original->position() > initialPosition);
        QVERIFY(view->setProperty("job", job));
        QCOMPARE(original->source(), source);
        QCOMPARE(original->playbackState(), QMediaPlayer::PlayingState);
        QCOMPARE(mainPlayer->property("pauses").toInt(), 1);
        QVERIFY(mainPlayer->setProperty("trackId", "43"));
        QCOMPARE(original->playbackState(), QMediaPlayer::StoppedState);
        QVERIFY(!view->property("playing").toBool());
        QVERIFY(view->property("selectedId").toString().isEmpty());
        QVERIFY(original->source().isEmpty());
    }
    void censorLastCandidateFreshPlayback_data() {
        QTest::addColumn<bool>("refreshDetail");
        QTest::addColumn<bool>("webAudio");
        QTest::newRow("fresh-resolve") << false << false;
        QTest::newRow("transient-detail-and-source-refresh") << true << false;
        QTest::newRow("unverified-web-audio") << false << true;
    }
    void censorLastCandidateFreshPlayback() {
        QFETCH(bool, refreshDetail);
        QFETCH(bool, webAudio);
        const QString lastId = webAudio ? "candidate:web:last" : "last";
        QQmlEngine engine;
        QQmlComponent bridgeComponent(&engine);
        bridgeComponent.setData(R"(
            import QtQml
            QtObject {
                property var censorState: ({ configured: true, native: true, native_repository_ready: true, jobs_total: 1,
                    jobs: [{ id: "detail", request: { track_id: 42 } }] })
                property var censorSelectedJob: ({})
                property bool censorBusy: false
                property bool busy: false
                property var requests: []
                signal censorPlaybackResolved(int requestId, string jobId, string candidateId, url url, string error)
                function censorSelectJob(id) {}
                function censorResolvePlayback(jobId, candidateId, requestId) {
                    requests = requests.concat([{ jobId: jobId, candidateId: candidateId, requestId: requestId }]);
                }
            }
        )", QUrl());
        QScopedPointer<QObject> bridge(bridgeComponent.create());
        QVERIFY2(bridge, qPrintable(bridgeComponent.errorString()));
        const auto candidate = [](const QString &id) {
            return QVariantMap{{"id", id}, {"full_audio", true}, {"downloadable", true},
                {"url", "https://stale.invalid/" + id + ".mp3"}};
        };
        auto lastCandidate = candidate(lastId);
        if (webAudio) {
            lastCandidate["full_audio"] = false;
            lastCandidate["provider"] = "direct_web";
            lastCandidate["metadata"] = QVariantMap{{"kind", "audio"}, {"content_type", "audio/mpeg"}};
        }
        QVariantMap detail{{"id", "detail"}, {"status", "review_required"},
            {"request", QVariantMap{{"track_id", 42}, {"original_url", "file:///original-before.wav"}}},
            {"candidates", QVariantList{candidate("first"), lastCandidate}}};
        QVERIFY(bridge->setProperty("censorSelectedJob", detail));
        QQmlComponent component(&engine, QUrl("qrc:/CensorDetector.qml"));
        QScopedPointer<QObject> view(component.createWithInitialProperties({
            {"bridge", QVariant::fromValue(bridge.data())}, {"uploadBackend", QVariant::fromValue(bridge.data())},
            {"selectedId", "detail"}, {"width", 640}, {"height", 700}}));
        QVERIFY2(view, qPrintable(component.errorString()));
        auto original = view->findChild<QMediaPlayer *>("censorOriginalPlayer");
        auto replacement = view->findChild<QMediaPlayer *>("censorCandidatePlayer");
        QVERIFY(original);
        QVERIFY(replacement);
        // Backwards from the original selects the final candidate, including the wrap boundary.
        QVERIFY(QMetaObject::invokeMethod(view.data(), "switchCandidate", Q_ARG(QVariant, -1)));
        QCOMPARE(view->property("candidateId").toString(), lastId);
        if (refreshDetail) {
            QVERIFY(bridge->setProperty("censorSelectedJob", QVariantMap{}));
            detail["request"] = QVariantMap{{"track_id", 42}, {"original_url", "file:///original-after.wav"}};
            QVERIFY(bridge->setProperty("censorSelectedJob", detail));
            QCOMPARE(original->source(), QUrl("file:///original-after.wav"));
        }
        QCOMPARE(view->property("candidateId").toString(), lastId);
        QVERIFY(view->property("playableSelection").toBool());
        QVERIFY(view->property("listenCandidate").toBool());
        if (webAudio) {
            QVariant reason;
            QVERIFY(QMetaObject::invokeMethod(view.data(), "disabledReason", Q_RETURN_ARG(QVariant, reason), Q_ARG(QVariant, "send_candidate")));
            QVERIFY(reason.toString().isEmpty());
            auto approval = view->findChild<QObject *>("censorReviewApproval");
            QVERIFY(approval);
            QVERIFY(approval->property("enabled").toBool());
            QVariant eligible;
            QVERIFY(QMetaObject::invokeMethod(view.data(), "audioCandidate", Q_RETURN_ARG(QVariant, eligible), Q_ARG(QVariant, lastCandidate)));
            QVERIFY(!eligible.toBool());
            for (const QString &kind : {"page", "preview"}) {
                auto blocked = lastCandidate;
                blocked["metadata"] = QVariantMap{{"kind", kind}, {"content_type", "audio/mpeg"}};
                QVERIFY(QMetaObject::invokeMethod(view.data(), "playableCandidate", Q_RETURN_ARG(QVariant, eligible), Q_ARG(QVariant, blocked)));
                QVERIFY(!eligible.toBool());
            }
            auto blocked = lastCandidate;
            blocked["downloadable"] = false;
            QVERIFY(QMetaObject::invokeMethod(view.data(), "playableCandidate", Q_RETURN_ARG(QVariant, eligible), Q_ARG(QVariant, blocked)));
            QVERIFY(!eligible.toBool());
            blocked = lastCandidate;
            blocked["metadata"] = QVariantMap{{"kind", "audio"}, {"content_type", "text/html"}};
            QVERIFY(QMetaObject::invokeMethod(view.data(), "playableCandidate", Q_RETURN_ARG(QVariant, eligible), Q_ARG(QVariant, blocked)));
            QVERIFY(!eligible.toBool());
        }
        QVERIFY(QMetaObject::invokeMethod(view.data(), "togglePlayback"));
        auto requests = bridge->property("requests").toList();
        QCOMPARE(requests.size(), 1);
        QCOMPARE(requests.last().toMap().value("candidateId").toString(), lastId);
        QCOMPARE(requests.last().toMap().value("jobId").toString(), QString("detail"));
        QVERIFY(original->playbackState() != QMediaPlayer::PlayingState);
        // Native mode must not load the stale candidate URL while awaiting resolution.
        QVERIFY(replacement->source().isEmpty());
        const int firstRequest = requests.last().toMap().value("requestId").toInt();
        QVERIFY(QMetaObject::invokeMethod(view.data(), "togglePlayback"));
        QVERIFY(QMetaObject::invokeMethod(view.data(), "togglePlayback"));
        requests = bridge->property("requests").toList();
        QCOMPARE(requests.size(), 2);
        const int currentRequest = requests.last().toMap().value("requestId").toInt();
        QVERIFY(currentRequest > firstRequest);
        const auto resolve = [&](int request, const QString &job, const QString &id, const QUrl &url, const QString &error) {
            return QMetaObject::invokeMethod(bridge.data(), "censorPlaybackResolved",
                Q_ARG(int, request), Q_ARG(QString, job), Q_ARG(QString, id),
                Q_ARG(QUrl, url), Q_ARG(QString, error));
        };
        QVERIFY(resolve(firstRequest, "detail", lastId, QUrl("file:///obsolete.wav"), "obsolete failure"));
        QVERIFY(resolve(currentRequest, "other-job", lastId, QUrl("file:///wrong-job.wav"), ""));
        QVERIFY(resolve(currentRequest, "detail", "first", QUrl("file:///wrong-candidate.wav"), ""));
        QVERIFY(view->property("resolvingPlayback").toBool());
        QVERIFY(view->property("playing").toBool());
        QVERIFY(view->property("playbackError").toString().isEmpty());
        QVERIFY(replacement->source().isEmpty());
        const QUrl fresh("file:///fresh-last.wav");
        QVERIFY(resolve(currentRequest, "detail", lastId, fresh, ""));
        QCOMPARE(replacement->source(), fresh);
        QVERIFY(!view->property("resolvingPlayback").toBool());
        QVERIFY(resolve(currentRequest, "detail", lastId, QUrl("file:///duplicate.wav"), "duplicate failure"));
        QCOMPARE(replacement->source(), fresh);
        QVERIFY(view->property("playbackError").toString().isEmpty());
        QVERIFY(QMetaObject::invokeMethod(view.data(), "stopPlayers"));
    }
    void censorSettingsDeferredAutosave() {
        Config config(QString{});
        MockBackend server;
        QJsonObject state{{"configured", true}, {"running", true}, {"busy", false},
                          {"jobs", QJsonArray{}}, {"settings", QJsonObject{{"workers", 2}}}};
        int saves = 0;
        bool rejectOnce = true;
        server.wireHook = [&](const QByteArray &request, QTcpSocket *) -> std::optional<QByteArray> {
            const bool post = request.startsWith("POST /api/censor/control ");
            if (!post && !request.startsWith("GET /api/censor/state ")) return std::nullopt;
            QJsonObject response = state;
            bool conflict = false;
            if (post) {
                const auto command = QJsonDocument::fromJson(request.mid(request.indexOf("\r\n\r\n") + 4)).object();
                if (command.value("action") != "settings") return std::nullopt;
                ++saves;
                conflict = rejectOnce;
                rejectOnce = false;
                if (conflict) {
                    state.insert("running", true);
                    response = QJsonObject{{"error", "Censor settings are waiting for active processing to finish"}};
                } else {
                    state.insert("settings", command.value("settings"));
                    response = state;
                }
            }
            const auto data = QJsonDocument(response).toJson(QJsonDocument::Compact);
            return QByteArray(conflict ? "HTTP/1.1 409 Conflict\r\n" : "HTTP/1.1 200 OK\r\n")
                + "Connection: close\r\nContent-Type: application/json\r\nContent-Length: "
                + QByteArray::number(data.size()) + "\r\n\r\n" + data;
        };
        Bridge bridge(server.url(), "censor-test-key", nullptr, &config);
        bridge.m_censorPoll.stop();
        QTRY_VERIFY(bridge.censorState().value("running").toBool());
        QSignalSpy toasts(&bridge, &Bridge::toast);
        QQmlEngine engine;
        QQmlComponent component(&engine, QUrl("qrc:/CensorSettingsPanel.qml"));
        QScopedPointer<QObject> panel(component.createWithInitialProperties({
            {"bridge", QVariant::fromValue(&bridge)}, {"width", 400}}));
        QVERIFY2(panel, qPrintable(component.errorString()));
        auto setWorkers = [&](int workers) {
            return QMetaObject::invokeMethod(panel.data(), "setValue", Q_ARG(QVariant, "workers"), Q_ARG(QVariant, workers));
        };
        QVERIFY(setWorkers(7));
        QVERIFY(panel->findChild<QObject *>("censorWorkers")->property("enabled").toBool());
        QVERIFY(QMetaObject::invokeMethod(panel.data(), "flushAutosave"));
        QCOMPARE(saves, 0);
        state.insert("running", false);
        bridge.m_censorState = state;
        emit bridge.censorStateChanged();
        QVERIFY(QMetaObject::invokeMethod(panel.data(), "flushAutosave"));
        QVERIFY(panel->property("saving").toBool());
        QVERIFY(panel->findChild<QObject *>("censorWorkers")->property("enabled").toBool());
        QVERIFY(setWorkers(9));
        QTRY_VERIFY(saves == 1 && !panel->property("saving").toBool());
        QVERIFY(panel->property("settingsQueued").toBool());
        QCOMPARE(panel->property("draft").toMap().value("workers").toInt(), 9);
        QCOMPARE(toasts.count(), 0);
        QTRY_VERIFY(bridge.censorState().value("running").toBool());
        state.insert("running", false);
        bridge.m_censorState = state;
        emit bridge.censorStateChanged();
        QTRY_VERIFY(saves == 2 && !panel->property("saving").toBool());
        QCOMPARE(state.value("settings").toObject().value("workers").toInt(), 9);
        QVERIFY(!panel->property("draftDirty").toBool());
        QVERIFY(!panel->property("settingsQueued").toBool());
    }
    void censorInlineSettings_data() {
        QTest::addColumn<int>("panelWidth");
        QTest::newRow("compact") << 280;
        QTest::newRow("stacked") << 400;
        QTest::newRow("two-columns") << 1000;
    }
    void censorInlineSettings() {
        QFETCH(int, panelWidth);
        Config config(QString{});
        MockBackend server;
        Bridge bridge(server.url(), "censor-test-key", nullptr, &config);
        bridge.m_censorPoll.stop();
        QTRY_VERIFY(!bridge.m_censorPolling);
        QQmlEngine engine;
        QSignalSpy warnings(&engine, &QQmlEngine::warnings);
        QQmlComponent component(&engine, QUrl("qrc:/CensorSettingsPanel.qml"));
        QScopedPointer<QObject> panel(component.createWithInitialProperties({
            {"bridge", QVariant::fromValue(&bridge)}, {"width", panelWidth}}));
        QVERIFY2(panel, qPrintable(component.errorString()));
        auto item = qobject_cast<QQuickItem *>(panel.data());
        QVERIFY(item);
        QTRY_VERIFY(item->implicitHeight() > 0);
        QCOMPARE(panel->property("color").value<QColor>(), QColor(Qt::transparent));
        QCOMPARE(panel->property("radius").toReal(), 0.0);
        auto panelBorder = qvariant_cast<QObject *>(panel->property("border"));
        QVERIFY(panelBorder);
        QCOMPARE(panelBorder->property("width").toReal(), 0.0);
        QCOMPARE(panelBorder->property("color").value<QColor>(), QColor(Qt::transparent));
        for (const QString &name : {"censorMode", "censorWorkers", "censorCpuThreads",
                                   "censorCandidateLimit"}) {
            auto control = panel->findChild<QQuickItem *>(name);
            QVERIFY2(control, qPrintable(name));
            QTRY_VERIFY(control->width() > 0);
            auto bounds = control->mapRectToItem(item, QRectF(0, 0, control->width(), control->height()));
            QVERIFY2(bounds.left() >= 0 && bounds.right() <= panelWidth + 0.5, qPrintable(name));
        }
        QVERIFY(QMetaObject::invokeMethod(panel.data(), "setValue",
            Q_ARG(QVariant, "workers"), Q_ARG(QVariant, 7)));
        bridge.m_censorState.insert("settings", QJsonObject{{"workers", 2}});
        emit bridge.censorStateChanged();
        QCOMPARE(panel->property("draft").toMap().value("workers").toInt(), 7);
        panel->setProperty("saving", true);
        emit bridge.censorCommandFinished("settings", false, "Retained draft");
        QCOMPARE(panel->property("saveError").toString(), QString("Retained draft"));
        QCOMPARE(panel->property("draft").toMap().value("workers").toInt(), 7);
        panel->setProperty("saving", true);
        emit bridge.censorCommandFinished("settings", true, {});
        QVERIFY(!panel->property("saving").toBool());
        QCOMPARE(panel->property("saveFeedback").toString(), QString("Settings saved"));
        emit bridge.censorStateChanged();
        QCOMPARE(panel->property("saveFeedback").toString(), QString("Settings saved"));
        QVERIFY(!panel->findChild<QObject *>("censorSettingsSaved"));
        QVERIFY(!panel->findChild<QObject *>("censorSettingsError"));
        QVERIFY(QMetaObject::invokeMethod(panel.data(), "loadSettings"));
        QCOMPARE(panel->property("draft").toMap().value("workers").toInt(), 7);
        QVERIFY(!panel->findChild<QObject *>("censorSettingsFooter"));
        QVERIFY(!panel->findChild<QObject *>("censorSettingsHeader"));
        const QStringList sectionNames = {"censorProcessingSection", "censorComparisonSection"};
        QList<QQuickItem *> sectionCards;
        for (const QString &name : sectionNames) {
            auto section = panel->findChild<QQuickItem *>(name);
            QVERIFY2(section, qPrintable(name));
            sectionCards.append(section);
            QCOMPARE(section->property("color").value<QColor>(), QColor("#0c0c0e"));
            QCOMPARE(section->property("radius").toReal(), 16.0);
            QVERIFY(!section->findChild<QObject *>("dialogCloseButton"));
        }
        const auto ancestorNamed = [](QQuickItem *child, const QString &name) {
            while (child && child->objectName() != name) child = child->parentItem();
            return child;
        };
        const QList<QPair<QString, QStringList>> sectionChildren = {
            {"censorProcessingSection", {"censorProcessingHeader", "censorMode", "censorWorkers", "censorCpuThreads"}},
            {"censorComparisonSection", {"censorComparisonHeader", "censorCandidateLimit"}}
        };
        const auto panelBounds = [item](QQuickItem *child) {
            return child->mapRectToItem(item, QRectF(0, 0, child->width(), child->height()));
        };
        for (const auto &[sectionName, childNames] : sectionChildren) {
            auto section = panel->findChild<QQuickItem *>(sectionName);
            const QRectF sectionBounds = panelBounds(section);
            for (const QString &childName : childNames) {
                auto child = panel->findChild<QQuickItem *>(childName);
                QVERIFY2(child, qPrintable(childName));
                QCOMPARE(ancestorNamed(child, sectionName), section);
                const QRectF childBounds = panelBounds(child);
                const QString containmentMessage = QString("%1 section=(%2,%3 %4x%5) child=(%6,%7 %8x%9)")
                    .arg(childName).arg(sectionBounds.x()).arg(sectionBounds.y())
                    .arg(sectionBounds.width()).arg(sectionBounds.height()).arg(childBounds.x())
                    .arg(childBounds.y()).arg(childBounds.width()).arg(childBounds.height());
                QVERIFY2(sectionBounds.contains(childBounds), qPrintable(containmentMessage));
            }
        }
        for (qsizetype index = 0; index < sectionCards.size(); ++index) {
            const QRectF bounds = panelBounds(sectionCards.at(index));
            QVERIFY(bounds.left() >= -0.5 && bounds.right() <= panelWidth + 0.5);
            for (qsizetype other = index + 1; other < sectionCards.size(); ++other)
                QVERIFY(!bounds.intersects(panelBounds(sectionCards.at(other))));
        }

        for (const QString &name : {"censorProcessingHeader", "censorComparisonHeader"}) {
            auto header = panel->findChild<QQuickItem *>(name);
            QVERIFY2(header, qPrintable(name));
            QCOMPARE(header->property("color").value<QColor>(), QColor("#171719"));
            QCOMPARE(header->property("radius").toReal(), 14.0);
        }
        const QMap<QString, QString> leadingGlyphs = {
            {"censorModeCard", "sliders-horizontal"}, {"censorWorkersCard", "layers"},
            {"censorCpuThreadsCard", "cpu"}, {"censorCandidateLimitCard", "list-filter"}
        };
        for (auto glyph = leadingGlyphs.cbegin(); glyph != leadingGlyphs.cend(); ++glyph) {
            auto card = panel->findChild<QQuickItem *>(glyph.key());
            QVERIFY2(card, qPrintable(glyph.key()));
            QCOMPARE(card->property("leadingGlyph").toString(), glyph.value());
            QCOMPARE(card->property("leadingGlyphColor").value<QColor>(), QColor("#999999"));
            auto icon = card->findChild<QQuickItem *>(glyph.key() + "LeadingGlyph");
            QVERIFY2(icon, qPrintable(glyph.key() + " icon"));
            QCOMPARE(icon->property("source").toUrl(), QUrl("qrc:/assets/lucide-" + glyph.value() + ".svg"));
        }

        QVERIFY(!panel->findChild<QQuickItem *>("censorCleanTemp"));
        QVERIFY(!panel->findChild<QQuickItem *>("censorAutoReplace"));
        QVERIFY(!panel->findChild<QQuickItem *>("censorMinConfidence"));
        QVERIFY(!panel->findChild<QQuickItem *>("censorMinConfidenceCard"));
        auto mode = panel->findChild<QQuickItem *>("censorMode");
        auto workers = panel->findChild<QQuickItem *>("censorWorkers");
        QVERIFY(mode && workers);
        QVERIFY(mode->property("glyph").toString().isEmpty());

        QVERIFY(mode->parentItem()->parentItem() != workers->parentItem()->parentItem());
        QCOMPARE(warnings.count(), 0);
    }
    void censorSettingsGeometry_data() {
        QTest::addColumn<int>("dialogWidth");
        QTest::newRow("520x500") << 520;
        QTest::newRow("400x500") << 400;
    }
    void censorSettingsGeometry() {
        QFETCH(int, dialogWidth);
        QQmlEngine engine;
        QQuickWindow window;
        window.resize(640, 560);
        window.show();
        QQmlComponent component(&engine, QUrl("qrc:/CensorSettingsDialog.qml"));
        QScopedPointer<QObject> dialog(component.createWithInitialProperties({
            {"parent", QVariant::fromValue(window.contentItem())},
            {"width", dialogWidth}, {"height", 500}}));
        QVERIFY2(dialog, qPrintable(component.errorString()));
        QVERIFY(QMetaObject::invokeMethod(dialog.data(), "open"));
        QTRY_VERIFY(dialog->property("opened").toBool());
        auto content = qvariant_cast<QQuickItem *>(dialog->property("contentItem"));
        QVERIFY(content);
        QVERIFY(!dialog->findChild<QObject *>("censorSettingsFooter"));
        QVERIFY(!dialog->findChild<QObject *>("censorSaveSettings"));
        QCOMPARE(content->property("bottomPadding").toReal(), 0.0);
        const auto bounds = [](QQuickItem *item) {
            return item->mapRectToScene(QRectF(0, 0, item->width(), item->height()));
        };
        QTRY_VERIFY(content->height() > 0);
        QVERIFY(content->clip());

        QQuickItem *first = nullptr;
        for (const QString &name : {"censorMode", "censorWorkers", "censorCpuThreads",
                                  "censorCandidateLimit"}) {
            auto control = dialog->findChild<QQuickItem *>(name);
            QVERIFY2(control, qPrintable(name));
            QVERIFY(control->width() > 0);
            if (!first) first = control;
            QVERIFY2(qAbs(control->width() - first->width()) < 0.5, qPrintable(name + " width"));
            QVERIFY2(qAbs(bounds(control).right() - bounds(first).right()) < 0.5,
                     qPrintable(name + " right alignment"));
            QVERIFY(bounds(control).left() >= bounds(content).left());
            QVERIFY(bounds(control).right() <= bounds(content).right());
            auto row = control->parentItem();
            for (auto sibling : row->childItems()) {
                if (sibling == control || !sibling->isVisible() || !sibling->property("text").isValid()) continue;
                QVERIFY2(bounds(sibling).right() <= bounds(control).left() + 0.5,
                         qPrintable(name + " label overlaps control"));
            }
        }
        QQuickItem *flickable = nullptr;
        const auto findFlickable = [&](auto &&self, QQuickItem *item) -> void {
            if (item->property("contentY").isValid() && item->property("contentHeight").isValid())
                flickable = item;
            for (auto child : item->childItems()) self(self, child);
        };
        findFlickable(findFlickable, content);
        QVERIFY(flickable);
        QVERIFY(flickable->property("contentHeight").toReal() > 0);
        QVERIFY(QMetaObject::invokeMethod(flickable, "cancelFlick"));
        const qreal end = flickable->property("contentHeight").toReal() - flickable->height()
            + flickable->property("bottomMargin").toReal();
        QVERIFY(flickable->setProperty("contentY", end));
        auto last = dialog->findChild<QQuickItem *>("censorCandidateLimit");
        QVERIFY(last);
        auto lastCard = last->parentItem();
        // The toggle is in the card's content layout; its parent is the card control.
        while (lastCard && !lastCard->property("compact").isValid()) lastCard = lastCard->parentItem();
        QVERIFY(lastCard);
        QTRY_VERIFY(bounds(lastCard).bottom() <= bounds(flickable).bottom() + 0.5);
        QVERIFY(bounds(lastCard).top() >= bounds(flickable).top() - 0.5);

    }

    void configSignalShutdown_data() {
#ifdef Q_OS_UNIX
        QTest::addColumn<int>("signalNumber");
        QTest::newRow("SIGINT") << int(SIGINT);
        QTest::newRow("SIGTERM") << int(SIGTERM);
#endif
    }
    void configSignalShutdown() {
#ifndef Q_OS_UNIX
        QSKIP("POSIX signal regression");
#else
        QFETCH(int, signalNumber);
        QTemporaryDir directory;
        QVERIFY(directory.isValid());
        const QString path = directory.filePath("Config.json");
        QProcess child;
        struct ReapChild {
            QProcess &process;
            ~ReapChild() { if (process.state() != QProcess::NotRunning) { process.kill(); process.waitForFinished(5000); } }
        } reap{child};
        child.start(QCoreApplication::applicationFilePath(), {"--signal-flush-child", path});
        QVERIFY(child.waitForStarted(5000));
        QByteArray output;
        QElapsedTimer timer; timer.start();
        while (!output.contains("READY\n") && timer.elapsed() < 5000) {
            child.waitForReadyRead(100);
            output += child.readAllStandardOutput();
        }
        QVERIFY2(output.contains("READY\n"), child.readAllStandardError().constData());
        QVERIFY(!QFile::exists(path));
        QCOMPARE(::kill(pid_t(child.processId()), signalNumber), 0);
        QVERIFY(child.waitForFinished(5000));
        QCOMPARE(child.exitStatus(), QProcess::NormalExit);
        QCOMPARE(child.exitCode(), 0);
        Config restored(path);
        QCOMPARE(restored.musicToken(), QString("signal-pending-token"));
        QCOMPARE(restored.volume(), 0.73);
#endif
    }
    void bridgeLargeGoStateAndConfiguredBudget() {
        MockBackend server;
        QJsonArray lyrics, allLines, tracks;
        const QString text(8192, QLatin1Char('x'));
        for (int line = 0; line < 128; ++line) {
            lyrics.append(QJsonObject{{"ts", QJsonValue::Null}, {"text", text}});
            allLines.append(QJsonObject{{"ts", QJsonValue::Null}, {"isDrug", false},
                {"parts", QJsonArray{QJsonObject{{"text", text}, {"marked", false}}}}});
        }
        for (int id = 1; id <= 12; ++id)
            tracks.append(QJsonObject{{"id", id}, {"title", "large valid track"}, {"artist", "artist"},
                {"status", "none"}, {"ignored", false}, {"lyrics", lyrics},
                {"lines", QJsonArray{}}, {"allLines", allLines}});
        server.state.insert("tracks", tracks);
        const auto body = QJsonDocument(server.state).toJson(QJsonDocument::Compact);
        QVERIFY(body.size() > 16 * 1024 * 1024);
        QVERIFY(body.size() < 256 * 1024 * 1024);
        QVERIFY(Bridge::DefaultMaximumResponseBytes >= qint64(257) * 1024 * 1024);
        Config config{QString()};
        Bridge bridge(server.url(), "key", nullptr, &config);
        QTRY_COMPARE_WITH_TIMEOUT(bridge.tracks()->count(), 12, 10000);
        QCOMPARE(bridge.m_source.first().toObject().value("lyrics").toArray().first().toObject().value("text").toString(), text);
        Bridge limited(server.url(), "key", nullptr, &config, 1024);
        QTRY_COMPARE(limited.connectionError(), QString("Backend response exceeds size limit"));
        QCOMPARE(limited.tracks()->count(), 0);
    }
    void initTestCase() {
        QVERIFY(settings.isValid());
        QSettings::setDefaultFormat(QSettings::IniFormat);
        QSettings::setPath(QSettings::IniFormat, QSettings::UserScope, settings.path());
        QQuickStyle::setStyle("Basic");
        qmlRegisterType<LyricsLayout>("DDetector.Lyrics", 1, 0, "LyricsLayout");
        qmlRegisterType<SyntaxHighlighter>("DDetector.Syntax", 1, 0, "SyntaxHighlighter");
        qmlRegisterType<EmbeddedTerminal>("DDetector.Terminal", 1, 0, "EmbeddedTerminal");
        qmlRegisterUncreatableType<TerminalSession>("DDetector.Terminal", 1, 0, "TerminalSession", "Owned by GitWorkspace");
        qmlRegisterType<AvatarImage>("DDetector", 1, 0, "AvatarImage");
        QDirIterator fonts(":/assets", {"*.ttf", "*.otf"}, QDir::Files);
        while (fonts.hasNext()) QFontDatabase::addApplicationFont(fonts.next());
        QFont font("DDetector Nunito"); font.setWeight(QFont::Bold); font.setPixelSize(13);
        QGuiApplication::setFont(font);
    }
    void siteComboBoxClickSelection() {
        QQmlEngine engine;
        QQuickWindow window;
        window.resize(360, 300);
        QQmlComponent component(&engine);
        component.setData(R"(
            import QtQuick
            import "qrc:/"
            SiteComboBox {
                x: 30; y: 20; width: 220
                model: ["First", "Blocked", "Last"]
                currentIndex: 0
                optionReason: value => value === "Blocked" ? "Unavailable" : null
                readonly property var popupObject: popup
                function optionAt(index) { return popup.contentItem.itemAtIndex(index); }
            }
        )", QUrl("qrc:/SiteComboBoxClickTest.qml"));
        QScopedPointer<QObject> object(component.createWithInitialProperties({{"parent", QVariant::fromValue(window.contentItem())}}));
        QVERIFY2(object, qPrintable(component.errorString()));
        auto combo = qobject_cast<QQuickItem *>(object.data());
        QVERIFY(combo);
        auto popup = combo->property("popupObject").value<QObject *>();
        QVERIFY(popup);
        QSignalSpy activated(combo, SIGNAL(activated(int)));
        QVERIFY(activated.isValid());
        window.show();
        QVERIFY(QTest::qWaitForWindowExposed(&window));
        const auto click = [&](QQuickItem *item) {
            QTest::mouseClick(&window, Qt::LeftButton, Qt::NoModifier,
                item->mapToScene(QPointF(item->width() / 2, item->height() / 2)).toPoint());
        };
        const auto optionAt = [&](int index) {
            QVariant result;
            QMetaObject::invokeMethod(combo, "optionAt", Q_RETURN_ARG(QVariant, result), Q_ARG(QVariant, index));
            return qobject_cast<QQuickItem *>(result.value<QObject *>());
        };
        click(combo);
        QTRY_VERIFY(popup->property("opened").toBool());
        QTRY_VERIFY(optionAt(1));
        auto blocked = optionAt(1);
        QVERIFY(!blocked->isEnabled());
        click(blocked);
        QCOMPARE(combo->property("currentIndex").toInt(), 0);
        QCOMPARE(combo->property("currentText").toString(), QString("First"));
        QCOMPARE(activated.count(), 0);
        QVERIFY(popup->property("opened").toBool());
        QTRY_VERIFY(optionAt(2));
        click(optionAt(2));
        QTRY_COMPARE(combo->property("currentIndex").toInt(), 2);
        QCOMPARE(combo->property("currentText").toString(), QString("Last"));
        QCOMPARE(activated.count(), 1);
        QCOMPARE(activated.first().first().toInt(), 2);
        QTRY_VERIFY(!popup->property("visible").toBool());
    }
    void siteSpinBoxStepButtons() {
        QQmlEngine engine;
        QQuickWindow window;
        window.resize(300, 160);
        QQmlComponent component(&engine);
        component.setData(R"(
            import QtQuick
            import "qrc:/"
            SiteSpinBox {
                x: 30; y: 30
                from: 0; to: 10; value: 4; stepSize: 2
                readonly property Item increaseButton: up.indicator
                readonly property Item decreaseButton: down.indicator
            }
        )", QUrl("qrc:/SiteSpinBoxClickTest.qml"));
        QScopedPointer<QObject> object(component.createWithInitialProperties({{"parent", QVariant::fromValue(window.contentItem())}}));
        QVERIFY2(object, qPrintable(component.errorString()));
        auto spin = qobject_cast<QQuickItem *>(object.data());
        QVERIFY(spin);
        auto increase = spin->property("increaseButton").value<QQuickItem *>();
        auto decrease = spin->property("decreaseButton").value<QQuickItem *>();
        QVERIFY(increase);
        QVERIFY(decrease);
        QSignalSpy modified(spin, SIGNAL(valueModified()));
        QVERIFY(modified.isValid());
        window.show();
        QVERIFY(QTest::qWaitForWindowExposed(&window));
        const auto click = [&](QQuickItem *item) {
            QTest::mouseClick(&window, Qt::LeftButton, Qt::NoModifier,
                item->mapToScene(QPointF(item->width() / 2, item->height() / 2)).toPoint());
        };
        QCOMPARE(modified.count(), 0);
        click(increase);
        QTRY_COMPARE(spin->property("value").toInt(), 6);
        QCOMPARE(modified.count(), 1);
        modified.clear();
        click(decrease);
        QTRY_COMPARE(spin->property("value").toInt(), 4);
        QCOMPARE(modified.count(), 1);
        modified.clear();
        QVERIFY(spin->setProperty("value", 10));
        QCOMPARE(modified.count(), 0);
        QVERIFY(!increase->isEnabled());
        click(increase);
        QCOMPARE(spin->property("value").toInt(), 10);
        QCOMPARE(modified.count(), 0);
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
    void visualizerSettingsPersistence() {
        QTemporaryDir directory;
        QVERIFY(directory.isValid());
        const auto path = directory.filePath("Config.json");
        Config config(path);
        QVERIFY(!config.visualizerEnabled());
        QSignalSpy changed(&config, &Config::settingsChanged);
        config.setVisualizerEnabled(true);
        QCOMPARE(changed.count(), 1);
        config.setVisualizerEnabled(true);
        QCOMPARE(changed.count(), 1);
        QVERIFY(config.flush());
        Config restored(path);
        QVERIFY(restored.visualizerEnabled());
        restored.setVisualizerEnabled(false);
        QVERIFY(restored.flush());
        Config disabled(path);
        QVERIFY(!disabled.visualizerEnabled());
        QFile file(path);
        QVERIFY(file.open(QIODevice::WriteOnly | QIODevice::Truncate));
        file.write("{\"visualizerEnabled\":\"invalid\"}");
        file.close();
        Config invalid(path);
        QVERIFY(!invalid.error().isEmpty());
        QVERIFY(!invalid.visualizerEnabled());
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
        QVERIFY(!config.approveSoundEnabled());
        config.setApproveSoundEnabled(true);
        config.setVolume(0.75);
        QVERIFY(config.error().isEmpty());
        QCOMPARE(changed.count(), 6);
        QVERIFY(config.flush());
        Config restored(path);
        QCOMPARE(restored.repositoryPath(), repositoryPath);
        QCOMPARE(restored.musicToken(), QString("test-private-token"));
        QVERIFY(restored.hideIgnored());
        QVERIFY(restored.hideWindowControls());
        QVERIFY(restored.approveSoundEnabled());
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
        QVERIFY(config.flush());
        Config cleared(path);
        QVERIFY(cleared.repositoryPath().isEmpty());
        QCOMPARE(changed.count(), 7);
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
    void censorGetOperationContention() {
        MockBackend server;
        QJsonObject job{{"id", "selected"}, {"detail_revision", "one"}, {"status", "stopped"}};
        QString conflict;
        int gets = 0;
        server.wireHook = [&](const QByteArray &request, QTcpSocket *) -> std::optional<QByteArray> {
            if (!request.startsWith("GET /api/censor/")) return std::nullopt;
            ++gets;
            const bool detail = request.startsWith("GET /api/censor/job?");
            const auto data = QJsonDocument(!conflict.isEmpty() ? QJsonObject{{"error", conflict}}
                : detail ? QJsonObject{{"job", job}}
                : QJsonObject{{"configured", true}, {"jobs", QJsonArray{job}}}).toJson(QJsonDocument::Compact);
            return QByteArray(conflict.isEmpty() ? "HTTP/1.1 200 OK\r\n" : "HTTP/1.1 409 Conflict\r\n")
                + "Connection: close\r\nContent-Type: application/json\r\nContent-Length: "
                + QByteArray::number(data.size()) + "\r\n\r\n" + data;
        };
        Config config{QString()};
        Bridge bridge(server.url(), "contention-test", nullptr, &config);
        bridge.m_censorPoll.stop(); bridge.m_poll.stop();
        QTRY_VERIFY(!bridge.m_censorPolling);
        bridge.censorSelectJob("selected");
        QTRY_VERIFY(!bridge.m_censorDetailPolling);
        const auto state = bridge.censorState();
        const auto selected = bridge.censorSelectedJob();
        QSignalSpy stateChanged(&bridge, &Bridge::censorStateChanged);
        QSignalSpy detailChanged(&bridge, &Bridge::censorSelectedJobChanged);
        conflict = "another operation is running";
        bridge.censorRequest();
        bridge.censorRequest("__detail");
        QTRY_VERIFY(!bridge.m_censorPolling && !bridge.m_censorDetailPolling);
        QCOMPARE(bridge.censorState(), state);
        QCOMPARE(bridge.censorSelectedJob(), selected);
        QCOMPARE(stateChanged.count(), 0);
        QCOMPARE(detailChanged.count(), 0);
        const int afterContention = gets;
        QTest::qWait(350);
        QCOMPARE(gets, afterContention); // No immediate retry loop.

        // A selection change during contention must recover on the next normal poll.
        bridge.censorSelectJob("replacement");
        QTRY_VERIFY(!bridge.m_censorDetailPolling);
        QVERIFY(bridge.censorSelectedJob().isEmpty());
        conflict.clear();
        job.insert("id", "replacement");
        bridge.censorRequest();
        QTRY_COMPARE(bridge.censorSelectedJob().value("id").toString(), QString("replacement"));
        QTRY_VERIFY(!bridge.m_censorDetailPolling);

        conflict = "conflicting operation settings";
        bridge.censorRequest();
        QTRY_VERIFY(!bridge.m_censorPolling);
        QCOMPARE(bridge.censorState().value("error").toString(), conflict);
        bridge.censorRequest("__detail");
        QTRY_VERIFY(!bridge.m_censorDetailPolling);
        QCOMPARE(bridge.censorSelectedJob().value("detail_error").toString(), conflict);
    }
    void censorDetailRevisionPolling() {
        MockBackend server;
        QJsonObject job{{"id", "selected"}, {"status", "stopped"}, {"detail_revision", "one"}};
        int details = 0;
        server.wireHook = [&](const QByteArray &request, QTcpSocket *) -> std::optional<QByteArray> {
            if (!request.startsWith("GET /api/censor/")) return std::nullopt;
            const bool detail = request.startsWith("GET /api/censor/job?");
            if (detail) ++details;
            const auto data = QJsonDocument(detail ? QJsonObject{{"job", job}}
                : QJsonObject{{"configured", true}, {"jobs", QJsonArray{job}}}).toJson(QJsonDocument::Compact);
            return "HTTP/1.1 200 OK\r\nConnection: close\r\nContent-Type: application/json\r\nContent-Length: "
                + QByteArray::number(data.size()) + "\r\n\r\n" + data;
        };
        Config config{QString()};
        Bridge bridge(server.url(), "revision-test", nullptr, &config);
        bridge.m_censorPoll.stop(); bridge.m_poll.stop();
        QTRY_VERIFY(!bridge.m_censorPolling);
        bridge.censorSelectJob("selected");
        QTRY_VERIFY(!bridge.m_censorDetailPolling);
        QCOMPARE(details, 1);
        for (int poll = 0; poll < 3; ++poll) {
            bridge.censorRequest();
            QTRY_VERIFY(!bridge.m_censorPolling);
            QVERIFY(!bridge.m_censorDetailPolling);
            QCOMPARE(details, 1);
        }
        job.insert("detail_revision", "two"); job.insert("status", "running");
        bridge.censorRequest();
        QTRY_COMPARE(bridge.censorSelectedJob().value("status").toString(), QString("running"));
        QCOMPARE(details, 2);
        job.remove("detail_revision"); // Legacy/oversized jobs must not suppress detail polling.
        for (int poll = 0; poll < 2; ++poll) {
            bridge.censorRequest();
            QTRY_VERIFY(!bridge.m_censorPolling);
            QTRY_VERIFY(!bridge.m_censorDetailPolling);
            QCOMPARE(details, 3 + poll);
        }
        bridge.censorSelectJob({});
        bridge.censorRequest();
        QTRY_VERIFY(!bridge.m_censorPolling);
        QCOMPARE(details, 4);
        QVERIFY(bridge.censorSelectedJob().isEmpty());
    }
    void modelScatteredRemovalCost() {
        TrackModel model;
        QAbstractItemModelTester tester(&model, QAbstractItemModelTester::FailureReportingMode::QtTest);
        QList<QJsonObject> rows, retained;
        for (int i = 0; i < 1000; ++i) {
            const QJsonObject row{{"id", i + 1}};
            rows.append(row);
            if (i >= 200 || i % 2) retained.append(row);
        }
        model.sync(rows);
        QSignalSpy resets(&model, &QAbstractItemModel::modelReset);
        model.sync(retained);
        QCOMPARE(resets.count(), 1);
        QCOMPARE(model.count(), 900);
        QCOMPARE(model.indexOf("1000"), 899);
    }
    void modelContiguousRemovalRanges() {
            for (int removed : {1, 100, 256, 257}) {
                TrackModel model;
                QAbstractItemModelTester tester(&model, QAbstractItemModelTester::FailureReportingMode::QtTest);
                QList<QJsonObject> rows;
                for (int i = 0; i < removed + 2; ++i) rows.append(QJsonObject{{"id", i + 1}});
                model.sync(rows);
                QPersistentModelIndex held(model.index(removed + 1));
                QPersistentModelIndex deleted(model.index(1));
                QSignalSpy ranges(&model, &QAbstractItemModel::rowsRemoved);
                QSignalSpy resets(&model, &QAbstractItemModel::modelReset);
                model.sync({rows.first(), rows.last()});
                QCOMPARE(ranges.count(), 1); QCOMPARE(resets.count(), 0);
                QCOMPARE(ranges.first().at(1).toInt(), 1);
                QCOMPARE(ranges.first().at(2).toInt(), removed);
                QCOMPARE(held.row(), 1); QVERIFY(!deleted.isValid());
                QCOMPARE(model.indexOf(QString::number(removed + 2)), 1);
            }
        }
        void bridgeDeltaScalarAndClearing() {
            MockBackend server;
            Bridge bridge(server.url(), "bridge-key");
            QJsonObject state{{"authenticated", true}, {"hasAccess", true},
                              {"tracksRevision", "one"}, {"tracks", QJsonArray{QJsonObject{{"id", 1}}}}};
            bridge.acceptState(state);
            QSignalSpy changes(bridge.tracks(), &QAbstractItemModel::dataChanged);
            QSignalSpy resets(bridge.tracks(), &QAbstractItemModel::modelReset);
            state.remove("tracks"); state.insert("processed", 1);
            bridge.acceptState(state);
            QCOMPARE(bridge.tracks()->count(), 1);
            QCOMPARE(changes.count(), 0); QCOMPARE(resets.count(), 0);
            state.insert("hasAccess", false);
            bridge.acceptState(state);
            QCOMPARE(bridge.tracks()->count(), 0);
            QVERIFY(bridge.m_tracksRevision.isEmpty());
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
    void modelDataCacheIsLazyBoundedAndCoherent() {
        TrackModel model;
        QAbstractItemModelTester tester(&model, QAbstractItemModelTester::FailureReportingMode::QtTest);
        QList<QJsonObject> rows;
        for (int i = 1; i <= 1000; ++i)
            rows.append({{"id", i}, {"title", QString::number(i)},
                         {"lyrics", QJsonArray{QJsonObject{{"text", QString(4096, 'x')}}}}});
        model.sync(rows);
        // The model tester may request data itself; start with an empty cache.
        model.m_dataCache.clear();
        QCOMPARE(model.count(), 1000);
        QCOMPARE(model.rowCount(), 1000);
        QCOMPARE(model.indexOf("1000"), 999);
        QVERIFY(!model.data(QModelIndex(), Qt::UserRole).isValid());
        QVERIFY(!model.data(model.index(0), Qt::DisplayRole).isValid());
        QCOMPARE(model.m_dataCache.size(), 0);
        const auto first = model.data(model.index(0), Qt::UserRole);
        QCOMPARE(first.toMap().value("id").toString(), QString("1"));
        const auto cached = model.m_dataCache.object(0);
        QCOMPARE(model.data(model.index(0), Qt::UserRole), first);
        QCOMPARE(model.m_dataCache.object(0), cached);
        model.sync(rows);
        QCOMPARE(model.m_dataCache.object(0), cached);
        for (int i = 1; i <= TrackModel::DataCacheCapacity; ++i)
            model.data(model.index(i), Qt::UserRole);
        QCOMPARE(model.m_dataCache.size(), TrackModel::DataCacheCapacity);
        QVERIFY(!model.m_dataCache.contains(0));
        model.data(model.index(999), Qt::UserRole);
        rows[999].insert("title", "updated");
        bool notifiedWithFreshData = false;
        connect(&model, &QAbstractItemModel::dataChanged, &model,
                [&](const QModelIndex &start, const QModelIndex &, const QList<int> &) {
            if (start.row() == 999)
                notifiedWithFreshData = model.data(start, Qt::UserRole).toMap().value("title") == "updated";
        });
        model.sync(rows);
        QVERIFY(notifiedWithFreshData);
        QPersistentModelIndex held(model.index(999));
        std::reverse(rows.begin(), rows.end());
        model.sync(rows);
        QCOMPARE(held.row(), 0);
        QCOMPARE(held.data(Qt::UserRole).toMap().value("title").toString(), QString("updated"));
        QCOMPARE(model.indexOf("1000"), 0);
        rows.removeFirst();
        model.sync(rows);
        QVERIFY(!held.isValid());
        QCOMPARE(model.data(model.index(0), Qt::UserRole).toMap().value("id").toString(), QString("999"));
        rows.append({{"id", 1001}, {"title", "new"}});
        model.sync(rows);
        QCOMPARE(model.data(model.index(999), Qt::UserRole).toMap().value("id").toString(), QString("1001"));
        model.sync({});
        QCOMPARE(model.count(), 0);
        QCOMPARE(model.indexOf("1001"), -1);
        QCOMPARE(model.m_dataCache.size(), 0);
    }
    void modelRejectsDuplicateIdsAndBulkReorders() {
        TrackModel model;
        QAbstractItemModelTester tester(&model, QAbstractItemModelTester::FailureReportingMode::QtTest);
        model.sync({{{"id", 1}}, {{"id", "1"}}, {{"id", ""}}, {{"id", 0}}, {{"id", -1}},
                    {{"id", 1.5}}, {{"title", "missing"}}, {{"id", "../2"}}, {{"id", "2"}}});
        QCOMPARE(model.count(), 2);
        QCOMPARE(model.indexOf("1"), 0); QCOMPARE(model.indexOf("2"), 1);
        QList<QJsonObject> rows;
        for (int i = 1; i <= 10000; ++i) rows.append({{"id", QString::number(i)}, {"title", "track"}});
        model.sync(rows);
        QPersistentModelIndex held(model.index(42));
        QSignalSpy resets(&model, &QAbstractItemModel::modelReset);
        QSignalSpy moves(&model, &QAbstractItemModel::rowsMoved);
        std::reverse(rows.begin(), rows.end());
        model.sync(rows);
        QCOMPARE(held.row(), 10000 - 43);
        QCOMPARE(held.data(Qt::UserRole).toMap().value("id").toString(), QString("43"));
        QCOMPARE(resets.count(), 0); QCOMPARE(moves.count(), 0);
        QCOMPARE(model.indexOf("1"), 9999);
    }
    void configDebounceFailureRetryAndShutdown() {
        QTemporaryDir dir; QVERIFY(dir.isValid());
        const QString path = dir.filePath("settings/Config.json");
        {
            Config config(path);
            for (int i = 0; i < 100; ++i) config.setVolume(i / 100.0);
            QVERIFY(!QFile::exists(path));
            QTRY_VERIFY(QFile::exists(path));
            Config loaded(path); QCOMPARE(loaded.volume(), 0.99);
            config.setVolume(0.25);
            // Destruction must flush an unexpired debounce.
        }
        Config loaded(path); QCOMPARE(loaded.volume(), 0.25);
        const QString blocked = dir.filePath("blocked");
        QFile file(blocked); QVERIFY(file.open(QIODevice::WriteOnly)); file.write("file, not directory"); file.close();
        Config failed(blocked + "/Config.json"); failed.setMusicToken("retry-token");
        QVERIFY(!failed.flush()); QVERIFY(!failed.error().isEmpty());
        QVERIFY(QFile::remove(blocked)); QVERIFY(QDir().mkdir(blocked));
        failed.setMusicToken("retry-token");
        QTRY_VERIFY(QFile::exists(blocked + "/Config.json"));
        QVERIFY(failed.error().isEmpty());
        Config recovered(blocked + "/Config.json"); QCOMPARE(recovered.musicToken(), QString("retry-token"));
        QFile stored(blocked + "/Config.json"); QVERIFY(stored.open(QIODevice::ReadOnly));
        const auto original = stored.readAll(); stored.close();
        failed.setMusicToken(QString(1024 * 1024, 'x')); QVERIFY(!failed.flush());
        QVERIFY(failed.error().contains("1 MiB"));
        QVERIFY(stored.open(QIODevice::ReadOnly)); QCOMPARE(stored.readAll(), original); stored.close();
        failed.setMusicToken("retry-token"); QVERIFY(failed.flush()); QVERIFY(failed.error().isEmpty());
    }
    void tokenRetriesTransientFailure() {
        MockBackend server;
        int sends = 0;
        server.wireHook = [&](const QByteArray &request, QTcpSocket *) -> std::optional<QByteArray> {
            if (request.startsWith("POST /api/yandex/token ") && ++sends == 1)
                return QByteArray("HTTP/1.1 503 Unavailable\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}");
            return std::nullopt;
        };
        Config config{QString()}; config.setMusicToken("test-token");
        Bridge bridge(server.url(), "key", nullptr, &config);
        QTRY_COMPARE_WITH_TIMEOUT(sends, 2, 5000);
        QTRY_COMPARE(bridge.m_yandexAttemptedToken, QString("test-token"));
        QVERIFY(!bridge.m_yandexRetry.isActive());
        QTRY_VERIFY(std::any_of(server.requests.cbegin(), server.requests.cend(), [](const auto &request) {
            return request.startsWith("POST /api/load ");
        }));
        int loads = 0;
        for (const auto &request : server.requests) if (request.startsWith("POST /api/load ")) ++loads;
        QCOMPARE(loads, 1);
    }
    void logoutClearsUnacknowledgedTokenAndCancelsRetry() {
        MockBackend server; int sends = 0;
        server.wireHook = [&](const QByteArray &request, QTcpSocket *) -> std::optional<QByteArray> {
            if (request.startsWith("POST /api/yandex/token ") && ++sends == 1)
                return QByteArray("HTTP/1.1 503 Unavailable\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}");
            if (request.startsWith("POST /api/logout ")) {
                server.state.insert("authenticated", false); server.state.insert("hasAccess", false);
            }
            return std::nullopt;
        };
        Config config{QString()}; config.setMusicToken("uncertain-token");
        Bridge bridge(server.url(), "key", nullptr, &config);
        QTRY_VERIFY(bridge.m_yandexRetry.isActive());
        bridge.logout();
        QTRY_COMPARE(sends, 2); QTRY_VERIFY(!bridge.m_yandexSyncing);
        QVERIFY(!bridge.m_yandexRetry.isActive()); QVERIFY(!bridge.m_yandexMayHaveToken);
        bool cleared = false;
        for (const auto &request : server.requests)
            if (request.startsWith("POST /api/yandex/token ") && request.contains("\"access_token\":\"\"")) cleared = true;
        QVERIFY(cleared);
        QTest::qWait(1200); QCOMPARE(sends, 2);
    }
    void stalePollCannotResurrectLogout() {
        MockBackend server; Config config{QString()};
        Bridge bridge(server.url(), "key", nullptr, &config);
        QTRY_COMPARE(bridge.tracks()->count(), 6); QTRY_VERIFY(!bridge.m_polling);
        bridge.m_poll.stop(); server.getDelayMs = 250;
        server.wireHook = [&](const QByteArray &request, QTcpSocket *) -> std::optional<QByteArray> {
            if (request.startsWith("POST /api/logout ")) {
                server.state.insert("authenticated", false); server.state.insert("hasAccess", false);
            }
            return std::nullopt;
        };
        const int before = server.requests.size();
        bridge.m_stateETag.clear(); bridge.request("/api/state", {}, true);
        QTRY_VERIFY(server.requests.size() > before);
        bridge.logout();
        bool resurrected = false;
        connect(&bridge, &Bridge::stateChanged, this, [&] {
            if (bridge.state().value("authenticated").toBool()) resurrected = true;
        });
        QTRY_VERIFY(server.requests.size() >= before + 3);
        QTRY_VERIFY(!bridge.m_polling);
        QVERIFY(!resurrected); QVERIFY(!bridge.state().value("authenticated").toBool());
        QCOMPARE(bridge.tracks()->count(), 0);
    }
    void tokenPermanentFailureDoesNotLoop() {
        MockBackend server; int sends = 0;
        server.wireHook = [&](const QByteArray &request, QTcpSocket *) -> std::optional<QByteArray> {
            if (request.startsWith("POST /api/yandex/token ")) {
                ++sends;
                return QByteArray("HTTP/1.1 401 Unauthorized\r\nContent-Length: 6\r\nConnection: close\r\n\r\ndenied");
            }
            return std::nullopt;
        };
        Config config{QString()}; config.setMusicToken("bad-token");
        Bridge bridge(server.url(), "key", nullptr, &config);
        QTRY_COMPARE(sends, 1); QTRY_VERIFY(!bridge.m_yandexSyncing);
        QTest::qWait(1200); QCOMPARE(sends, 1);
        config.setMusicToken("different-token"); QTRY_COMPARE(sends, 2);
    }
    void bridgeResponseBudgetAndAbsoluteDeadline() {
        MockBackend server; Config config{QString()};
        Bridge bridge(server.url(), "key", nullptr, &config);
        QTRY_COMPARE(bridge.tracks()->count(), 6); QTRY_VERIFY(!bridge.m_polling);
        bridge.m_poll.stop(); bridge.m_maximumResponseBytes = 64;
        for (bool chunked : {false, true}) {
            server.wireHook = [chunked](const QByteArray &, QTcpSocket *) -> std::optional<QByteArray> {
                return chunked
                    ? QByteArray("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n\r\n3e8\r\n") + QByteArray(1000, 'x') + "\r\n0\r\n\r\n"
                    : QByteArray("HTTP/1.1 200 OK\r\nContent-Length: 1000\r\nConnection: close\r\n\r\n") + QByteArray(1000, 'x');
            };
            bridge.setConnectionError({}); bridge.m_stateETag.clear(); bridge.request("/api/state", {}, true);
            QTRY_VERIFY(!bridge.m_polling);
            QVERIFY(bridge.connectionError().contains("size limit"));
            QCOMPARE(bridge.tracks()->count(), 6);
        }
        server.holdConnection = true;
        server.wireHook = [](const QByteArray &, QTcpSocket *socket) -> std::optional<QByteArray> {
            auto timer = new QTimer(socket);
            QObject::connect(timer, &QTimer::timeout, socket, [socket] { socket->write(" "); });
            timer->start(20);
            return QByteArray("HTTP/1.1 200 OK\r\nContent-Length: 1000\r\n\r\n{");
        };
        bridge.m_maximumResponseBytes = 4096; bridge.m_requestDeadlineMs = 120;
        bridge.request("/api/state", {}, true);
        QTRY_VERIFY_WITH_TIMEOUT(bridge.connectionError().contains("timed out"), 1500);
        QVERIFY(!bridge.m_polling); QCOMPARE(bridge.tracks()->count(), 6);
    }
    void credentialMigrationPersistenceContract() {
        QTemporaryDir directory; QVERIFY(directory.isValid());
        const auto path = directory.filePath("Config.json");
        Config config(path);
        config.setMusicToken("legacy-secret"); QVERIFY(config.flush());
        config.m_useKeychain = true;
        // Model an OS write pending/failed: the only durable copy must survive.
        config.scheduleSave(); QVERIFY(config.flush());
        const auto read = [&] {
            QFile file(path); if (!file.open(QIODevice::ReadOnly)) return QJsonObject();
            return QJsonDocument::fromJson(file.readAll()).object();
        };
        QCOMPARE(read().value("musicToken").toString(), QString("legacy-secret"));
        config.m_credentialStored = true; config.m_storedToken = "legacy-secret";
        config.scheduleSave(); QVERIFY(config.flush());
        QVERIFY(!read().contains("musicToken"));
        config.setMusicToken("replacement"); QVERIFY(config.flush());
        QCOMPARE(read().value("musicToken").toString(), QString("replacement"));
        config.setMusicToken({}); QVERIFY(config.flush());
        QVERIFY(read().contains("musicToken")); // Explicit logout overrides a stale keychain value.
        QCOMPARE(read().value("musicToken").toString(), QString());
        config.m_storedToken.clear(); config.scheduleSave(); QVERIFY(config.flush());
        QVERIFY(!read().contains("musicToken"));
        Config pending(directory.filePath("pending.json"));
        pending.m_useKeychain = true; pending.setVolume(0.7); QVERIFY(pending.flush());
        QFile file(directory.filePath("pending.json")); QVERIFY(file.open(QIODevice::ReadOnly));
        QVERIFY(!QJsonDocument::fromJson(file.readAll()).object().contains("musicToken"));
    }
    void credentialFallbackAndDelayedRead() {
        QTemporaryDir directory; QVERIFY(directory.isValid());
        const auto path = directory.filePath("Config.json");
        {
            Config fallback(path);
            fallback.setVolume(0.7);
            QVERIFY(fallback.flush());
        }
        QFile file(path); QVERIFY(file.open(QIODevice::ReadOnly));
        QVERIFY(!QJsonDocument::fromJson(file.readAll()).object().contains("musicToken"));
        file.close();
        Config restored(path);
        QVERIFY(!restored.m_credentialEdited);
        QVERIFY(restored.acceptCredentialRead(true, false, "stored-secret"));
        QCOMPARE(restored.musicToken(), QString("stored-secret"));
        Config clearing{QString()};
        clearing.setMusicToken({});
        QVERIFY(clearing.m_credentialEdited);
        QVERIFY(clearing.acceptCredentialRead(true, false, "old-secret"));
        QVERIFY(clearing.musicToken().isEmpty());
        Config retry{QString()};
        QVERIFY(!retry.acceptCredentialRead(false, false, {}));
        QVERIFY(!retry.m_credentialReady);
        QVERIFY(retry.m_credentialRetry.isActive());
        retry.setMusicToken("replacement");
        QVERIFY(retry.acceptCredentialRead(true, false, "old-secret"));
        QVERIFY(!retry.m_credentialRetry.isActive());
        QCOMPARE(retry.musicToken(), QString("replacement"));
    }
    void credentialWriteRetryAndStaleCompletion() {
        QTemporaryDir directory; QVERIFY(directory.isValid());
        const auto path = directory.filePath("Config.json");
        Config config(path);
        config.setMusicToken("replacement"); QVERIFY(config.flush());
        QVERIFY(!config.acceptCredentialWrite(false, "old-secret"));
        QVERIFY(config.m_credentialWriteRetry.isActive());
        QCOMPARE(config.m_credentialWriteAttempts, 1);
        config.m_credentialWriteRetry.stop();
        QVERIFY(config.acceptCredentialWrite(true, "old-secret"));
        QVERIFY(!config.m_credentialWriteRetry.isActive());
        QCOMPARE(config.musicToken(), QString("replacement"));
        config.m_useKeychain = true;
        QVERIFY(config.flush());
        const auto read = [&] {
            QFile file(path); if (!file.open(QIODevice::ReadOnly)) return QJsonObject();
            return QJsonDocument::fromJson(file.readAll()).object();
        };
        QCOMPARE(read().value("musicToken").toString(), QString("replacement"));
        QVERIFY(!config.acceptCredentialWrite(true, "replacement"));
        QVERIFY(config.flush());
        QVERIFY(!read().contains("musicToken"));
        // Failure retries are bounded; explicit retryCredentials resets the budget.
        for (int i = 0; i < 4; ++i) {
            config.m_credentialWriteRetry.stop();
            QVERIFY(!config.acceptCredentialWrite(false, "replacement"));
        }
        QCOMPARE(config.m_credentialWriteAttempts, 3);
        QVERIFY(!config.m_credentialWriteRetry.isActive());
    }
    void repositoryCoordinatorRejectsUnsafeSwitches() {
        QTemporaryDir a, b; QVERIFY(a.isValid()); QVERIFY(b.isValid());
        const auto git = [](const QString &root, const QStringList &args) {
            QProcess process; process.setWorkingDirectory(root);
            process.start("git", QStringList{"--no-pager", "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false"} + args);
            return process.waitForFinished(5000) && process.exitCode() == 0;
        };
        for (const auto &root : {a.path(), b.path()}) {
            QVERIFY(git(root, {"init", "-q"}));
            QFile file(root + "/README.md"); QVERIFY(file.open(QIODevice::WriteOnly)); file.write("# Tracks\n"); file.close();
            for (const auto &name : {QString("list_v2.json"), QString("list.json")}) {
                QFile list(root + "/" + name); QVERIFY(list.open(QIODevice::WriteOnly));
                list.write(name == "list_v2.json" ? "{\"sources\":[]}" : "legacy bytes");
            }
            QVERIFY(git(root, {"add", "README.md", "list_v2.json", "list.json"}));
            QVERIFY(git(root, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "initial"}));
            QVERIFY(git(root, {"config", "user.name", "Test"}));
            QVERIFY(git(root, {"config", "user.email", "test@example.invalid"}));
        }
        Config config{QString()}; config.setRepositoryPath(a.path());
        LocalContexts local(config);
        QCOMPARE(local.workspace.repositoryPath(), config.repositoryPath());
        QVERIFY(local.uploader.directory().isEmpty());
        QVERIFY(local.workspace.resolving());
        config.setRepositoryPath(b.path()); QCOMPARE(config.repositoryPath(), a.path());
        QTRY_VERIFY(!local.workspace.resolving()); QCOMPARE(local.uploader.directory(), a.path());
        local.workspace.openFile("README.md"); QTRY_VERIFY(!local.workspace.fileLoading()); local.workspace.setText("dirty");
        config.setRepositoryPath(b.path()); QCOMPARE(config.repositoryPath(), a.path());
        local.uploader.send("1", "song", "artist"); QVERIFY(!local.uploader.busy());
        QVERIFY(local.uploader.error().contains("edits"));
        local.workspace.setText("# Tracks\n");
        local.workspace.openFile("list_v2.json"); QTRY_VERIFY(!local.workspace.fileLoading()); local.workspace.setText("unsaved v2 edits");
        local.uploader.send("1", "song", "artist"); QVERIFY(!local.uploader.busy());
        QVERIFY(local.uploader.error().contains("edits"));
        local.workspace.setText("{\"sources\":[]}");
        local.workspace.openFile("list.json"); QTRY_VERIFY(!local.workspace.fileLoading()); local.workspace.setText("unsaved legacy edits");
        local.uploader.send("invalid-id", "song", "artist"); QVERIFY(local.uploader.busy());
        QTRY_VERIFY_WITH_TIMEOUT(!local.uploader.busy(), 20000);
        QVERIFY(local.uploader.error().contains("ASCII digits"));
        QVERIFY(local.workspace.dirty()); QCOMPARE(local.workspace.text(), QString("unsaved legacy edits"));
        local.workspace.setText("legacy bytes"); local.workspace.openFile("README.md"); QTRY_VERIFY(!local.workspace.fileLoading());
        local.workspace.startTerminal(); QVERIFY(local.workspace.terminalRunning());
        config.setRepositoryPath(b.path()); QCOMPARE(config.repositoryPath(), a.path());
        local.workspace.stopTerminal();
        QTcpServer server; QVERIFY(server.listen(QHostAddress::LocalHost));
        QPointer<QTcpSocket> audio;
        connect(&server, &QTcpServer::newConnection, this, [&] {
            audio = server.nextPendingConnection();
            connect(audio, &QTcpSocket::readyRead, audio, [audio] { audio->readAll(); });
        });
        local.uploader.send("1", "song", "artist", "http://127.0.0.1:" + QString::number(server.serverPort()) + "/audio");
        QTRY_VERIFY(audio); QVERIFY(local.uploader.busy());
        config.setRepositoryPath(b.path()); QCOMPARE(config.repositoryPath(), a.path());
        local.workspace.setRepositoryPath(b.path()); QCOMPARE(local.workspace.repositoryPath(), a.path());
        QVERIFY(!local.uploader.setDirectory(b.path()));
        local.workspace.setText("local edits"); QVERIFY(!local.workspace.save());
        audio->write("HTTP/1.1 200 OK\r\nContent-Length: 3\r\nConnection: close\r\n\r\nmp3");
        audio->disconnectFromHost();
        QTRY_VERIFY_WITH_TIMEOUT(!local.uploader.busy(), 20000);
        QCOMPARE(local.uploader.directory(), a.path());
        local.workspace.requestClose(); local.workspace.resolveUnsaved("discard");
        config.setRepositoryPath(b.path());
        QCOMPARE(config.repositoryPath(), b.path());
        QVERIFY(local.uploader.directory().isEmpty());
        QTRY_VERIFY(!local.workspace.resolving()); QCOMPARE(local.workspace.rootPath(), b.path());
        QCOMPARE(local.uploader.directory(), b.path());
        config.setVolume(0.123); QCOMPARE(local.workspace.rootPath(), b.path());
        if (audio) delete audio;
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
    void developerToolsIndicator() {
        Config config{QString()};
        MockBackend server;
        Bridge bridge(server.url(), "test-random-key", nullptr, &config);
        QTRY_COMPARE(bridge.tracks()->count(), 6);
        MusicPlayer player;
        LocalContexts local(config);
        QQmlApplicationEngine engine;
        local.attach(engine, config, bridge, player);
        QSignalSpy warnings(&engine, &QQmlEngine::warnings);
        engine.load(QUrl("qrc:/Main.qml"));
        QVERIFY(!engine.rootObjects().isEmpty());
        auto window = qobject_cast<QQuickWindow *>(engine.rootObjects().first());
        QVERIFY(window);
        QVERIFY(!window->findChild<QQuickItem *>("devToolsButton"));
        QVERIFY(!window->findChild<QQuickItem *>("accountDevMenuEntry"));
        auto separator = window->findChild<QQuickItem *>("accountDevMenuSeparator");
        QVERIFY(separator);
        QVERIFY(!separator->property("visible").toBool());
        if (!QFile::exists(":/DevTools.qml")) return;

        QQmlComponent mockUpdater(&engine);
        mockUpdater.setData(R"(
            import QtQml
            QtObject {
                property bool available: false
                property bool busy: false
                property string version: "v1.2.3"
                property string status: ""
                property string error: ""
                property real progress: -1
                property int installs: 0
                signal changed()
                function forceAvailable() { available = true; }
                function install() { installs++; busy = true; progress = 0; changed(); }
            }
        )", QUrl());
        QScopedPointer<QObject> updater(mockUpdater.create());
        QVERIFY2(updater, qPrintable(mockUpdater.errorString()));
        QVERIFY(window->setProperty("updater", QVariant::fromValue(updater.data())));
        QVERIFY(window->setProperty("developmentToolsEnabled", true));
        QQuickItem *devEntry = nullptr;
        QTRY_VERIFY((devEntry = window->findChild<QQuickItem *>("accountDevMenuEntry")));
        auto avatar = window->findChild<QQuickItem *>("avatarButton");
        auto accountMenu = window->findChild<QObject *>("accountMenu");
        auto settingsEntry = window->findChild<QQuickItem *>("accountSettingsEntry");
        QVERIFY(avatar);
        QVERIFY(accountMenu);
        QVERIFY(settingsEntry);
        QVERIFY(QMetaObject::invokeMethod(avatar, "clicked"));
        QTRY_VERIFY(accountMenu->property("visible").toBool());
        QTRY_VERIFY(devEntry->isVisible());
        QVERIFY(separator->isVisible());
        QCOMPARE(devEntry->property("glyph").toString(), QString("flask-conical"));
        QCOMPARE(accountMenu->property("surfaceColor"), window->property("surface2"));
        QTRY_VERIFY(devEntry->mapToScene(QPointF()).y() < separator->mapToScene(QPointF()).y());
        QTRY_VERIFY(separator->mapToScene(QPointF()).y() < settingsEntry->mapToScene(QPointF()).y());
        QVERIFY(!window->findChild<QQuickItem *>("devToolsButton"));
        auto download = window->findChild<QQuickItem *>("updateDownloadButton");
        QVERIFY(download);
        QVERIFY(!download->isVisible());
        QTRY_VERIFY(accountMenu->property("opened").toBool());
        QTest::mouseClick(window, Qt::LeftButton, Qt::NoModifier,
            devEntry->mapToScene(QPointF(devEntry->width() / 2, devEntry->height() / 2)).toPoint());
        QTRY_VERIFY(!accountMenu->property("visible").toBool());
        auto menu = window->findChild<QObject *>("devToolsDialog");
        QVERIFY(menu);
        QTRY_VERIFY(menu->property("visible").toBool());
        auto force = window->findChild<QQuickItem *>("forceUpdateButton");
        QVERIFY(force);
        auto real = window->findChild<QQuickItem *>("realUpdateButton");
        auto quote = window->findChild<QQuickItem *>("updateExplanationQuote");
        QVERIFY(real);
        QVERIFY(quote);
        QTRY_COMPARE(force->height(), real->height());
        QTRY_COMPARE(force->width(), real->width());
        QCOMPARE(force->height(), force->implicitHeight());
        QTRY_COMPARE(force->mapToScene(QPointF()).y(), real->mapToScene(QPointF()).y());
        auto forceBackground = force->property("background").value<QQuickItem *>();
        auto realBackground = real->property("background").value<QQuickItem *>();
        QVERIFY(forceBackground);
        QVERIFY(realBackground);
        QTRY_COMPARE(forceBackground->height(), realBackground->height());
        QTRY_COMPARE(forceBackground->width(), realBackground->width());
        QTRY_COMPARE(forceBackground->mapToScene(QPointF()).y(), realBackground->mapToScene(QPointF()).y());
        auto card = window->findChild<QQuickItem *>("developerUpdateCard");
        QVERIFY(card);
        QCOMPARE(card->property("color").value<QColor>(), QColor("#171719"));
        QCOMPARE(card->property("radius").toDouble(), 14.0);
        QCOMPARE(card->property("contentPadding").toInt(), 12);
        QCOMPARE(quote->parentItem()->parentItem(), card);
        QCOMPARE(force->parentItem()->parentItem()->parentItem(), card);
        QTRY_VERIFY(force->mapToScene(QPointF()).x() < real->mapToScene(QPointF()).x());
        QVERIFY(quote->mapToScene(QPointF()).y() < force->mapToScene(QPointF()).y());
        QVERIFY(QMetaObject::invokeMethod(force, "clicked"));
        QTRY_VERIFY(download->isVisible());
        QTRY_VERIFY(!menu->property("visible").toBool());
        QVERIFY(download->mapToScene(QPointF()).y() < avatar->mapToScene(QPointF()).y());
        QVERIFY(!window->findChild<QObject *>("updateDialog"));
        auto ring = download->findChild<QQuickItem *>("buttonProgressRing");
        auto spinner = download->findChild<QQuickItem *>("buttonSpinner");
        QVERIFY(ring);
        QVERIFY(spinner);
        QVERIFY(!ring->isVisible());
        QVERIFY(QMetaObject::invokeMethod(download, "clicked"));
        QCOMPARE(updater->property("installs").toInt(), 1);
        QTRY_VERIFY(ring->isVisible());
        QVERIFY(!download->isEnabled());
        QVERIFY(!spinner->isVisible());
        QCOMPARE(ring->property("progress").toDouble(), 0.0);
        QVERIFY(updater->setProperty("progress", 0.5));
        QTRY_COMPARE(ring->property("fraction").toDouble(), 0.5);
        QVERIFY(updater->setProperty("progress", -1.0));
        QTRY_VERIFY(ring->property("indeterminate").toBool());
        QVERIFY(updater->setProperty("progress", 1.0));
        QTRY_COMPARE(ring->property("fraction").toDouble(), 1.0);
        QVERIFY(updater->setProperty("busy", false));
        QTRY_VERIFY(!ring->isVisible());
        QVERIFY(download->isEnabled());
        auto notifications = window->findChild<QAbstractItemModel *>("notificationModel");
        QVERIFY(notifications);
        const int initialNotifications = notifications->rowCount();
        for (const QString status : {"Update available", "Up to date", "Update installed; restarting"}) {
            QVERIFY(updater->setProperty("busy", true));
            QVERIFY(QMetaObject::invokeMethod(updater.data(), "changed"));
            QVERIFY(updater->setProperty("status", status));
            QVERIFY(updater->setProperty("busy", false));
            QVERIFY(QMetaObject::invokeMethod(updater.data(), "changed"));
            QCoreApplication::processEvents();
            QCOMPARE(notifications->rowCount(), initialNotifications);
        }
        QVERIFY(updater->setProperty("busy", true));
        QVERIFY(QMetaObject::invokeMethod(updater.data(), "changed"));
        QVERIFY(updater->setProperty("error", "Update fixture failure"));
        QVERIFY(updater->setProperty("busy", false));
        QVERIFY(QMetaObject::invokeMethod(updater.data(), "changed"));
        QTRY_COMPARE(notifications->rowCount(), initialNotifications + 1);
        QCOMPARE(warnings.count(), 0);
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
        local.attach(engine, config, bridge, musicPlayer);
        QSignalSpy warnings(&engine, &QQmlEngine::warnings);
        engine.load(QUrl("qrc:/Main.qml"));
        QVERIFY(!engine.rootObjects().isEmpty());
        auto window = qobject_cast<QQuickWindow *>(engine.rootObjects().first());
        QVERIFY(window);
        QCOMPARE(window->property("config").value<QObject *>(), &config);
        QCOMPARE(window->property("backendBridge").value<QObject *>(), &bridge);
        QCOMPARE(window->property("player").value<QObject *>(), &musicPlayer);
        QCOMPARE(window->property("workspace").value<QObject *>(), &local.workspace);
        QCOMPARE(window->property("uploader").value<QObject *>(), &local.uploader);
        QCOMPARE(window->property("gitTag").toString(), QStringLiteral("dev"));
        QVERIFY(!window->property("developmentToolsEnabled").toBool());
        QVERIFY(!window->property("updater").value<QObject *>());
        QQuickItem *sidebarSearchIcon = nullptr;
        const auto findSearchIcon = [&](auto &&self, QQuickItem *item) -> void {
            if (item->property("source").toUrl() == QUrl("qrc:/assets/lucide-search.svg"))
                sidebarSearchIcon = item;
            for (auto child : item->childItems()) self(self, child);
        };
        findSearchIcon(findSearchIcon, window->contentItem());
        QVERIFY(sidebarSearchIcon);
        // Item.layer owns the effect component, not the Image's QObject child tree.
        // Instantiate that same component in its creation context to inspect its bindings.
        const QVariant effect = QQmlProperty::read(sidebarSearchIcon, "layer.effect");
        auto tintComponent = qobject_cast<QQmlComponent *>(effect.value<QObject *>());
        QVERIFY(tintComponent);
        QScopedPointer<QObject> searchTint(tintComponent->create(tintComponent->creationContext()));
        QVERIFY2(searchTint, qPrintable(tintComponent->errorString()));
        QCOMPARE(searchTint->property("brightness").toReal(), 1.0);
        QCOMPARE(searchTint->property("colorization").toReal(), 1.0);
        QCOMPARE(window->minimumWidth(), 1200);
        QCOMPARE(window->minimumHeight(), 750);
        window->resize(1200, 800);
        QTest::qWait(100);
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
                QVERIFY(!window->findChild<QObject *>("titleBar"));
                auto settingsEntry = account->findChild<QObject *>("accountSettingsEntry");
                QVERIFY(settingsEntry);
                QVERIFY(QMetaObject::invokeMethod(settingsEntry, "triggered"));
                auto dialog = window->findChild<QObject *>("settingsDialog");
                QVERIFY(dialog); QCOMPARE(dialog->property("visible").toBool(), true);
                QVERIFY(!window->findChild<QObject *>("themeSetting"));
                auto visualizerToggle = window->findChild<QObject *>("visualizerSetting");
                auto visualizer = window->findChild<QQuickItem *>("spectrumVisualizer");
                auto playerPanel = window->findChild<QQuickItem *>("playerPanel");
                QVERIFY(visualizerToggle); QVERIFY(visualizer); QVERIFY(playerPanel);
                QVERIFY(!config.visualizerEnabled());
                QVERIFY(!musicPlayer.visualizerEnabled());
                QVERIFY(!visualizer->isVisible());
                const auto originalBackground = playerPanel->property("color").value<QColor>();
                visualizerToggle->setProperty("checked", true);
                QVERIFY(QMetaObject::invokeMethod(visualizerToggle, "toggled"));
                QTRY_VERIFY(config.visualizerEnabled());
                QTRY_VERIFY(musicPlayer.visualizerEnabled());
                QTRY_VERIFY(visualizer->isVisible());
                QCOMPARE(visualizer->property("accent").value<QColor>(), window->property("accent").value<QColor>());
                QCOMPARE(playerPanel->property("color").value<QColor>(), originalBackground);
                QVariantList testLevels, testPeaks;
                for (int i = 0; i < 75; ++i) { testLevels.append(0.6); testPeaks.append(0.8); }
                QVERIFY(visualizer->setProperty("levels", testLevels));
                QVERIFY(visualizer->setProperty("peaks", testPeaks));
                QTest::qWait(100);
                QVERIFY(warnings.isEmpty());
                config.setVisualizerEnabled(false);
                QTRY_VERIFY(!musicPlayer.visualizerEnabled());
                QTRY_VERIFY(!visualizer->isVisible());
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
                auto approveSound = window->findChild<QObject *>("approveSoundSetting");
                QVERIFY(approveSound);
                approveSound->setProperty("checked", true);
                QVERIFY(QMetaObject::invokeMethod(approveSound, "toggled"));
                QVERIFY(config.approveSoundEnabled());
                QVERIFY(QMetaObject::invokeMethod(dialog, "close"));
        auto track = bridge.tracks()->data(bridge.tracks()->index(1), Qt::UserRole);
        QVERIFY(QMetaObject::invokeMethod(window, "choose", Q_ARG(QVariant, track)));
        QCOMPARE(window->property("selectedId").toString(), QString("102"));

        QCOMPARE(QGuiApplication::clipboard()->text(), QString("102"));
        QVERIFY(QMetaObject::invokeMethod(window, "toggleExpanded", Q_ARG(QVariant, QVariant("102"))));
        QTest::qWait(400);
        QCOMPARE(window->property("bg").value<QColor>(), QColor("#000000"));
        QCOMPARE(window->property("surface").value<QColor>(), QColor("#0c0c0e"));
        QCOMPARE(window->property("surface2").value<QColor>(), QColor("#171719"));
        QCOMPARE(window->property("accent").value<QColor>(), QColor("#2bfef5"));
        QVERIFY(!window->grabWindow().isNull());
                const auto screenshots = qEnvironmentVariable("DDETECTOR_TEST_SCREENSHOT_DIR");
                if (!screenshots.isEmpty()) QVERIFY(window->grabWindow().save(screenshots + "/desktop-expanded-dark.png"));
        QCOMPARE(engine.rootContext()->contextProperty("gitWorkspace").value<QObject *>(),
                 static_cast<QObject *>(&local.workspace));
        QCOMPARE(engine.rootContext()->contextProperty("localUploader").value<QObject *>(),
                 static_cast<QObject *>(&local.uploader));
        QQuickItem *workspaceView = nullptr;
        QQuickItem *lyricsButton = nullptr;
        QQuickItem *gitButton = nullptr;
        QObject *sendDialog = nullptr;
        for (auto object : window->findChildren<QObject *>()) {
            if (object->objectName() == "gitWorkspaceLoader")
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
        auto sorting = window->findChild<QQuickItem *>("sortSelect");
        QVERIFY(sorting);
        auto header = sorting->parentItem()->parentItem();
        auto rightPanel = workspaceView->parentItem();
        auto panels = rightPanel->parentItem();
        const auto headerHeight = header->height();
        const auto sortingY = sorting->mapToItem(header, QPointF()).y();
        QCOMPARE(headerHeight, 48.0);
        // Programmatic widths must not restore tablet/mobile layouts, even when
        // bypassing the production minimum size on the offscreen platform.
        for (const int width : {1440, 950, 700, 600, 480, 390, 320}) {
            window->setWidth(width);
            QCoreApplication::processEvents();
            QCOMPARE(window->minimumWidth(), 1200);
            QCOMPARE(window->minimumHeight(), 750);
            QCOMPARE(header->height(), headerHeight);
            QCOMPARE(sorting->mapToItem(header, QPointF()).y(), sortingY);
            QCOMPARE(rightPanel->y(), 0.0);
            QVERIFY(rightPanel->x() > 0);
            QVERIFY(qAbs(rightPanel->x() + rightPanel->width() - panels->width()) < 1);
            QVERIFY(qAbs(rightPanel->x() - (panels->width() * 0.40 + 1)) < 1);
            QCOMPARE(workspaceView->y(), 0.0);
        }
        window->setWidth(1200);
        QCoreApplication::processEvents();
        QCOMPARE(local.workspace.repositoryPath(), config.repositoryPath());
        QVERIFY(!workspaceView->property("item").value<QObject *>());
        QCOMPARE(window->property("rightPage").toInt(), 0);
        QVERIFY(!workspaceView->isVisible());
        QVERIFY(QMetaObject::invokeMethod(gitButton, "clicked"));
        QCOMPARE(window->property("rightPage").toInt(), 1);
        QTRY_VERIFY(workspaceView->isVisible());
        QTRY_VERIFY(workspaceView->property("item").value<QObject *>());
        auto loadedWorkspace = workspaceView->property("item").value<QObject *>();
        QCOMPARE(loadedWorkspace->property("workspaceBackend").value<QObject *>(),
                 static_cast<QObject *>(&local.workspace));
        QTRY_COMPARE(local.workspace.repositoryPath(), config.repositoryPath());
        // The workspace opens a system terminal explicitly; visiting Git must not spawn one.
        QVERIFY(!loadedWorkspace->findChild<QObject *>("gitTerminalLoader"));
        QVERIFY(!local.workspace.terminalRunning());
        QVERIFY(QMetaObject::invokeMethod(lyricsButton, "clicked"));
        QCOMPARE(window->property("rightPage").toInt(), 0);
        QTRY_VERIFY(!workspaceView->isVisible());
        QCOMPARE(workspaceView->property("item").value<QObject *>(), loadedWorkspace);
        QVERIFY(!local.workspace.terminalRunning());

        QVERIFY(sendDialog);
        QCOMPARE(sendDialog->property("uploadBackend").value<QObject *>(),
                 static_cast<QObject *>(&local.uploader));
        const auto snapshot = track.toMap();
        QVERIFY(!snapshot.isEmpty());
        QVERIFY(QMetaObject::invokeMethod(sendDialog, "openTrack",
                                         Q_ARG(QVariant, snapshot.value("id")),
                                         Q_ARG(QVariant, snapshot.value("title")),
                                         Q_ARG(QVariant, snapshot.value("artist")),
                                         Q_ARG(QVariant, QVariant(QString())),
                                         Q_ARG(QVariant, QVariant(QVariantMap()))));
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
        local.attach(engine, config, bridge, musicPlayer);
        engine.load(QUrl("qrc:/Main.qml"));
        QVERIFY(!engine.rootObjects().isEmpty());
        auto window = engine.rootObjects().first();
        const auto identity = [](const QString &provider, const QString &url) {
            return QVariantMap{{"provider", provider}, {"identity_data", QVariantMap{{"avatar_url", url}}}};
        };
        const QVariantMap metadata{{"avatar_url", "https://example.com/metadata.png"}};
        const QList<QPair<QVariantMap, QString>> cases{
            {{{"user_metadata", metadata}, {"identities", QVariantList{identity("discord", "https://example.com/discord.png"), identity("github", "https://example.com/github.png")}}}, "https://example.com/github.png"},
            {{{"user_metadata", metadata}, {"identities", QVariantList{identity("discord", "https://example.com/discord.png")}}}, "https://example.com/discord.png"},
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
        local.attach(engine, config, bridge, musicPlayer);
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
            QCOMPARE(panel->height(), 88.0);
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
        local.attach(engine, config, bridge, musicPlayer);
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
        local.attach(engine, config, bridge, musicPlayer);
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
        local.attach(engine, config, bridge, musicPlayer);
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
        auto authPanel = title->parentItem();
        const auto horizontalPadding = authPanel->property("horizontalPadding").toReal();
        QCOMPARE(horizontalPadding, 32.0);
        const auto titleFontSize = title->property("font").value<QFont>().pixelSize();
        for (const int width : {700, 480, 390, 320}) {
            window->setWidth(width);
            QCoreApplication::processEvents();
            QVERIFY(artwork->isVisible());
            QCOMPARE(authPanel->property("horizontalPadding").toReal(), horizontalPadding);
            QVERIFY(qAbs(authPanel->x() - (artwork->x() + artwork->width() + horizontalPadding)) < 1);
            QVERIFY(qAbs(artwork->width() - (artwork->parentItem()->width() - 2) * 0.9 / 1.9) < 1);
            QCOMPARE(title->property("font").value<QFont>().pixelSize(), titleFontSize);
        }
        window->setWidth(1200);
        QCoreApplication::processEvents();
        auto discord = visualChild(window->contentItem(), "login-discord");
        QVERIFY(discord); QVERIFY(QMetaObject::invokeMethod(discord, "clicked"));
        bridge.login("github");
        QCOMPARE(bridge.pendingProvider(), QString("discord"));
        QTRY_VERIFY(std::any_of(server.requests.cbegin(), server.requests.cend(),
            [](const QByteArray &req) { return req.startsWith("POST /api/login "); }));
        int logins = 0;
        for (const auto &req : server.requests) if (req.startsWith("POST /api/login ")) {
            ++logins; QVERIFY(req.contains("\"provider\":\"discord\""));
        }
        QCOMPARE(logins, 1);
        QCOMPARE(warnings.count(), 0);
    }
};
int main(int argc, char **argv) {
    QApplication app(argc, argv);
    if (app.arguments().size() == 3 && app.arguments().at(1) == "--signal-flush-child")
        return NativeTests::runSignalFlushChild(app.arguments().at(2));
    NativeTests tests;
    return QTest::qExec(&tests, argc, argv);
}
#include "tests.moc"

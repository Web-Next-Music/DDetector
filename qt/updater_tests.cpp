#include "updater.h"
#include "version.h"
#include <QTest>
#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>
#include <QSignalSpy>
#include <QCryptographicHash>
#include <QDir>
#include <QFile>
#include <QFileInfo>
#include <QProcessEnvironment>
#include <QTemporaryDir>
#include <QNetworkAccessManager>
#include <QNetworkRequest>
#include <cstring>
#include <QNetworkReply>
#include <QProcess>
#include <QTimer>

class MetadataReply final : public QNetworkReply {
public:
    explicit MetadataReply(const QNetworkRequest &request, QObject *parent) : QNetworkReply(parent) {
        setRequest(request); setUrl(request.url()); setOperation(QNetworkAccessManager::GetOperation);
        open(QIODevice::ReadOnly);
    }
    void abort() override { setError(OperationCanceledError, "Cancelled"); complete({}); }
    void complete(const QByteArray &body) {
        m_body = body; setFinished(true); emit readyRead(); emit finished();
    }
    qint64 bytesAvailable() const override { return m_body.size() + QNetworkReply::bytesAvailable(); }
protected:
    qint64 readData(char *data, qint64 maximum) override {
        const qint64 count = qMin(maximum, qint64(m_body.size()));
        if (!count) return -1;
        std::memcpy(data, m_body.constData(), size_t(count)); m_body.remove(0, count); return count;
    }
private:
    QByteArray m_body;
};

class MetadataNetwork final : public QNetworkAccessManager {
public:
    explicit MetadataNetwork(QObject *parent) : QNetworkAccessManager(parent) {}
    int requests = 0;
    MetadataReply *reply = nullptr;
protected:
    QNetworkReply *createRequest(Operation operation, const QNetworkRequest &request, QIODevice *) override {
        Q_ASSERT(operation == GetOperation);
        ++requests; reply = new MetadataReply(request, this); return reply;
    }
};

class UpdaterTests final : public QObject {
    Q_OBJECT
private slots:
    void semanticVersions() {
        const QStringList ordered = {"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta",
            "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0",
            "1.0.1", "1.1.0", "2.0.0", "999999999999999999999999999.0.0"};
        for (int i = 0; i < ordered.size(); ++i) {
            for (int j = 0; j < ordered.size(); ++j) {
                int result = 42;
                QVERIFY(Updater::compareVersions(ordered[i], ordered[j], &result));
                QCOMPARE(result, i < j ? -1 : i > j ? 1 : 0);
            }
        }
        for (const auto &pair : QList<QPair<QString, QString>>{
            {"v1.2.3+build.01", "1.2.3+other"}, {"1.2.3-alpha+one", "v1.2.3-alpha+two"}}) {
            int result = 42;
            QVERIFY(Updater::compareVersions(pair.first, pair.second, &result)); QCOMPARE(result, 0);
        }
        int result = 0;
        QVERIFY(Updater::compareVersions("1.0.0-9", "1.0.0-A", &result)); QCOMPARE(result, -1);
        QVERIFY(Updater::compareVersions("1.0.0-A", "1.0.0-a", &result)); QCOMPARE(result, -1);
    }
    void invalidVersions() {
        for (const QString invalid : {"", "dev", "1", "1.2", "1.2.3.4", "01.2.3", "1.02.3",
            "1.2.03", "1.2.3-01", "1.2.3-a..b", "1.2.3+", "1.2.3-", "1.2.3+foo..bar",
            "1.2.3 a", "V1.2.3", " 1.2.3", "1.2.3\n", "1.2.3-α"}) {
            int result = 0;
            QVERIFY2(!Updater::compareVersions(invalid, "1.2.3", &result), qPrintable(invalid));
            QVERIFY(!Updater::compareVersions("1.2.3", invalid, &result));
        }
    }
    void artifacts() {
        using P = Updater::Platform;
        QCOMPARE(Updater::artifactName(P::Windows, "v1.2.3", "x86_64"), "DDetector-1.2.3-Setup.exe");
        QCOMPARE(Updater::artifactName(P::Debian, "v1.2.3", "x86_64"), "ddetector_1.2.3_amd64.deb");
        QCOMPARE(Updater::artifactName(P::Arch, "v1.2.3", "x86_64"), "ddetector-1.2.3-1-x86_64.pkg.tar.zst");
        QCOMPARE(Updater::artifactName(P::AppImage, "v1.2.3", "x86_64"), "DDetector-1.2.3-x86_64.AppImage");
        for (P platform : {P::Windows, P::Debian, P::Arch, P::AppImage, P::Browser}) {
            QVERIFY(Updater::artifactName(platform, "1.2.3", "arm64").isEmpty());
            QVERIFY(Updater::artifactName(platform, "1.2.3-rc.1", "x86_64").isEmpty());
            QVERIFY(Updater::artifactName(platform, "../../evil", "x86_64").isEmpty());
        }
        QVERIFY(Updater::artifactName(P::Browser, "1.2.3", "x86_64").isEmpty());
    }
    void releaseValidation() {
        Updater updater(nullptr, false);
        QSignalSpy changed(&updater, &Updater::changed);
        QJsonObject release{{"tag_name", "v1.2.4"}, {"draft", false}, {"prerelease", false},
            {"html_url", "https://github.com/Web-Next-Music/DDetector/releases/tag/v1.2.4"}, {"assets", QJsonArray{}}};
        updater.acceptRelease(QJsonDocument(release).toJson());
        QVERIFY(updater.error().isEmpty()); QCOMPARE(updater.version(), "v1.2.4");
        int comparison = 0;
        const bool known = Updater::compareVersions("v1.2.4", QStringLiteral(DDETECTOR_GIT_TAG), &comparison);
        QCOMPARE(updater.available(), known && comparison > 0);
        QVERIFY(!updater.busy()); QCOMPARE(changed.count(), 1);
        release["prerelease"] = true;
        updater.acceptRelease(QJsonDocument(release).toJson()); QVERIFY(!updater.error().isEmpty());
        release["prerelease"] = false; release["html_url"] = "https://evil.invalid/releases/tag/v1.2.4";
        updater.acceptRelease(QJsonDocument(release).toJson()); QVERIFY(!updater.error().isEmpty());
        updater.acceptRelease("not json"); QVERIFY(!updater.error().isEmpty());
    }
    void repositoryUrls() {
        Updater updater(nullptr, false);
        QCOMPARE(updater.m_page.toString(), "https://github.com/Web-Next-Music/DDetector/releases/latest");
        for (const QString repo : {"Web-Next-Music/DDetector", "web-next-music/ddetector", "WEB-NEXT-MUSIC/DDETECTOR"}) {
            QVERIFY(Updater::releaseUrl(QUrl("https://github.com/" + repo + "/releases/tag/v1.2.3"), false));
            QVERIFY(Updater::releaseUrl(QUrl("https://github.com/" + repo + "/releases/download/v1.2.3/DDetector.AppImage"), true));
            Updater mixedCase(nullptr, false);
            const QJsonObject release{{"tag_name", "v1.2.4"}, {"draft", false}, {"prerelease", false},
                {"html_url", "https://github.com/" + repo + "/releases/tag/v1.2.4"}, {"assets", QJsonArray{}}};
            mixedCase.acceptRelease(QJsonDocument(release).toJson());
            QVERIFY(mixedCase.error().isEmpty());
        }
        for (const QString url : {"https://github.com/web-next-music/ddetector-go/releases/tag/v1.2.3",
            "https://github.com/Web-Next-Music/DDetector-other/releases/tag/v1.2.3",
            "http://github.com/Web-Next-Music/DDetector/releases/tag/v1.2.3",
            "https://github.com.evil.invalid/Web-Next-Music/DDetector/releases/tag/v1.2.3",
            "https://user@github.com/Web-Next-Music/DDetector/releases/tag/v1.2.3",
            "https://github.com:444/Web-Next-Music/DDetector/releases/tag/v1.2.3",
            "https://github.com/Web-Next-Music/DDetector/releases/tag/v1.2.3?other=1"}) {
            QVERIFY2(!Updater::releaseUrl(QUrl(url), false), qPrintable(url));
        }
    }
    void restartEnvironment() {
        QProcessEnvironment env;
        env.insert("APPDIR", "/tmp/.mount_ddetector");
        env.insert("APPIMAGE", "/home/user/DDetector.AppImage");
        env.insert("OWD", "/home/user"); env.insert("ARGV0", "old-image");
        env.insert("DDETECTOR_QT_EXECUTABLE", "/tmp/.mount_ddetector/usr/lib/ddetector/ddetector-qt");
        env.insert("PATH", "/tmp/.mount_ddetector/usr/bin:/usr/bin:/bin");
        env.insert("LD_LIBRARY_PATH", "/tmp/.mount_ddetector/usr/lib:/custom/lib");
        env.insert("QT_PLUGIN_PATH", "/tmp/.mount_ddetector/usr/plugins");
        env.insert("QML_IMPORT_PATH", "/tmp/.mount_ddetector/usr/qml");
        env.insert("XDG_DATA_DIRS", "/tmp/.mount_ddetector/usr/share:/usr/share");
        env.insert("HOME", "/home/user"); env.insert("DISPLAY", ":0");
        const auto clean = Updater::restartEnvironment(env);
        QCOMPARE(clean.value("PATH"), "/usr/bin:/bin");
        QCOMPARE(clean.value("LD_LIBRARY_PATH"), "/custom/lib");
        QCOMPARE(clean.value("XDG_DATA_DIRS"), "/usr/share");
        QCOMPARE(clean.value("HOME"), "/home/user"); QCOMPARE(clean.value("DISPLAY"), ":0");
        for (const auto &key : {"APPDIR", "APPIMAGE", "OWD", "ARGV0", "DDETECTOR_QT_EXECUTABLE",
             "QT_PLUGIN_PATH", "QML_IMPORT_PATH"}) QVERIFY(!clean.contains(key));
    }
    void nativeRestartEnvironment() {
        QProcessEnvironment env;
        env.insert("PATH", "/opt/ddetector/usr/bin:/usr/bin:/opt/ddetector-other/bin");
        env.insert("LD_LIBRARY_PATH", "/opt/ddetector/usr/lib");
        env.insert("QT_PLUGIN_PATH", "/opt/ddetector/usr/plugins");
        env.insert("QML_IMPORT_PATH", "/opt/ddetector/usr/qml");
        env.insert("XDG_DATA_DIRS", "/opt/ddetector/usr/share:/usr/share");
        env.insert("HOME", "/home/user");
        const auto clean = Updater::restartEnvironment(env);
        QCOMPARE(clean.value("PATH"), "/usr/bin:/opt/ddetector-other/bin");
        QCOMPARE(clean.value("XDG_DATA_DIRS"), "/usr/share");
        QCOMPARE(clean.value("HOME"), "/home/user");
        for (const auto &key : {"LD_LIBRARY_PATH", "QT_PLUGIN_PATH", "QML_IMPORT_PATH"})
            QVERIFY(!clean.contains(key));
    }
    void launcherArguments() {
        QProcess process;
        for (const auto &path : {QString("/usr/bin/ddetector"), QString("/home/user/My App.AppImage")}) {
            Updater::configureLauncher(process, path);
            QCOMPARE(process.program(), path);
            QVERIFY(process.arguments().isEmpty());
            QCOMPARE(process.workingDirectory(), QFileInfo(path).absolutePath());
            QCOMPARE(process.processEnvironment(), Updater::restartEnvironment(QProcessEnvironment::systemEnvironment()));
        }
        Updater::configureLauncher(process, "/tmp/DDetector Setup.exe", true);
        QCOMPARE(process.arguments(), QStringList{"/RESTARTAPP"});
    }
    void windowsInstallerHandoff() {
        for (bool starts : {false, true}) {
            Updater updater(nullptr, false);
            updater.m_busy = true;
            updater.m_staging = std::make_unique<QTemporaryDir>();
            QVERIFY(updater.m_staging->isValid());
            const QString directory = updater.m_staging->path();
            int launches = 0, quits = 0;
            QString program;
            QStringList arguments;
            updater.m_startDetached = [&](QProcess &process) {
                ++launches; program = process.program(); arguments = process.arguments();
                return starts;
            };
            updater.m_quit = [&] { ++quits; };
            updater.launchInstaller(Updater::Platform::Windows, directory + "/Setup.exe");
            QCOMPARE(launches, 1);
            QCOMPARE(program, directory + "/Setup.exe");
            QCOMPARE(arguments, QStringList{"/RESTARTAPP"});
            QCOMPARE(quits, starts ? 1 : 0);
            QCOMPARE(updater.error().isEmpty(), starts);
            if (starts) {
                QVERIFY(!updater.m_staging->autoRemove());
                updater.m_staging->setAutoRemove(true);
            }
        }
    }
    void appImageRestartFailure() {
        Updater updater(nullptr, false);
        updater.m_busy = true;
        int quits = 0;
        QString program;
        QStringList arguments;
        updater.m_startDetached = [&](QProcess &process) {
            program = process.program(); arguments = process.arguments(); return false;
        };
        updater.m_quit = [&] { ++quits; };
        updater.restartLauncher("/home/user/My App.AppImage");
        QCOMPARE(program, QString("/home/user/My App.AppImage"));
        QVERIFY(arguments.isEmpty());
        QCOMPARE(quits, 0);
        QVERIFY(!updater.busy());
        QVERIFY(updater.error().contains("manually"));
    }
    void packageRestartOutcome() {
        for (const auto &outcome : {QPair<int, bool>{1, true}, {126, true}, {0, false}, {0, true}}) {
            for (bool starts : {false, true}) {
                Updater updater(nullptr, false);
                updater.m_busy = true; updater.m_available = true;
                int launches = 0, quits = 0;
                updater.m_startDetached = [&](QProcess &process) {
                    ++launches;
                    if (process.program() != "/usr/bin/ddetector" || !process.arguments().isEmpty())
                        return false;
                    return starts;
                };
                updater.m_quit = [&] { ++quits; };
                updater.packageFinished(outcome.first, outcome.second);
                const bool installed = outcome.first == 0 && outcome.second;
                QCOMPARE(launches, installed ? 1 : 0);
                QCOMPARE(quits, installed && starts ? 1 : 0);
                QVERIFY(!updater.busy());
                QCOMPARE(updater.available(), !installed);
                QCOMPARE(updater.error().isEmpty(), installed && starts);
                if (installed && !starts) QVERIFY(updater.error().contains("manually"));
            }
        }
    }
    void appImageAtomicReplacement() {
#ifndef Q_OS_LINUX
        QSKIP("AppImage replacement is Linux-only");
#else
        QTemporaryDir dir; QVERIFY(dir.isValid());
        const QString target = dir.filePath("DDetector with spaces.AppImage");
        const QString source = dir.filePath("download");
        const QByteArray oldBytes("old running image"), newBytes(200000, 'n');
        QFile old(target); QVERIFY(old.open(QIODevice::WriteOnly)); QCOMPARE(old.write(oldBytes), oldBytes.size()); old.close();
        const auto permissions = QFileDevice::ReadOwner | QFileDevice::WriteOwner | QFileDevice::ExeOwner
            | QFileDevice::ReadGroup | QFileDevice::ExeGroup;
        QVERIFY(QFile::setPermissions(target, permissions));
        const auto originalPermissions = QFileInfo(target).permissions();
        QFile input(source); QVERIFY(input.open(QIODevice::WriteOnly)); QCOMPARE(input.write(newBytes), newBytes.size()); input.close();
        QVERIFY(Updater::appImagePathError(target).isEmpty());
        // An already-open image keeps the old inode after atomic replacement.
        QVERIFY(old.open(QIODevice::ReadOnly));
        QString error;
        const auto digest = QCryptographicHash::hash(newBytes, QCryptographicHash::Sha256).toHex();
        QVERIFY2(Updater::replaceAppImage(target, source, digest, &error), qPrintable(error));
        QVERIFY(error.isEmpty()); QCOMPARE(old.readAll(), oldBytes); old.close();
        QFile updated(target); QVERIFY(updated.open(QIODevice::ReadOnly)); QCOMPARE(updated.readAll(), newBytes); updated.close();
        QCOMPARE(QFileInfo(target).permissions(), originalPermissions);
        QVERIFY(!Updater::replaceAppImage(target, source, QByteArray(64, '0'), &error));
        QVERIFY(error.contains("SHA-256"));
        QVERIFY(updated.open(QIODevice::ReadOnly)); QCOMPARE(updated.readAll(), newBytes); updated.close();
        QVERIFY(!Updater::replaceAppImage(target, dir.filePath("missing"), digest, &error));
        QVERIFY(updated.open(QIODevice::ReadOnly)); QCOMPARE(updated.readAll(), newBytes); updated.close();
        QCOMPARE(QDir(dir.path()).entryList(QDir::Files).size(), 2);
#endif
    }
    void appImageUnsafePaths() {
#ifndef Q_OS_LINUX
        QSKIP("AppImage path validation is Linux-only");
#else
        QTemporaryDir dir; QVERIFY(dir.isValid());
        const QString target = dir.filePath("DDetector.AppImage");
        QFile file(target); QVERIFY(file.open(QIODevice::WriteOnly)); QCOMPARE(file.write("old"), qint64(3)); file.close();
        QVERIFY(QFile::setPermissions(target, QFileDevice::ReadOwner | QFileDevice::WriteOwner | QFileDevice::ExeOwner));
        QVERIFY(Updater::appImagePathError(target).isEmpty());
        const QString link = dir.filePath("link.AppImage"); QVERIFY(QFile::link(target, link));
        for (const auto &path : {QString(), QString("relative.AppImage"), link, dir.path(),
             dir.filePath("missing"), dir.path() + "/../" + QFileInfo(dir.path()).fileName() + "/DDetector.AppImage"}) {
            QVERIFY2(!Updater::appImagePathError(path).isEmpty(), qPrintable(path));
        }
        QString error;
        const QByteArray digest = QCryptographicHash::hash("old", QCryptographicHash::Sha256).toHex();
        QVERIFY(!Updater::replaceAppImage(link, target, digest, &error));
        QVERIFY(QFileInfo(link).isSymLink());
        QVERIFY(file.open(QIODevice::ReadOnly)); QCOMPARE(file.readAll(), QByteArray("old")); file.close();
        QVERIFY(QFile::setPermissions(target, QFileDevice::ReadOwner | QFileDevice::ExeOwner));
        QVERIFY(!Updater::appImagePathError(target).isEmpty());
        QVERIFY(QFile::setPermissions(target, QFileDevice::ReadOwner | QFileDevice::WriteOwner));
        QVERIFY(!Updater::appImagePathError(target).isEmpty());
        QVERIFY(QFile::setPermissions(target, QFileDevice::ReadOwner | QFileDevice::WriteOwner | QFileDevice::ExeOwner));
        const auto directoryPermissions = QFileInfo(dir.path()).permissions();
        QVERIFY(QFile::setPermissions(dir.path(), directoryPermissions | QFileDevice::WriteOther));
        QVERIFY(!Updater::appImagePathError(target).isEmpty());
        QVERIFY(QFile::setPermissions(dir.path(), QFileDevice::ReadOwner | QFileDevice::ExeOwner));
        QVERIFY(!Updater::appImagePathError(target).isEmpty());
        QVERIFY(QFile::setPermissions(dir.path(), directoryPermissions));
        QVERIFY(Updater::appImagePathError(target).isEmpty());
        QTemporaryDir links; QVERIFY(links.isValid());
        const QString directoryLink = links.filePath("linked-directory");
        QVERIFY(QFile::link(dir.path(), directoryLink));
        QVERIFY(!Updater::appImagePathError(directoryLink + "/DDetector.AppImage").isEmpty());
#endif
    }
    void forcedMetadata() {
        Updater updater(nullptr, false);
        for (const QString tag : {QString("v0.0.0"), QStringLiteral(DDETECTOR_GIT_TAG), QString("v1.2.4")}) {
            int comparison = 0;
            if (!Updater::compareVersions(tag, tag, &comparison)) continue;
            const QJsonObject release{{"tag_name", tag}, {"draft", false}, {"prerelease", false},
                {"html_url", "https://github.com/Web-Next-Music/DDetector/releases/tag/" + tag}, {"assets", QJsonArray{}}};
            const auto json = QJsonDocument(release).toJson();
            updater.acceptRelease(json, true);
            QVERIFY(updater.available()); QVERIFY(updater.m_forced); QVERIFY(updater.error().isEmpty());
            updater.acceptRelease(json);
            const bool known = Updater::compareVersions(tag, QStringLiteral(DDETECTOR_GIT_TAG), &comparison);
            QCOMPARE(updater.available(), known && comparison > 0); QVERIFY(!updater.m_forced);
        }
        QJsonObject release{{"tag_name", "v1.2.4"}, {"draft", false}, {"prerelease", false},
            {"html_url", "https://github.com/Web-Next-Music/DDetector/releases/tag/v1.2.4"}, {"assets", QJsonArray{}}};
        for (const auto &field : {QString("draft"), QString("prerelease"), QString("tag_name"), QString("html_url"), QString("assets")}) {
            auto invalid = release;
            if (field == "draft" || field == "prerelease") invalid[field] = true;
            else invalid[field] = "invalid";
            updater.acceptRelease(QJsonDocument(invalid).toJson(), true);
            QVERIFY(!updater.available()); QVERIFY(!updater.m_forced); QVERIFY(!updater.error().isEmpty());
        }
        release["tag_name"] = "v1.2.4-rc.1";
        updater.acceptRelease(QJsonDocument(release).toJson(), true);
        QVERIFY(!updater.available()); QVERIFY(!updater.m_forced); QVERIFY(!updater.error().isEmpty());
        updater.acceptRelease("not json", true);
        QVERIFY(!updater.available()); QVERIFY(!updater.error().isEmpty());
    }
    void periodicChecks() {
        Updater disabled(nullptr, false);
        QVERIFY(!disabled.findChild<QTimer *>("updateHeartbeat"));
        Updater updater(nullptr, true);
        delete updater.m_network;
        auto network = new MetadataNetwork(&updater);
        updater.m_network = network;
        auto heartbeat = updater.findChild<QTimer *>("updateHeartbeat");
        QVERIFY(heartbeat);
        QVERIFY(heartbeat->isActive());
        QVERIFY(!heartbeat->isSingleShot());
        QCOMPARE(heartbeat->interval(), 30 * 60 * 1000);
        QTRY_COMPARE(network->requests, 1);
        QVERIFY(updater.busy());
        QVERIFY(QMetaObject::invokeMethod(heartbeat, "timeout"));
        QCOMPARE(network->requests, 1);
        const QJsonObject release{{"tag_name", "v1.2.4"}, {"draft", false}, {"prerelease", false},
            {"html_url", "https://github.com/Web-Next-Music/DDetector/releases/tag/v1.2.4"}, {"assets", QJsonArray{}}};
        network->reply->complete(QJsonDocument(release).toJson());
        QVERIFY(!updater.busy());
        QVERIFY(QMetaObject::invokeMethod(heartbeat, "timeout"));
        QCOMPARE(network->requests, 2);
        QVERIFY(updater.busy());
        network->reply->complete(QJsonDocument(release).toJson());
        QVERIFY(!updater.busy());
        QVERIFY(heartbeat->isActive());
    }
    void startupDisabledAndRealCheck() {
        Updater updater(nullptr, false);
        QTest::qWait(20); QVERIFY(!updater.busy()); QVERIFY(!updater.available());
        updater.install(); QVERIFY(updater.status().isEmpty());
        delete updater.m_network;
        auto network = new MetadataNetwork(&updater); updater.m_network = network;
#ifdef DDETECTOR_DEV_TOOLS
        updater.forceAvailable();
#else
        QVERIFY(updater.metaObject()->indexOfMethod("forceAvailable()") < 0);
        updater.checkRelease(true);
#endif
        QVERIFY(updater.busy()); QVERIFY(!updater.available()); QCOMPARE(network->requests, 1);
        QCOMPARE(network->reply->url(), QUrl("https://api.github.com/repos/Web-Next-Music/DDetector/releases/latest"));
        QSignalSpy changed(&updater, &Updater::changed);
        updater.install(); updater.check(); updater.checkRelease(true);
#ifdef DDETECTOR_DEV_TOOLS
        updater.forceAvailable();
#endif
        QCOMPARE(network->requests, 1); QCOMPARE(changed.count(), 0);
        const QJsonObject release{{"tag_name", "v0.0.0"}, {"draft", false}, {"prerelease", false},
            {"html_url", "https://github.com/Web-Next-Music/DDetector/releases/tag/v0.0.0"}, {"assets", QJsonArray{}}};
        network->reply->complete(QJsonDocument(release).toJson());
        QVERIFY(!updater.busy()); QVERIFY(updater.available()); QVERIFY(updater.m_forced);
        QVERIFY(!updater.m_staging); QVERIFY(updater.findChildren<QProcess *>().isEmpty());
        updater.check(); QVERIFY(!updater.m_forced); QVERIFY(!updater.available());
        QCOMPARE(network->requests, 2);
        network->reply->complete(QJsonDocument(release).toJson());
        int comparison = 0;
        const bool known = Updater::compareVersions("v0.0.0", QStringLiteral(DDETECTOR_GIT_TAG), &comparison);
        QCOMPARE(updater.available(), known && comparison > 0); QVERIFY(!updater.m_forced);
    }

};

QTEST_GUILESS_MAIN(UpdaterTests)
#include "updater_tests.moc"

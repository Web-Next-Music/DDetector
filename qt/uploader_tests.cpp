#include "uploader.h"
#include <QTest>
#include <QTemporaryDir>
#include <QFile>
#include <QProcess>
#include <QSignalSpy>
#include <QTcpServer>
#include <QTcpSocket>

class UploaderTests final : public QObject {
    Q_OBJECT
    static QByteArray git(const QString &root, const QStringList &args) {
        QProcess p;
        p.setWorkingDirectory(root);
        p.start("git", QStringList{"--no-pager"} + args);
        if (!p.waitForFinished(10000) || p.exitCode() != 0) qFatal("Fixture Git failed: %s", p.readAllStandardError().constData());
        return p.readAllStandardOutput();
    }
    static void put(const QString &path, const QByteArray &bytes) {
        QFile f(path); if (!f.open(QIODevice::WriteOnly) || f.write(bytes) != bytes.size()) qFatal("Fixture write failed");
    }
    static QByteArray get(const QString &path) { QFile f(path); if (!f.open(QIODevice::ReadOnly)) return {}; return f.readAll(); }
    static void init(const QString &root) {
        git(root, {"init", "-q"});
        git(root, {"config", "user.name", "Uploader Test"});
        git(root, {"config", "user.email", "uploader@example.invalid"});
        put(root + "/README.md", "# Tracks\n");
        git(root, {"add", "--", "README.md"});
        git(root, {"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false", "commit", "-qm", "initial"});
    }
private slots:
    void addSkipUpdateAndUnrelatedIndex() {
        QTemporaryDir dir; QVERIFY(dir.isValid()); init(dir.path());
        put(dir.path() + "/unrelated", "staged user work\n");
        git(dir.path(), {"add", "--", "unrelated"});
        const auto index = git(dir.path(), {"diff", "--cached", "--binary", "--", "unrelated"});
        Uploader uploader(dir.path()); QSignalSpy done(&uploader, &Uploader::completed);
        const QString title = "quoted \" $(touch SHOULD_NOT_EXIST) `echo bad`";
        uploader.send("123", title, " A, , B ");
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QCOMPARE(uploader.error(), QString()); QCOMPARE(uploader.action(), "added"); QCOMPARE(done.count(), 1);
        QCOMPARE(git(dir.path(), {"log", "-1", "--format=%s"}).trimmed(), ("add track «" + title + " - A, B»").toUtf8());
        QVERIFY(!QFile::exists(dir.path() + "/SHOULD_NOT_EXIST"));
        QCOMPARE(git(dir.path(), {"diff", "--cached", "--binary", "--", "unrelated"}), index);
        const auto head = git(dir.path(), {"rev-parse", "HEAD"});
        const auto readme = get(dir.path() + "/README.md");
        uploader.send("123", "changed metadata", "C");
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QCOMPARE(uploader.error(), QString()); QCOMPARE(uploader.action(), "skipped");
        QCOMPARE(git(dir.path(), {"rev-parse", "HEAD"}), head); QCOMPARE(get(dir.path() + "/README.md"), readme);
        put(dir.path() + "/list.json", "{\n    \"tracks\": {\n        \"123\": \"old\"\n    }\n}\n");
        git(dir.path(), {"add", "--", "list.json"});

        git(dir.path(), {"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false", "commit", "--only", "-qm", "old URL", "--", "list.json"});
        uploader.send("123", "Updated", "C");
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QCOMPARE(uploader.error(), QString()); QCOMPARE(uploader.action(), "updated");
        QCOMPARE(git(dir.path(), {"diff", "--cached", "--binary", "--", "unrelated"}), index);
    }
    void formattingPreservation_data() {
        QTest::addColumn<QByteArray>("eol");
        QTest::addColumn<QByteArray>("bom");
        for (const auto &eol : {QByteArray("\r\n"), QByteArray("\n"), QByteArray("\r")}) {
            for (bool bom : {false, true}) {
                const auto name = eol.toHex() + (bom ? "-bom" : "-plain");
                QTest::newRow(name.constData()) << eol << (bom ? QByteArray::fromHex("efbbbf") : QByteArray());
            }
        }
    }
    void formattingPreservation() {
        QFETCH(QByteArray, eol); QFETCH(QByteArray, bom);
        QTemporaryDir dir; init(dir.path());
        git(dir.path(), {"config", "core.autocrlf", "false"});
        const QByteArray prefix = bom + "{" + eol + "    \"tracks\": {" + eol + "        \"1\": \"old\"";
        const QByteArray suffix = " \t" + eol + "    }" + eol + "}" + eol + " \t";
        const QByteArray readme = bom + "# Tracks  " + eol + eol + "trailing spaces \t";
        put(dir.path() + "/list.json", prefix + suffix);
        put(dir.path() + "/README.md", readme);
        git(dir.path(), {"add", "--", "list.json", "README.md"});
        git(dir.path(), {"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false", "commit", "-qm", "format fixture"});
        Uploader uploader(dir.path()); uploader.send("2", "Song", "Artist");
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QCOMPARE(uploader.error(), QString());
        const QByteArray entry = "        \"2\": \"https://raw.githubusercontent.com/Hazzz895/FckCensorData/refs/heads/main/tracks/2\"";
        QCOMPARE(get(dir.path() + "/list.json"), prefix + ", \t" + eol + entry + suffix.mid(2));
        QCOMPARE(get(dir.path() + "/README.md"), readme + eol + "- [Song - Artist](https://music.yandex.ru/track/2)" + eol);
        QVERIFY(get(dir.path() + "/README.md").startsWith(readme));
        QVERIFY(get(dir.path() + "/list.json").startsWith(prefix));
        uploader.send("1", "Updated", "Artist");
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QCOMPARE(uploader.error(), QString()); QCOMPARE(uploader.action(), "updated");
        QVERIFY(get(dir.path() + "/list.json").startsWith(bom + "{" + eol));
    }
    void invalidUtf8_data() {
        QTest::addColumn<QString>("target");
        QTest::addColumn<QByteArray>("invalid");
        for (const auto &target : {QString("list.json"), QString("README.md")}) {
            for (const auto &invalid : {QByteArray::fromHex("ff"), QByteArray::fromHex("c080"),
                                        QByteArray::fromHex("eda080"), QByteArray::fromHex("e282")}) {
                QTest::newRow((target.toUtf8() + invalid.toHex()).constData()) << target << invalid;
            }
        }
    }
    void invalidUtf8() {
        QFETCH(QString, target); QFETCH(QByteArray, invalid);
        QTemporaryDir dir; init(dir.path());
        const QByteArray list = "{\n    \"tracks\": {\n        \"1\": \"old" + (target == "list.json" ? invalid : QByteArray()) + "\"\n    }\n}\n";
        const QByteArray readme = "# Tracks\n" + (target == "README.md" ? invalid : QByteArray());
        put(dir.path() + "/list.json", list); put(dir.path() + "/README.md", readme);
        git(dir.path(), {"add", "--", "list.json", "README.md"});
        git(dir.path(), {"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false", "commit", "-qm", "invalid fixture"});
        const auto head = git(dir.path(), {"rev-parse", "HEAD"});
        Uploader uploader(dir.path()); uploader.send("2", "Song", "Artist");
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QVERIFY2(uploader.error().contains("Invalid UTF-8"), qPrintable(uploader.error()));
        QCOMPARE(get(dir.path() + "/list.json"), list); QCOMPARE(get(dir.path() + "/README.md"), readme);
        QCOMPARE(git(dir.path(), {"rev-parse", "HEAD"}), head);
        QVERIFY(git(dir.path(), {"status", "--porcelain"}).isEmpty());
    }
    void invalidAndDirtyTargets() {
        QTemporaryDir dir; init(dir.path()); Uploader uploader(dir.path());
        uploader.send("../escape", "x", "y");
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000); QVERIFY(!uploader.error().isEmpty());
        QVERIFY(!QFile::exists(dir.path() + "/list.json"));
        uploader.send("12\n", "x", "y");
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000); QVERIFY(uploader.error().contains("ASCII digits"));
        put(dir.path() + "/README.md", "user edits");
        uploader.send("12", "x", "y");
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000); QVERIFY(uploader.error().contains("existing changes"));
        QCOMPARE(get(dir.path() + "/README.md"), QByteArray("user edits"));
    }
    void symlinkAndCommitFailure() {
        QTemporaryDir dir; init(dir.path()); Uploader uploader(dir.path());
        QTemporaryDir outside;
        put(outside.path() + "/list", "do not touch");
        QVERIFY(QFile::link(outside.path() + "/list", dir.path() + "/list.json"));
        uploader.send("12", "x", "y");
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QVERIFY(uploader.error().contains("symlink"));
        QCOMPARE(get(outside.path() + "/list"), QByteArray("do not touch"));
        QVERIFY(QFile::remove(dir.path() + "/list.json"));
        git(dir.path(), {"config", "user.name", ""});
        const auto head = git(dir.path(), {"rev-parse", "HEAD"});
        QSignalSpy done(&uploader, &Uploader::completed);
        uploader.send("12", "x", "y");
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QVERIFY2(uploader.error().contains("Git failed"), qPrintable(uploader.error())); QCOMPARE(done.count(), 0);
        QCOMPARE(git(dir.path(), {"rev-parse", "HEAD"}), head);
        QVERIFY(QFile::exists(dir.path() + "/list.json"));
    }
    void audioOverwriteSkippedDoesNotCommit() {
        QTemporaryDir dir; init(dir.path()); Uploader uploader(dir.path());
        uploader.send("42", "Song", "Artist");
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000); QCOMPARE(uploader.error(), QString());
        const auto head = git(dir.path(), {"rev-parse", "HEAD"});
        QTcpServer server; QVERIFY(server.listen(QHostAddress::LocalHost));
        connect(&server, &QTcpServer::newConnection, this, [&] {
            auto socket = server.nextPendingConnection();
            connect(socket, &QTcpSocket::readyRead, socket, [socket] {
                socket->readAll();
                socket->write("HTTP/1.1 200 OK\r\nContent-Length: 3\r\nConnection: close\r\n\r\nmp3");
                socket->disconnectFromHost();
            });
            connect(socket, &QTcpSocket::disconnected, socket, &QObject::deleteLater);
        });
        uploader.send("42", "Song", "Artist", "http://127.0.0.1:" + QString::number(server.serverPort()) + "/audio");
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 35000);
        QCOMPARE(uploader.error(), QString()); QCOMPARE(uploader.action(), "skipped");
        QCOMPARE(get(dir.path() + "/tracks/42"), QByteArray("mp3"));
        QCOMPARE(git(dir.path(), {"rev-parse", "HEAD"}), head);
        QVERIFY(git(dir.path(), {"diff", "--cached", "--name-only"}).isEmpty());
        git(dir.path(), {"add", "--", "tracks/42"});
        git(dir.path(), {"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false", "commit", "--only", "-qm", "fixture audio", "--", "tracks/42"});
        const auto audioHead = git(dir.path(), {"rev-parse", "HEAD"});
        uploader.send("42", "Song", "Artist", "http://127.0.0.1:" + QString::number(server.serverPort()) + "/audio");
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 35000);
        QCOMPARE(uploader.error(), QString()); QVERIFY(uploader.status().contains("overwritten"));
        QCOMPARE(git(dir.path(), {"rev-parse", "HEAD"}), audioHead);
        uploader.send("43", "Second", "Artist", "http://127.0.0.1:" + QString::number(server.serverPort()) + "/audio");
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 35000);
        QCOMPARE(uploader.error(), QString()); QCOMPARE(uploader.action(), "added");
        QCOMPARE(git(dir.path(), {"show", "HEAD:tracks/43"}), QByteArray("mp3"));
        uploader.send("44", "Third", "Artist", "file:///etc/passwd");
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QVERIFY(!uploader.error().isEmpty()); QVERIFY(!QFile::exists(dir.path() + "/tracks/44"));
    }
};
QTEST_GUILESS_MAIN(UploaderTests)
#include "uploader_tests.moc"

#include "uploader.h"
#include <QTest>
#include <QTemporaryDir>
#include <QFile>
#include <QProcess>
#include <QSignalSpy>
#include <QTcpServer>
#include <QTcpSocket>
#include <QPointer>
#include <QDir>
#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>
#ifdef UPLOADER_QML_INTEGRATION
#include <QQmlEngine>
#include <QQmlComponent>
#include <QQuickWindow>
#include <QQuickItem>
#endif

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
    static QByteArray tinyMp3() {
        // Non-seekable MP3 output includes padding: six 1152-sample frames = 144 ms.
        static const QByteArray bytes = [] {
            QProcess p;
            p.start("ffmpeg", {"-nostdin", "-v", "error", "-f", "lavfi", "-i",
                               "anullsrc=r=48000:cl=mono", "-t", "0.12", "-c:a", "libmp3lame", "-f", "mp3", "pipe:1"});
            if (!p.waitForFinished(10000) || p.exitCode() != 0) qFatal("Cannot generate MP3 fixture");
            return p.readAllStandardOutput();
        }();
        return bytes;
    }
    static void localAudio(const QString &root, const QString &id) {
        if (!QDir().mkpath(root + "/tracks")) qFatal("Cannot create fixture tracks");
        put(root + "/tracks/" + id, tinyMp3());
        git(root, {"add", "--", "tracks/" + id});
        git(root, {"commit", "-qm", "fixture audio"});
    }
    static QByteArray defaultReadme() {
        return QString("# Tracks\n## Автоматически подменяемые треки\n<table><tbody>\n</tbody></table>\n").toUtf8();
    }
    static void init(const QString &root) {
        git(root, {"init", "-q"});
        git(root, {"config", "user.name", "Uploader Test"});
        git(root, {"config", "user.email", "uploader@example.invalid"});
        put(root + "/README.md", defaultReadme());
        git(root, {"add", "--", "README.md"});
        git(root, {"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false", "commit", "-qm", "initial"});
    }
private slots:
#ifdef UPLOADER_QML_INTEGRATION
    void sendDialogPersistsArtwork_data() {
        QTest::addColumn<bool>("restore");
        QTest::addColumn<bool>("preview");
        QTest::addColumn<bool>("noCover");
        QTest::newRow("restore-async") << true << false << false;
        QTest::newRow("restore-preview") << true << true << false;
        QTest::newRow("send-preview") << false << true << false;
        QTest::newRow("restore-no-cover") << true << false << true;
    }
    void sendDialogPersistsArtwork() {
        QFETCH(bool, restore);
        QFETCH(bool, preview);
        QFETCH(bool, noCover);
        QTemporaryDir dir; init(dir.path());
        localAudio(dir.path(), "36045842");
        Uploader uploader(dir.path());
        QQmlEngine engine;
        QQmlComponent component(&engine, QUrl("qrc:/SendDialog.qml"));
        QTRY_VERIFY_WITH_TIMEOUT(component.status() != QQmlComponent::Loading, 5000);
        QVERIFY2(component.isReady(), qPrintable(component.errorString()));
        QQuickWindow window;
        QScopedPointer<QObject> dialog(component.createWithInitialProperties({{"uploadBackend", QVariant::fromValue(&uploader)}}));
        QVERIFY2(dialog, qPrintable(component.errorString()));
        dialog->setProperty("parent", QVariant::fromValue(window.contentItem()));
        window.show();
        const QString cover = "https://i.scdn.co/image/fixture";
        const QVariantMap original{{"artists", QVariantList{QVariantMap{{"id", 4622996}, {"name", QString::fromUtf8("Овсянкин")}}}}};
        const QString source = noCover ? QString() : preview ? "image://cover/" + QString::fromLatin1(QUrl::toPercentEncoding(cover)) : cover;
        QTcpServer server; QVERIFY(server.listen(QHostAddress::LocalHost));
        connect(&server, &QTcpServer::newConnection, this, [&] {
            auto socket = server.nextPendingConnection();
            connect(socket, &QTcpSocket::readyRead, socket, [socket] {
                socket->readAll();
                const auto body = tinyMp3();
                socket->write("HTTP/1.1 200 OK\r\nContent-Length: " + QByteArray::number(body.size()) + "\r\nConnection: close\r\n\r\n" + body);
                socket->disconnectFromHost();
            });
            connect(socket, &QTcpSocket::disconnected, socket, &QObject::deleteLater);
        });
        const QString audioUrl = "http://127.0.0.1:" + QString::number(server.serverPort()) + "/audio";
        auto invoke = [&](const char *method) {
            if (restore) return QMetaObject::invokeMethod(dialog.data(), method,
                Q_ARG(QVariant, "36045842"), Q_ARG(QVariant, QString::fromUtf8("Отсиди")),
                Q_ARG(QVariant, QString::fromUtf8("Овсянкин")), Q_ARG(QVariant, source),
                Q_ARG(QVariant, original), Q_ARG(QVariant, audioUrl));
            return QMetaObject::invokeMethod(dialog.data(), method,
                Q_ARG(QVariant, "36045842"), Q_ARG(QVariant, QString::fromUtf8("Отсиди")),
                Q_ARG(QVariant, QString::fromUtf8("Овсянкин")), Q_ARG(QVariant, source), Q_ARG(QVariant, original));
        };
        QVERIFY(invoke(restore ? "openRestore" : "openTrack"));
        if (restore) {
            auto url = dialog->findChild<QObject *>("sendUrl"); QVERIFY(url);
            url->setProperty("text", audioUrl);
        }
        QVERIFY(!dialog->findChild<QObject *>("editMetadata")->property("checked").toBool());
        QVERIFY(dialog->property("canSend").toBool());
        auto button = dialog->findChild<QObject *>("sendTrackButton"); QVERIFY(button);
        QVERIFY(QMetaObject::invokeMethod(button, "clicked"));
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QCOMPARE(uploader.error(), QString());
        const auto sources = QJsonDocument::fromJson(get(dir.path() + "/list_v2.json")).object()["sources"].toArray();
        QVERIFY(!sources.isEmpty());
        const auto saved = sources[0].toObject()["tracks"].toObject()["36045842"].toObject()["coverUri"].toString();
        QCOMPARE(saved, noCover ? QString() : cover.mid(8));
        QCOMPARE(get(dir.path() + "/README.md").contains(cover.toUtf8()), !noCover);
    }
#endif
    void restoreInsertion_data() {
        QTest::addColumn<bool>("restore");
        QTest::addColumn<QByteArray>("insertion");
        QTest::addColumn<QVariant>("artistId");
        QTest::newRow("restore-null-numeric-id") << true << QByteArray("\"insertionIndex\":null,") << QVariant(4622996);
        QTest::newRow("restore-absent-string-id") << true << QByteArray() << QVariant("4622996");
        QTest::newRow("restore-edited-id") << true << QByteArray() << QVariant("4622997");
        QTest::newRow("restore-numeric-insertion") << true << QByteArray("\"insertionIndex\":-5,") << QVariant(4622996.0);
        QTest::newRow("ordinary-send-null") << false << QByteArray("\"insertionIndex\":null,") << QVariant("4622996");
        QTest::newRow("ordinary-send-absent") << false << QByteArray() << QVariant("4622996");
        QTest::newRow("ordinary-send-numeric") << false << QByteArray("\"insertionIndex\":-5,") << QVariant("4622996");
    }
    void restoreInsertion() {
        QFETCH(bool, restore);
        QFETCH(QByteArray, insertion);
        QFETCH(QVariant, artistId);
        QTemporaryDir dir; init(dir.path());
        put(dir.path() + "/list_v2.json", QByteArray(R"({"sources":[{"supportedVersions":">=2.0.0","tracks":{"36045842":{"__fckCensor":{)")
            + insertion + R"("keep":true}}}}]})");
        git(dir.path(), {"add", "."}); git(dir.path(), {"commit", "-qm", "metadata fixture"});
        QTcpServer server; QVERIFY(server.listen(QHostAddress::LocalHost));
        const auto body = tinyMp3();
        connect(&server, &QTcpServer::newConnection, this, [&] {
            auto socket = server.nextPendingConnection();
            connect(socket, &QTcpSocket::readyRead, socket, [socket, body] {
                socket->readAll();
                socket->write("HTTP/1.1 200 OK\r\nContent-Length: " + QByteArray::number(body.size()) + "\r\nConnection: close\r\n\r\n" + body);
                socket->disconnectFromHost();
            });
            connect(socket, &QTcpSocket::disconnected, socket, &QObject::deleteLater);
        });
        // Restore's projected API baseline (metadata editing off), matching the reported track.
        QVariantMap patch{{"title", QString::fromUtf8("Отсиди")},
            {"artists", QVariantList{
                QVariantMap{{"id", artistId}, {"name", QString::fromUtf8("Овсянкин")}},
                QVariantMap{{"id", "77"}, {"name", "Second"}}}}};
        Uploader uploader(dir.path());
        uploader.send("36045842", "Fixture", "Fixture artist",
            "http://127.0.0.1:" + QString::number(server.serverPort()) + "/audio", patch, {}, restore);
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QCOMPARE(uploader.error(), QString());
        const auto source = QJsonDocument::fromJson(get(dir.path() + "/list_v2.json")).object()["sources"].toArray()[0].toObject();
        const auto track = source["tracks"].toObject()["36045842"].toObject();
        QCOMPARE(track["title"].toString(), QString::fromUtf8("Отсиди"));
        const auto options = track["__fckCensor"].toObject();
        QCOMPARE(options["keep"].toBool(), true);
        const bool numeric = insertion.contains("-5");
                QCOMPARE(options.contains("insertionIndex"), !restore || numeric);
                if (numeric) QCOMPARE(options["insertionIndex"].toInt(), -5);
                else if (!restore) QVERIFY(options["insertionIndex"].isNull());
                QVERIFY(!track.contains("coverUri"));
        const auto artist = track["artists"].toArray()[0].toObject();
        QCOMPARE(artist["id"], QJsonValue::fromVariant(artistId));
        QCOMPARE(artist["entity"].toString(), restore ? QString("artist") : QString());
        QCOMPARE(track["artists"].toArray().size(), 2);
        QCOMPARE(track["artists"].toArray()[1].toObject()["id"].toString(), QString("77"));
        QVERIFY(get(dir.path() + "/README.md").contains("https://music.yandex.ru/artist/" + artistId.toString().toUtf8()));
        if (restore) QVERIFY(track["durationMs"].toInteger() > 0);
    }
    void artistValidation_data() {
        QTest::addColumn<QVariant>("secondId");
        QTest::addColumn<QString>("error");
        QTest::newRow("numeric-string-duplicate") << QVariant("4622996") << QString("Duplicate artist ID 4622996");
        QTest::newRow("numeric-duplicate") << QVariant(4622996.0) << QString("Duplicate artist ID 4622996");
        QTest::newRow("invalid") << QVariant("bad id") << QString("Invalid artist ID for Second");
        QTest::newRow("fractional") << QVariant(7.5) << QString("Invalid artist ID for Second");
        QTest::newRow("empty") << QVariant("") << QString("Yandex artist IDs are unavailable");
    }
    void artistValidation() {
        QFETCH(QVariant, secondId);
        QFETCH(QString, error);
        QTemporaryDir dir; init(dir.path());
        Uploader uploader(dir.path());
        uploader.send("36045842", "Отсиди", "Овсянкин", "https://example.test/audio.mp3",
            {{"title", "Отсиди"}, {"artists", QVariantList{
                QVariantMap{{"id", 4622996}, {"name", "Овсянкин"}},
                QVariantMap{{"id", secondId}, {"name", "Second"}}}}}, {}, true);
        QVERIFY2(uploader.error().contains(error), qPrintable(uploader.error()));
        QVERIFY(!uploader.busy());
        QVERIFY(git(dir.path(), {"status", "--porcelain"}).isEmpty());
    }
    void restoreRequiresIdentity() {
        QTemporaryDir dir; init(dir.path());
        const auto head = git(dir.path(), {"rev-parse", "HEAD"});
        Uploader uploader(dir.path());
        uploader.send("36045842", "Label", "Name", "https://example.org/audio", {}, {}, true);
        QVERIFY(uploader.error().contains("Reopen Restore to retry the API"));
        QVERIFY(!uploader.busy());
        QVariantMap patch{{"title", "Fixture"}, {"coverUri", "example.org/cover"},
            {"artists", QVariantList{QVariantMap{{"id", "spotify-id"}, {"name", "Artist"}}}}};
        uploader.send("36045842", "Label", "Name", "https://example.org/audio", patch, {}, true);
        QVERIFY(uploader.error().contains("positive numeric Yandex artist IDs"));
        QCOMPARE(git(dir.path(), {"rev-parse", "HEAD"}), head);
        QVERIFY(git(dir.path(), {"status", "--porcelain"}).isEmpty());
    }
    void sectionRouting_data() {
        QTest::addColumn<QByteArray>("eol");
        for (const auto &eol : {QByteArray("\n"), QByteArray("\r\n")})
            QTest::newRow(eol.toHex().constData()) << eol;
    }
    void sectionRouting() {
        QFETCH(QByteArray, eol);
        QTemporaryDir dir; init(dir.path());
        git(dir.path(), {"config", "core.autocrlf", "false"});
        const auto table = [&eol](const QString &heading, const QByteArray &title) {
            return "## " + heading.toUtf8() + eol + "<table>" + eol + "<tbody>" + eol
                + "\t<tr><td>Before</td></tr>  " + eol
                + "\t<tr><td>Unclosed row" + eol
                + "\t<tr><td><a href=\"https://music.yandex.ru/track/123\">" + title + "</a></td></tr>" + eol
                + "\t<tr><td>After</td></tr>" + eol + "</tbody>" + eol + "</table>" + eol;
        };
        const auto normal = table(QStringLiteral("Автоматически подменяемые треки"), "Normal");
        const auto other = table(QStringLiteral("Other tracks"), "Other");
        const QByteArray prefix = QByteArray::fromHex("efbbbf") + "# Catalog" + eol;
        const QByteArray suffix = "## Other" + eol + "Untouched  ";
        put(dir.path() + "/README.md", prefix + normal + other + suffix);
        put(dir.path() + "/list_v2.json", R"({"sources":[]})");
        git(dir.path(), {"add", "."}); git(dir.path(), {"commit", "-qm", "routing fixture"});
        Uploader uploader(dir.path());
        uploader.send("123", "Changed", "Artist");
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QCOMPARE(uploader.error(), QString());
        const auto result = get(dir.path() + "/README.md");
        QVERIFY(result.startsWith(prefix)); QVERIFY(result.endsWith(suffix));
        QVERIFY(result.contains(other));
        QCOMPARE(result.count(">Changed</a>"), 1);
        QCOMPARE(result.count("\t<tr><td>Before</td></tr>  " + eol), 2);
        QCOMPARE(result.count("\t<tr><td>After</td></tr>" + eol), 2);
        QCOMPARE(result.count("\t<tr><td>Unclosed row" + eol), 2);
        if (eol == "\r\n") QVERIFY(!QByteArray(result).replace("\r\n", "").contains('\n'));
    }
    void missingSection_data() {
        QTest::addColumn<bool>("malformed");
        QTest::newRow("absent") << false;
        QTest::newRow("malformed") << true;
    }
    void missingSection() {
        QFETCH(bool, malformed);
        QTemporaryDir dir; init(dir.path());
        QByteArray before = "# Catalog\r\n";
        if (malformed) before += QString("## Автоматически подменяемые треки\r\n<table><tr><td>Broken</td></tr></table>\r\n").toUtf8();
        before += "## Other\r\n<table><tbody></tbody></table>\r\n";
        const QByteArray list = R"({"sources":[]})";
        put(dir.path() + "/README.md", before); put(dir.path() + "/list_v2.json", list);
        git(dir.path(), {"add", "."}); git(dir.path(), {"commit", "-qm", "missing section fixture"});
        const auto head = git(dir.path(), {"rev-parse", "HEAD"});
        Uploader uploader(dir.path());
        uploader.send("123", "Song", "Artist");
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QVERIFY(uploader.error().contains("missing or malformed table"));
        QCOMPARE(get(dir.path() + "/README.md"), before);
        QCOMPARE(get(dir.path() + "/list_v2.json"), list);
        QCOMPARE(git(dir.path(), {"rev-parse", "HEAD"}), head);
        QVERIFY(git(dir.path(), {"status", "--porcelain"}).isEmpty());
    }
    void readmeTable_data() {
        QTest::addColumn<QByteArray>("eol");
        QTest::addColumn<bool>("metadata");
        QTest::newRow("lf-metadata") << QByteArray("\n") << true;
        QTest::newRow("crlf-fallback") << QByteArray("\r\n") << false;
    }
    void readmeTable() {
        QFETCH(QByteArray, eol); QFETCH(bool, metadata);
        QTemporaryDir dir; init(dir.path());
        git(dir.path(), {"config", "core.autocrlf", "false"});
        const QByteArray prefix = QByteArray::fromHex("efbbbf") + "# Catalog" + eol
            + QString("## Автоматически подменяемые треки").toUtf8() + eol + eol
            + "<table>" + eol + "\t<tbody>" + eol
            + "\t\t<tr><td>Keep original row</td></tr>" + eol;
        const QByteArray suffix = "\t</tbody>" + eol + "</table>" + eol + eol
            + QString("## Автоматически подменяемые исполнители").toUtf8() + eol
            + "<table><tbody><tr><td>Artists untouched</td></tr></tbody></table>" + eol
            + QString("## Автоматически подменяемые альбомы").toUtf8() + eol
            + "<table><tbody><tr><td>Albums untouched</td></tr></tbody></table>  ";
        put(dir.path() + "/README.md", prefix + suffix);
        put(dir.path() + "/list_v2.json", metadata
            ? R"({"sources":[{"supportedVersions":">=2.0.0","tracks":{"123":{"coverUri":"example.org/art/%%?a=1&b=2","artists":[{"id":42,"name":"A & <B>"},{"id":"43","name":"Second"}]}}}]})"
            : R"({"sources":[]})");
        git(dir.path(), {"add", "--", "README.md", "list_v2.json"});
        git(dir.path(), {"commit", "-qm", "table fixture"});
        Uploader uploader(dir.path());
        uploader.send("123", "Song <&\"", "Artist <&\"");
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QCOMPARE(uploader.error(), QString());
        const auto readme = get(dir.path() + "/README.md");
        QVERIFY(readme.startsWith(prefix)); QVERIFY(readme.endsWith(suffix));
        QVERIFY(readme.contains("href=\"https://music.yandex.ru/track/123\">Song &lt;&amp;&quot;</a>"));
        if (metadata) {
            QVERIFY(readme.contains("width=\"40px\" height=\"40px\" src=\"https://example.org/art/100x100?a=1&amp;b=2\""));
            QVERIFY(readme.contains("href=\"https://music.yandex.ru/artist/42\">A &amp; &lt;B&gt;</a>, <a href=\"https://music.yandex.ru/artist/43\">Second</a>"));
        } else {
            QVERIFY(!readme.contains("<img"));
            QVERIFY(readme.contains("Artist &lt;&amp;&quot;"));
        }
        if (eol == "\r\n") QVERIFY(!QByteArray(readme).replace("\r\n", "").contains('\n'));
        uploader.send("123", "Changed", "Artist", {}, {{"title", "Entity change"}});
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QCOMPARE(uploader.error(), QString());
        const auto changed = get(dir.path() + "/README.md");
        QCOMPARE(changed.count("https://music.yandex.ru/track/123"), 1);
        QVERIFY(changed.contains(">Changed</a>"));
        QVERIFY(changed.startsWith(prefix)); QVERIFY(changed.endsWith(suffix));
        uploader.send("123", "Ignored", "Artist");
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QCOMPARE(uploader.action(), "skipped");
        QCOMPARE(get(dir.path() + "/README.md"), changed);
    }
    void rowFormatting_data() {
        QTest::addColumn<QByteArray>("unit");
        QTest::addColumn<QByteArray>("eol");
        QTest::addColumn<bool>("cover");
        QTest::addColumn<bool>("emptyTable");
        for (const auto &unit : {QByteArray("\t"), QByteArray("  "), QByteArray("    ")})
            for (const auto &eol : {QByteArray("\n"), QByteArray("\r\n")})
                for (bool cover : {false, true})
                    for (bool emptyTable : {false, true})
                        QTest::newRow((unit.toHex() + "-" + eol.toHex() + (cover ? "-cover" : "-empty-cover")
                            + (emptyTable ? "-empty-table" : "-rows")).constData()) << unit << eol << cover << emptyTable;
    }
    void rowFormatting() {
        QFETCH(QByteArray, unit); QFETCH(QByteArray, eol); QFETCH(bool, cover); QFETCH(bool, emptyTable);
        QTemporaryDir dir; init(dir.path());
        git(dir.path(), {"config", "core.autocrlf", "false"});
        const QByteArray existing = unit.repeated(2) + "<tr>" + eol + unit.repeated(3)
            + "<td>Keep</td>" + eol + unit.repeated(2) + "</tr>" + eol;
        const QByteArray prefix = QByteArray::fromHex("efbbbf") + "# Catalog" + eol
            + QString("## Автоматически подменяемые треки").toUtf8() + eol
            + "<table>" + eol + unit + "<tbody>" + eol + (emptyTable ? QByteArray() : existing);
        const QByteArray suffix = unit + "</tbody>" + eol + "</table>" + eol
            + "## Other" + eol + "Do not change  " + eol;
        put(dir.path() + "/README.md", prefix + suffix);
        put(dir.path() + "/list_v2.json", R"({"sources":[]})");
        git(dir.path(), {"add", "."}); git(dir.path(), {"commit", "-qm", "formatting fixture"});
        Uploader uploader(dir.path());
        const QVariantMap publication = cover ? QVariantMap{{"coverUri", "example.org/cover/%%"}} : QVariantMap();
        const auto expectedRow = [&](const QByteArray &title) {
            QByteArray row = unit.repeated(2) + "<tr>" + eol;
            if (cover) row += unit.repeated(3) + "<td>" + eol + unit.repeated(4)
                + "<img width=\"40px\" height=\"40px\" src=\"https://example.org/cover/100x100\" alt=\"" + title
                + "\">" + eol + unit.repeated(3) + "</td>" + eol;
            else row += unit.repeated(3) + "<td></td>" + eol;
            row += unit.repeated(3) + "<td>" + eol + unit.repeated(4)
                + "<a href=\"https://music.yandex.ru/track/36045842\">" + title + "</a>" + eol
                + unit.repeated(3) + "</td>" + eol + unit.repeated(3) + "<td>" + eol
                + unit.repeated(4) + "Artist without ID" + eol + unit.repeated(3) + "</td>" + eol
                + unit.repeated(2) + "</tr>" + eol;
            return row;
        };
        for (const auto &title : {QByteArray("First"), QByteArray("Changed")}) {
            uploader.send("36045842", QString::fromUtf8(title), "Artist without ID", {},
                          {{"title", QString::fromUtf8(title)}}, publication);
            QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
            QCOMPARE(uploader.error(), QString());
            const auto result = get(dir.path() + "/README.md");
            const auto row = expectedRow(title);
            QCOMPARE(result, prefix + row + suffix);
            for (auto line : row.split('\n')) {
                if (line.endsWith('\r')) line.chop(1);
                QVERIFY(!line.endsWith(' ')); QVERIFY(!line.endsWith('\t'));
            }
        }
    }
    void publicationMetadata() {
        QTemporaryDir dir; init(dir.path());
        put(dir.path() + "/README.md", QString("## Автоматически подменяемые треки\n<table>\n\t<tbody>\n\t</tbody>\n</table>\n").toUtf8());
        put(dir.path() + "/list_v2.json", R"({"sources":[]})");
        git(dir.path(), {"add", "--", "README.md", "list_v2.json"});
        git(dir.path(), {"commit", "-qm", "publication fixture"});
        Uploader uploader(dir.path());
        const QVariantMap publication{{"coverUri", "example.org/original/%%"},
            {"artists", QVariantList{QVariantMap{{"id", 42}, {"name", "geoxantes"}}}}};
        uploader.send("136797648", "Да я джампер", "geoxantes", {}, {}, publication);
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QCOMPARE(uploader.error(), QString());
        auto readme = get(dir.path() + "/README.md");
        QVERIFY(readme.contains("src=\"https://example.org/original/100x100\""));
        QVERIFY(readme.contains("href=\"https://music.yandex.ru/artist/42\">geoxantes</a>"));
        const auto list = QJsonDocument::fromJson(get(dir.path() + "/list_v2.json"));
        for (const auto source : list.object().value("sources").toArray())
            QVERIFY(!source.toObject().value("tracks").toObject().contains("136797648"));
        uploader.send("136797648", "Да я джампер", "geoxantes", {},
                      {{"coverUri", "example.org/override/%%"}}, publication);
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QCOMPARE(uploader.error(), QString());
        readme = get(dir.path() + "/README.md");
        QVERIFY(readme.contains("src=\"https://example.org/override/100x100\""));
        QVERIFY(readme.contains("href=\"https://music.yandex.ru/artist/42\">geoxantes</a>"));
        QCOMPARE(readme.count("https://music.yandex.ru/track/136797648"), 1);
    }
    void spotifyPersistedCover() {
        QTemporaryDir dir; init(dir.path());
        put(dir.path() + "/list_v2.json", R"({"sources":[]})");
        git(dir.path(), {"add", "."}); git(dir.path(), {"commit", "-qm", "Spotify fixture"});
        Uploader uploader(dir.path());
        const QString cover = "https://i.scdn.co/image/ab67616d0000b273123abc";
        uploader.send("36045842", QStringLiteral("Отсиди"), "Artist", {}, {{"coverUri", cover}});
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QCOMPARE(uploader.error(), QString());
        const auto readme = get(dir.path() + "/README.md");
        QVERIFY(readme.contains("src=\"" + cover.toUtf8() + "\""));
        QVERIFY(readme.contains(QStringLiteral("Отсиди").toUtf8()));
        const auto persistedCover = [&] {
            const auto list = QJsonDocument::fromJson(get(dir.path() + "/list_v2.json")).object();
            for (const auto source : list.value("sources").toArray()) {
                const auto track = source.toObject().value("tracks").toObject().value("36045842").toObject();
                if (track.contains("coverUri")) return track.value("coverUri").toString();
            }
            return QString();
        };
        QCOMPARE(persistedCover(), cover.mid(8));
        // Same canonical audio URL: a requested cover patch must not be skipped.
        const QString replacement = "https://i.scdn.co/image/def456";
        uploader.send("36045842", QStringLiteral("Отсиди"), "Artist", {}, {{"coverUri", replacement}});
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QCOMPARE(uploader.error(), QString());
        QCOMPARE(uploader.action(), QString("updated"));
        QCOMPARE(persistedCover(), replacement.mid(8));
        const auto repeated = get(dir.path() + "/README.md");
        QVERIFY(repeated.contains("src=\"" + replacement.toUtf8() + "\""));
        QVERIFY(!repeated.contains(cover.toUtf8()));
        QCOMPARE(repeated.count("https://music.yandex.ru/track/36045842"), 1);
        // An explicit user override wins over the publication snapshot.
        uploader.send("36045842", QStringLiteral("Отсиди"), "Artist", {},
                      {{"coverUri", "avatars.yandex.net/cover/%%"}}, {{"coverUri", cover}});
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QCOMPARE(uploader.error(), QString());
        const auto updated = get(dir.path() + "/README.md");
        QVERIFY(updated.contains("src=\"https://avatars.yandex.net/cover/100x100\""));
        QVERIFY(!updated.contains(cover.toUtf8()));
        QCOMPARE(persistedCover(), QString("avatars.yandex.net/cover/%%"));
        uploader.send("36045842", QStringLiteral("Отсиди"), "Artist", {},
                      {{"coverUri", "avatars.yandex.net/cover/%%"}});
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QCOMPARE(uploader.error(), QString());
        QCOMPARE(uploader.action(), QString("skipped"));
        QCOMPARE(get(dir.path() + "/README.md"), updated);
    }
    void durationPreview_data() {
        QTest::addColumn<QString>("mode");
        for (const auto &mode : {"local", "missing", "invalid", "traversal", "invalid-url"})
            QTest::newRow(mode) << QString(mode);
    }
    void durationPreview() {
        QFETCH(QString, mode);
        QTemporaryDir dir; init(dir.path());
        if (mode == "local" || mode == "invalid") localAudio(dir.path(), "123");
        if (mode == "invalid") put(dir.path() + "/tracks/123", "not audio");
        Uploader uploader(dir.path());
        QSignalSpy ready(&uploader, &Uploader::durationReady);
        const auto request = uploader.requestDuration(mode == "traversal" ? "../123" : "123",
                                                      mode == "invalid-url" ? "file:///etc/passwd" : "");
        QVERIFY(!uploader.busy());
        QVERIFY(ready.isEmpty());
        QTRY_COMPARE_WITH_TIMEOUT(ready.size(), 1, 10000);
        QCOMPARE(ready[0][0].toInt(), request);
        QCOMPARE(ready[0][1].toLongLong(), mode == "local" ? 144 : -1);
        QVERIFY(uploader.error().isEmpty());
    }
    void remoteDurationPreview() {
        QTemporaryDir dir;
        QTcpServer server; QVERIFY(server.listen(QHostAddress::LocalHost));
        const auto body = tinyMp3();
        connect(&server, &QTcpServer::newConnection, this, [&] {
            auto socket = server.nextPendingConnection();
            connect(socket, &QTcpSocket::readyRead, socket, [socket, body] {
                socket->readAll();
                socket->write("HTTP/1.1 200 OK\r\nContent-Length: " + QByteArray::number(body.size()) +
                              "\r\nConnection: close\r\n\r\n" + body);
                socket->disconnectFromHost();
            });
            connect(socket, &QTcpSocket::disconnected, socket, &QObject::deleteLater);
        });
        Uploader uploader(dir.path());
        QSignalSpy ready(&uploader, &Uploader::durationReady);
        const auto request = uploader.requestDuration("123", "http://127.0.0.1:" + QString::number(server.serverPort()) + "/audio");
        QVERIFY(ready.isEmpty());
        QTRY_COMPARE_WITH_TIMEOUT(ready.size(), 1, 10000);
        QCOMPARE(ready[0][0].toInt(), request);
        QCOMPARE(ready[0][1].toLongLong(), 144);
        QVERIFY(QDir(dir.path()).entryList(QDir::Files).isEmpty());
    }
    void matchingStorageDuration() {
        QTemporaryDir dir; init(dir.path());
        put(dir.path() + "/list_v2.json", R"({"sources":[{"supportedVersions":">=2.0.0","tracks":{"123":{"durationMs":9999}},"tracksStorages":[{"urlTemplate":"raw.githubusercontent.com/Hazzz895/FckCensorData/refs/heads/main/tracks/%%","trackIds":[{"id":123,"durationMs":144}]}]}]})");
        git(dir.path(), {"add", "--", "list_v2.json"}); git(dir.path(), {"commit", "-qm", "list"});
        localAudio(dir.path(), "123");
        Uploader uploader(dir.path());
        uploader.send("123", "Label", "Artist", {}, {{"durationMs", 144}});
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QCOMPARE(uploader.error(), QString());
        QCOMPARE(uploader.trackMetadata("123").value("durationMs").toInt(), 144);
    }
    void measuredDuration_data() {
        QTest::addColumn<QString>("mode");
        for (const auto &mode : {"local", "download", "missing", "invalid-local", "invalid-download",
                                 "conflicting-duration", "matching-duration"})
            QTest::newRow(mode) << QString(mode);
    }
    void measuredDuration() {
        QFETCH(QString, mode);
        QTemporaryDir dir; init(dir.path());
        const QByteArray before = R"({"sources":[{"supportedVersions":">=2.0.0","tracks":{"123":{"durationMs":9999,"unknown":true}},"tracksStorages":[{"urlTemplate":"raw.githubusercontent.com/Hazzz895/FckCensorData/refs/heads/main/tracks/%%","trackIds":[456]}]}]})";
        put(dir.path() + "/list_v2.json", before);
        git(dir.path(), {"add", "--", "list_v2.json"}); git(dir.path(), {"commit", "-qm", "list"});
        const bool remote = mode == "download" || mode == "invalid-download";
        const bool unknown = mode == "missing" || mode == "invalid-local";
                const bool success = unknown || mode == "local" || mode == "download" || mode == "matching-duration";
        if (!remote && mode != "missing") {
            localAudio(dir.path(), "123");
            if (mode == "invalid-local") {
                put(dir.path() + "/tracks/123", "not audio");
                git(dir.path(), {"add", "--", "tracks/123"}); git(dir.path(), {"commit", "-qm", "invalid audio"});
            }
        }
        const auto head = git(dir.path(), {"rev-parse", "HEAD"});
        const auto readme = get(dir.path() + "/README.md");
        const auto originalAudio = get(dir.path() + "/tracks/123");
        QTcpServer server; QVERIFY(server.listen(QHostAddress::LocalHost));
        const auto body = mode == "invalid-download" ? QByteArray("not audio") : tinyMp3();
        connect(&server, &QTcpServer::newConnection, this, [&] {
            auto socket = server.nextPendingConnection();
            connect(socket, &QTcpSocket::readyRead, socket, [socket, body] {
                socket->readAll();
                socket->write("HTTP/1.1 200 OK\r\nContent-Length: " + QByteArray::number(body.size()) +
                              "\r\nConnection: close\r\n\r\n" + body);
                socket->disconnectFromHost();
            });
            connect(socket, &QTcpSocket::disconnected, socket, &QObject::deleteLater);
        });
        QVariantMap patch{{"title", "Custom"}};
        if (mode.endsWith("duration")) patch.insert("durationMs", mode == "matching-duration" ? 144 : 9999);
        Uploader uploader(dir.path());
        uploader.send("123", "Label", "Artist", remote ? "http://127.0.0.1:" + QString::number(server.serverPort()) + "/audio" : QString(), patch);
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        if (!success) {
            QVERIFY2(!uploader.error().isEmpty(), qPrintable(mode));
            QCOMPARE(get(dir.path() + "/list_v2.json"), before);
            QCOMPARE(get(dir.path() + "/README.md"), readme);
            QCOMPARE(get(dir.path() + "/tracks/123"), originalAudio);
            QCOMPARE(git(dir.path(), {"rev-parse", "HEAD"}), head);
            QVERIFY(git(dir.path(), {"status", "--porcelain"}).isEmpty());
            return;
        }
        QCOMPARE(uploader.error(), QString());
        auto source = QJsonDocument::fromJson(get(dir.path() + "/list_v2.json")).object()["sources"].toArray()[0].toObject();
        QCOMPARE(source["tracksStorages"].toArray()[0].toObject()["trackIds"].toArray(),
                 unknown ? QJsonArray({456, QJsonObject{{"id", 123}}})
                                          : QJsonArray({456, QJsonObject{{"id", 123}, {"durationMs", 144}}}));
                         if (!remote) QCOMPARE(get(dir.path() + "/tracks/123"), originalAudio);
                         QVERIFY(git(dir.path(), {"rev-parse", "HEAD"}) != head);
        QCOMPARE(source["tracks"].toObject()["123"].toObject()["durationMs"].toInt(), mode == "matching-duration" ? 144 : 9999);
        if (remote) QCOMPARE(get(dir.path() + "/tracks/123"), body);
        // A measured storage duration must not block ordinary entity edits.
        uploader.send("123", "Label", "Artist", {}, {{"title", "Changed"}});
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QCOMPARE(uploader.error(), QString()); QCOMPARE(uploader.action(), "updated");
        source = QJsonDocument::fromJson(get(dir.path() + "/list_v2.json")).object()["sources"].toArray()[0].toObject();
        QCOMPARE(source["tracks"].toObject()["123"].toObject()["title"].toString(), "Changed");
        if (unknown) return;
        const auto after = get(dir.path() + "/list_v2.json");
        uploader.send("123", "Label", "Artist", "http://127.0.0.1:1/never", {{"durationMs", 200}});
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QVERIFY(uploader.error().contains("shadow"));
        QCOMPARE(get(dir.path() + "/list_v2.json"), after);
    }
    void sourceRouting_data() {
        QTest::addColumn<bool>("metadata");
        QTest::newRow("plain") << false;
        QTest::newRow("metadata") << true;
    }
    void sourceRouting() {
        QFETCH(bool, metadata);
        QTemporaryDir dir; init(dir.path());
        const QByteArray legacy = R"({"supportedVersions":">0.0.0","tracksStorages":[{"urlTemplate":"raw.githubusercontent.com/Hazzz895/FckCensorData/refs/heads/main/tracks/%%","trackIds":[456]}]})";
        put(dir.path() + "/list_v2.json", "{\"sources\":[{\"supportedVersions\":\">=2.0.0\",\"tracks\":{\"1\":{\"title\":\"Keep\"}}}," + legacy + "]}");
        git(dir.path(), {"add", "--", "list_v2.json"}); git(dir.path(), {"commit", "-qm", "list"});
        Uploader uploader(dir.path());
        localAudio(dir.path(), "123");
        uploader.send("123", "Label", "Artist", {}, metadata ? QVariantMap{{"title", "Custom"}} : QVariantMap{});
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QCOMPARE(uploader.error(), QString());
        const auto bytes = get(dir.path() + "/list_v2.json");
        QVERIFY(bytes.contains(legacy));
        const auto sources = QJsonDocument::fromJson(bytes).object()["sources"].toArray();
        QCOMPARE(sources.size(), 2);
        const auto source = sources[0].toObject();
        QCOMPARE(source["tracksStorages"].toArray()[0].toObject()["trackIds"].toArray(),
                 QJsonArray({QJsonObject{{"id", 123}, {"durationMs", 144}}}));
        QCOMPARE(source["tracks"].toObject().contains("123"), metadata);
        if (metadata) QCOMPARE(source["tracks"].toObject()["123"].toObject()["title"].toString(), "Custom");
    }
    void optionalMetadata() {
        QTemporaryDir dir; init(dir.path());
        const QByteArray before = R"({"sources":[{"supportedVersions":">0.0.0","tracks":{"123":{"title":"Old","unknown":{"keep":true},"artists":[{"id":"42","name":"Old artist","coverUri":"keep"}]}},"tracksStorages":[{"urlTemplate":"raw.githubusercontent.com/Hazzz895/FckCensorData/refs/heads/main/tracks/%%","trackIds":[123]}]}]})";
        put(dir.path() + "/list_v2.json", before);
        git(dir.path(), {"add", "--", "list_v2.json"});
        git(dir.path(), {"commit", "-qm", "list"});
        Uploader uploader(dir.path());
        uploader.send("123", "Display label", "Display artist", {}, {{"title", "Custom"}, {"coverUri", "https://example.org/cover/%%"}, {"durationMs", 123000}, {"artists", QVariantList{QVariantMap{{"id", "42"}, {"name", "New artist"}}}}});
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QCOMPARE(uploader.error(), QString()); QCOMPARE(uploader.action(), "updated");
        const auto source = QJsonDocument::fromJson(get(dir.path() + "/list_v2.json")).object()["sources"].toArray()[0].toObject();
        const auto track = source["tracks"].toObject()["123"].toObject();
        QCOMPARE(track["title"].toString(), "Custom");
        QCOMPARE(track["coverUri"].toString(), "example.org/cover/%%");
        QCOMPARE(track["durationMs"].toInt(), 123000);
        QVERIFY(track["unknown"].toObject()["keep"].toBool());
        QCOMPARE(track["artists"].toArray()[0].toObject()["coverUri"].toString(), "keep");
        QVERIFY(track["__fckCensor"].toObject()["insertionIndex"].isNull());
        QCOMPARE(source["tracksStorages"].toArray()[0].toObject()["trackIds"].toArray()[0].toInt(), 123);
        const auto bytes = get(dir.path() + "/list_v2.json");
        const auto head = git(dir.path(), {"rev-parse", "HEAD"});
        uploader.send("123", "Other display", "Other artist");
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QCOMPARE(uploader.error(), QString()); QCOMPARE(uploader.action(), "skipped");
        QCOMPARE(get(dir.path() + "/list_v2.json"), bytes);
        QCOMPARE(git(dir.path(), {"rev-parse", "HEAD"}), head);
        uploader.send("123", "Label", "Artist", "http://127.0.0.1:1/never", {{"durationMs", -1}});
        QVERIFY(!uploader.busy()); QVERIFY(!uploader.error().isEmpty());
        QCOMPARE(get(dir.path() + "/list_v2.json"), bytes);
    }
    void metadataPartialAndIntentionalInsertion() {
        QTemporaryDir dir; init(dir.path());
        const QByteArray before = R"({"sources":[{"supportedVersions":">=2.0.0","tracks":{"123":{"title":"Keep","coverUri":"keep.example/art","__fckCensor":{"insertionIndex":0,"unknown":"keep"},"artists":[{"id":42,"name":"Before","various":true}]}},"tracksStorages":[{"urlTemplate":"raw.githubusercontent.com/Hazzz895/FckCensorData/refs/heads/main/tracks/%%","trackIds":[]}]}]})";
        put(dir.path() + "/list_v2.json", before);
        git(dir.path(), {"add", "--", "list_v2.json"}); git(dir.path(), {"commit", "-qm", "list"});
        Uploader uploader(dir.path());
        localAudio(dir.path(), "123");
        uploader.send("123", "Display", "Display", {}, {{"artists", QVariantList{QVariantMap{{"id", "42"}, {"name", "After"}}}}});
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QCOMPARE(uploader.error(), QString()); QCOMPARE(uploader.action(), "added");
        const auto source = QJsonDocument::fromJson(get(dir.path() + "/list_v2.json")).object()["sources"].toArray()[0].toObject();
        const auto track = source["tracks"].toObject()["123"].toObject();
        QCOMPARE(track["title"].toString(), "Keep"); QCOMPARE(track["coverUri"].toString(), "keep.example/art");
        QCOMPARE(track["__fckCensor"].toObject()["insertionIndex"].toInt(-1), 0);
        QCOMPARE(track["__fckCensor"].toObject()["unknown"].toString(), "keep");
        QVERIFY(track["artists"].toArray()[0].toObject()["various"].toBool());
        QCOMPARE(source["tracksStorages"].toArray()[0].toObject()["trackIds"].toArray(),
                 QJsonArray({QJsonObject{{"id", 123}, {"durationMs", 144}}}));
        const auto after = get(dir.path() + "/list_v2.json");
        const auto head = git(dir.path(), {"rev-parse", "HEAD"});
        uploader.send("123", "Display", "Display", {}, {{"title", "Keep"}});
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QCOMPARE(uploader.error(), QString()); QCOMPARE(uploader.action(), "skipped");
        QCOMPARE(get(dir.path() + "/list_v2.json"), after); QCOMPARE(git(dir.path(), {"rev-parse", "HEAD"}), head);
    }
    void invalidMetadata_data() {
        QTest::addColumn<QVariantMap>("patch");
        QTest::newRow("empty-title") << QVariantMap{{"title", " "}};
        QTest::newRow("null-title") << QVariantMap{{"title", QVariant()}};
        QTest::newRow("fraction-duration") << QVariantMap{{"durationMs", 1.5}};
        QTest::newRow("unknown") << QVariantMap{{"albums", QVariantList{}}};
        QTest::newRow("invalid-cover") << QVariantMap{{"coverUri", "file:///tmp/cover"}};
        QTest::newRow("empty-artists") << QVariantMap{{"artists", QVariantList{}}};
        QTest::newRow("missing-id") << QVariantMap{{"artists", QVariantList{QVariantMap{{"name", "Name"}}}}};
        QTest::newRow("bad-id") << QVariantMap{{"artists", QVariantList{QVariantMap{{"id", "../42"}, {"name", "Name"}}}}};
        QTest::newRow("bad-name") << QVariantMap{{"artists", QVariantList{QVariantMap{{"id", "42"}, {"name", "Name\nOther"}}}}};
    }
    void invalidMetadata() {
        QFETCH(QVariantMap, patch);
        QTemporaryDir dir; init(dir.path());
        Uploader uploader(dir.path());
        uploader.send("123", "Label", "Artist", "http://127.0.0.1:1/never", patch);
        QVERIFY(!uploader.busy()); QVERIFY(!uploader.error().isEmpty());
        QVERIFY(!QFile::exists(dir.path() + "/list_v2.json"));
        QVERIFY(!QFile::exists(dir.path() + "/tracks"));
        QCOMPARE(get(dir.path() + "/README.md"), defaultReadme());
    }
    void metadataShadowing_data() {
        QTest::addColumn<QByteArray>("sources");
        const QByteArray store = R"({"urlTemplate":"raw.githubusercontent.com/Hazzz895/FckCensorData/refs/heads/main/tracks/%%","trackIds":[123]})";
        QTest::newRow("ambiguous modern gate") << QByteArray(R"({"supportedVersions":">=2.0.0"},{"supportedVersions":">=2.0.0"})");
        QTest::newRow("earlier metadata") << QByteArray(R"({"tracks":{"123":{"title":"first"}}},{"tracksStorages":[)") + store + "]}";
        QTest::newRow("duration") << QByteArray(R"({"tracksStorages":[{"urlTemplate":"raw.githubusercontent.com/Hazzz895/FckCensorData/refs/heads/main/tracks/%%","trackIds":[{"id":123,"durationMs":10}]}]})");
        QTest::newRow("ambiguous") << QByteArray("{\"tracksStorages\":[") + store + "]},{\"tracksStorages\":[" + store + "]}";
    }
    void metadataShadowing() {
        QFETCH(QByteArray, sources);
        QTemporaryDir dir; init(dir.path());
        const auto before = "{\"sources\":[" + sources + "]}";
        put(dir.path() + "/list_v2.json", before);
        git(dir.path(), {"add", "--", "list_v2.json"}); git(dir.path(), {"commit", "-qm", "list"});
        Uploader uploader(dir.path());
        uploader.send("123", "Label", "Artist", "http://127.0.0.1:1/never",
                      sources.contains("durationMs") ? QVariantMap{{"durationMs", 20}} : QVariantMap{{"title", "Custom"}});
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QVERIFY(!uploader.error().isEmpty()); QVERIFY(!uploader.error().contains("Download"));
        QCOMPARE(get(dir.path() + "/list_v2.json"), before);
        QVERIFY(!QFile::exists(dir.path() + "/tracks/123"));
    }
    void reuseGitHubList() {
        QTemporaryDir dir; init(dir.path());
        const QByteArray storage = R"({"urlTemplate":"raw.githubusercontent.com/Hazzz895/FckCensorData/refs/heads/main/tracks/%%","trackIds":[{"id":119689460,"durationMs":110838},152396542],"tracks":{"149360077":149665384,"155807913":151855578},"unknown":9007199254740993})";
        const QByteArray before = R"({"sources":[{"supportedVersions":">=2.0.0","tracks":{"1":{"title":"keep"}}},{"supportedVersions":">0.0.0","tracksStorages":[)" + storage + "]}]}";
        put(dir.path() + "/list_v2.json", before);
        git(dir.path(), {"add", "--", "list_v2.json"});
        git(dir.path(), {"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false", "commit", "-qm", "list"});
        Uploader uploader(dir.path());
        localAudio(dir.path(), "126984723");
        uploader.send("126984723", "Title", "Artist");
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QCOMPARE(uploader.error(), QString());
        auto want = before;
                want.replace("\"tracks\":{\"1\":{\"title\":\"keep\"}}", "\"tracks\":{\"1\":{\"title\":\"keep\"}},\"tracksStorages\":[{\"trackIds\":[{\"durationMs\":144,\"id\":126984723}],\"urlTemplate\":\"raw.githubusercontent.com/Hazzz895/FckCensorData/refs/heads/main/tracks/%%\"}]");
        QCOMPARE(get(dir.path() + "/list_v2.json"), want);
        const auto head = git(dir.path(), {"rev-parse", "HEAD"});
        uploader.send("126984723", "Title", "Artist");
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QCOMPARE(uploader.error(), QString()); QCOMPARE(uploader.action(), "skipped");
        QCOMPARE(get(dir.path() + "/list_v2.json"), want);
        QCOMPARE(git(dir.path(), {"rev-parse", "HEAD"}), head);
        for (const auto &alias : {QString("149360077"), QString("155807913")}) {
            uploader.send(alias, "Title", "Artist");
            QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
            QCOMPARE(uploader.error(), QString()); QCOMPARE(uploader.action(), "updated");
        }
        const QByteArray ambiguous = "{\"sources\":[{\"tracksStorages\":[" + storage + "," + storage + "]}]}";
        put(dir.path() + "/list_v2.json", ambiguous);
        git(dir.path(), {"add", "--", "list_v2.json"});
        git(dir.path(), {"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false", "commit", "-qm", "ambiguous"});
        uploader.send("126984723", "Title", "Artist");
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QVERIFY(uploader.error().contains("Ambiguous"));
        QCOMPARE(get(dir.path() + "/list_v2.json"), ambiguous);
    }
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
        put(dir.path() + "/list_v2.json", "{\"sources\":[{\"supportedVersions\":\">0.0.0\",\"tracksStorages\":[{\"tracks\":{\"123\":\"https://old.example/123\"}}]}]}");
        git(dir.path(), {"add", "--", "list_v2.json"});

        git(dir.path(), {"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false", "commit", "--only", "-qm", "old URL", "--", "list_v2.json"});
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
        const QByteArray prefix = bom + "{" + eol + "    \"unknown\": {\"keep\": 123}," + eol + "    \"sources\": [" + eol
            + "{\"supportedVersions\":\">0.0.0\",\"tracksStorages\":[{\"tracks\":{\"1\":\"https://old.example/1\"}}]}";
        const QByteArray suffix = " \t" + eol + "    ]" + eol + "}" + eol + " \t";
        const QByteArray readme = bom + QByteArray(defaultReadme()).replace("\n", eol) + "trailing spaces \t";
        put(dir.path() + "/list_v2.json", prefix + suffix);
        put(dir.path() + "/README.md", readme);
        git(dir.path(), {"add", "--", "list_v2.json", "README.md"});
        git(dir.path(), {"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false", "commit", "-qm", "format fixture"});
        Uploader uploader(dir.path()); uploader.send("2", "Song", "Artist");
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QCOMPARE(uploader.error(), QString());
        const auto result = get(dir.path() + "/list_v2.json");
        QVERIFY(result.startsWith(prefix + "," + eol));
                QVERIFY(result.endsWith(suffix));
        QVERIFY(result.endsWith("]" + eol + "}" + eol + " \t"));
        const auto sources = QJsonDocument::fromJson(result).object().value("sources").toArray();
        QCOMPARE(sources.size(), 2);
        QCOMPARE(sources[1].toObject().value("supportedVersions").toString(), ">=2.0.0");
        QCOMPARE(sources[1].toObject().value("tracksStorages").toArray()[0].toObject().value("tracks").toObject().value("2").toString(),
                 QString("https://raw.githubusercontent.com/Hazzz895/FckCensorData/refs/heads/main/tracks/2"));
        const auto changedReadme = get(dir.path() + "/README.md");
        QVERIFY(changedReadme.startsWith(bom + "# Tracks" + eol));
        QVERIFY(changedReadme.endsWith("trailing spaces \t"));
        QVERIFY(changedReadme.contains("https://music.yandex.ru/track/2"));
        QVERIFY(get(dir.path() + "/list_v2.json").startsWith(prefix));
        uploader.send("1", "Updated", "Artist");
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QCOMPARE(uploader.error(), QString()); QCOMPARE(uploader.action(), "updated");
        QVERIFY(get(dir.path() + "/list_v2.json").startsWith(bom + "{" + eol));
    }
    void prettyV2Insertion_data() {
        QTest::addColumn<bool>("storage");
        QTest::addColumn<QByteArray>("unit");
        QTest::addColumn<QByteArray>("eol");
        QTest::newRow("source-spaces") << false << QByteArray("  ") << QByteArray("\n");
        QTest::newRow("source-tabs-crlf") << false << QByteArray("\t") << QByteArray("\r\n");
        QTest::newRow("storage-spaces") << true << QByteArray("  ") << QByteArray("\n");
        QTest::newRow("storage-tabs-crlf") << true << QByteArray("\t") << QByteArray("\r\n");
    }
    void prettyV2Insertion() {
        QFETCH(bool, storage); QFETCH(QByteArray, unit); QFETCH(QByteArray, eol);
        QTemporaryDir dir; init(dir.path());
        git(dir.path(), {"config", "core.autocrlf", "false"});
        QByteArray input = "{\n  \"unknown\" : {\"number\":1e+03,\"escaped\":\"\\u0061\"},\n  \"sources\": [\n    {\n      \"supportedVersions\": \">0.0.0\",\n      \"tracksStorages\": [\n        {\n          \"tracks\": {\n            \"1\": \"https://old.example/1\",\n            \"99\": \"https://old.example/99\", \n\n            \"100\" : \"https://old.example/100\"\n          }\n        }\n      ]\n    }\n  ]\n}\n";
        const QByteArray url = "https://raw.githubusercontent.com/Hazzz895/FckCensorData/refs/heads/main/tracks/";
        QByteArray expected = input;
        if (storage) {
            expected.replace("            \"1\": \"https://old.example/1\",\n", "");
            expected.replace("        }\n      ]", "        },\n        {\n          \"tracks\": {\n            \"1\": {\n              \"url\": \"" + url + "1\"\n            }\n          }\n        }\n      ]");
        } else {
            expected.replace("    }\n  ]", "    },\n    {\n      \"supportedVersions\": \">=2.0.0\",\n      \"tracksStorages\": [\n        {\n          \"tracks\": {\n            \"2\": \"" + url + "2\"\n          }\n        }\n      ]\n    }\n  ]");
        }
        const auto layout = [&](QByteArray bytes) {
            auto lines = bytes.split('\n');
            for (auto &line : lines) {
                int spaces = 0;
                while (spaces < line.size() && line[spaces] == ' ') ++spaces;
                line = unit.repeated(spaces / 2) + line.mid(spaces);
            }
            return QByteArray::fromHex("efbbbf") + lines.join(eol);
        };
        put(dir.path() + "/list_v2.json", layout(input));
        git(dir.path(), {"add", "--", "list_v2.json"});
        git(dir.path(), {"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false", "commit", "-qm", "pretty fixture"});
        Uploader uploader(dir.path()); uploader.send(storage ? "1" : "2", "Song", "Artist");
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QCOMPARE(uploader.error(), QString());
        QCOMPARE(get(dir.path() + "/list_v2.json"), layout(expected));
    }
    void legacyIsNeverTouched_data() {
        QTest::addColumn<bool>("legacyExists");
        QTest::newRow("absent") << false;
        QTest::newRow("dirty-and-staged") << true;
    }
    void legacyIsNeverTouched() {
        QFETCH(bool, legacyExists);
        QTemporaryDir dir; init(dir.path());
        const QByteArray staged = "legacy staged bytes\xff";
        const QByteArray working = QByteArray("legacy working bytes") + QByteArray::fromHex("00ff");
        if (legacyExists) {
            put(dir.path() + "/list.json", staged);
            git(dir.path(), {"add", "--", "list.json"});
            put(dir.path() + "/list.json", working);
        }
        const auto index = git(dir.path(), {"diff", "--cached", "--binary", "--", "list.json"});
        Uploader uploader(dir.path()); uploader.send("123", "Song", "Artist");
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QCOMPARE(uploader.error(), QString()); QCOMPARE(uploader.action(), "added");
        QCOMPARE(QFile::exists(dir.path() + "/list.json"), legacyExists);
        if (legacyExists) QCOMPARE(get(dir.path() + "/list.json"), working);
        QCOMPARE(git(dir.path(), {"diff", "--cached", "--binary", "--", "list.json"}), index);
        QCOMPARE(git(dir.path(), {"diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD"}), QByteArray("README.md\nlist_v2.json\n"));
    }
    void v2Mappings_data() {
        QTest::addColumn<QByteArray>("store");
        QTest::addColumn<QString>("action");
        QTest::newRow("explicit") << QByteArray("{\"tracks\":{\"123\":{\"url\":\"https://old.example/123\",\"durationMs\":100,\"unknown\":{\"keep\":true}},\"456\":\"https://keep.example/456\"},\"storeMetadata\":true}") << QString("updated");
        QTest::newRow("template-map") << QByteArray("{\"urlTemplate\":\"old.example/%%\",\"tracks\":{\"123\":{\"url\":\"123\",\"unknown\":{\"keep\":true},\"durationMs\":100},\"456\":\"456\"}}") << QString("updated");
        QTest::newRow("template-ids") << QByteArray("{\"urlTemplate\":\"https://old.example/%%\",\"trackIds\":[{\"id\":123,\"unknown\":{\"keep\":true},\"durationMs\":100},456]}") << QString("updated");
        QTest::newRow("skip-template") << QByteArray("{\"urlTemplate\":\"https://raw.githubusercontent.com/Hazzz895/FckCensorData/refs/heads/main/tracks/%%\",\"trackIds\":[123,456]}") << QString("skipped");
    }
    void v2Mappings() {
        QFETCH(QByteArray, store); QFETCH(QString, action);
        QTemporaryDir dir; init(dir.path());
        const QByteArray original = "{\"unknown\": {\"text\": \"preserve\"}, \"sources\":[{\"supportedVersions\":\">=1.2.3 <2.0.0\",\"sourceMetadata\":true,\"tracksStorages\":[" + store + "]},{\"supportedVersions\":\">0.0.0\",\"metadataOnly\":true}]}";
        put(dir.path() + "/list_v2.json", original);
        git(dir.path(), {"add", "--", "list_v2.json"});
        git(dir.path(), {"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false", "commit", "-qm", "v2 fixture"});
        const auto head = git(dir.path(), {"rev-parse", "HEAD"});
        Uploader uploader(dir.path()); uploader.send("123", "Song", "Artist");
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QCOMPARE(uploader.error(), QString()); QCOMPARE(uploader.action(), action);
        const auto bytes = get(dir.path() + "/list_v2.json");
        QVERIFY(bytes.startsWith("{\"unknown\": {\"text\": \"preserve\"}"));
        if (action == "skipped") {
            QCOMPARE(bytes, original); QCOMPARE(git(dir.path(), {"rev-parse", "HEAD"}), head);
        } else {
            const auto sources = QJsonDocument::fromJson(bytes).object().value("sources").toArray();
            QCOMPARE(sources.size(), 2);
            const auto source = sources[0].toObject();
            QCOMPARE(source.value("supportedVersions").toString(), ">=1.2.3 <2.0.0");
            QVERIFY(source.value("sourceMetadata").toBool());
            const auto stores = source.value("tracksStorages").toArray();
            QCOMPARE(stores.size(), 2);
            const auto entry = stores[1].toObject().value("tracks").toObject().value("123").toObject();
            QCOMPARE(entry.value("url").toString(), QString("https://raw.githubusercontent.com/Hazzz895/FckCensorData/refs/heads/main/tracks/123"));
            QVERIFY(entry.value("unknown").toObject().value("keep").toBool());
            QVERIFY(!entry.contains("durationMs"));
            const auto oldStore = stores[0].toObject();
            QVERIFY(!oldStore.value("tracks").toObject().contains("123"));
            if (oldStore.contains("trackIds")) QCOMPARE(oldStore.value("trackIds").toArray(), QJsonArray{456});
            else QVERIFY(oldStore.value("tracks").toObject().contains("456"));
            QCOMPARE(sources[1].toObject().value("metadataOnly").toBool(), true);
        }
        QVERIFY(!QFile::exists(dir.path() + "/list.json"));
    }
    void invalidV2_data() {
        QTest::addColumn<QByteArray>("bytes");
        QTest::newRow("empty") << QByteArray();
        QTest::newRow("legacy-shape") << QByteArray("{\"tracks\":{}}");
        QTest::newRow("malformed") << QByteArray("{");
        QTest::newRow("bad-storage") << QByteArray("{\"sources\":[{\"tracksStorages\":{}}]}");
        QTest::newRow("bad-template") << QByteArray("{\"sources\":[{\"tracksStorages\":[{\"urlTemplate\":\"bad\",\"trackIds\":[123]}]}]}");
        QTest::newRow("conflict") << QByteArray("{\"sources\":[{\"tracksStorages\":[{\"tracks\":{\"123\":\"https://one.example/123\"}},{\"tracks\":{\"123\":\"https://two.example/123\"}}]}]}");
    }
    void invalidV2() {
        QFETCH(QByteArray, bytes);
        QTemporaryDir dir; init(dir.path());
        put(dir.path() + "/list_v2.json", bytes);
        git(dir.path(), {"add", "--", "list_v2.json"});
        git(dir.path(), {"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false", "commit", "-qm", "invalid v2"});
        const auto head = git(dir.path(), {"rev-parse", "HEAD"});
        Uploader uploader(dir.path()); uploader.send("123", "Song", "Artist", "http://127.0.0.1:1/not-requested");
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QVERIFY(uploader.error().contains("original preserved"));
        QCOMPARE(get(dir.path() + "/list_v2.json"), bytes);
        QCOMPARE(git(dir.path(), {"rev-parse", "HEAD"}), head);
        QVERIFY(!QFile::exists(dir.path() + "/tracks/123"));
        QVERIFY(!QFile::exists(dir.path() + "/list.json"));
    }
    void invalidUtf8_data() {
        QTest::addColumn<QString>("target");
        QTest::addColumn<QByteArray>("invalid");
        for (const auto &target : {QString("list_v2.json"), QString("README.md")}) {
            for (const auto &invalid : {QByteArray::fromHex("ff"), QByteArray::fromHex("c080"),
                                        QByteArray::fromHex("eda080"), QByteArray::fromHex("e282")}) {
                QTest::newRow((target.toUtf8() + invalid.toHex()).constData()) << target << invalid;
            }
        }
    }
    void invalidUtf8() {
        QFETCH(QString, target); QFETCH(QByteArray, invalid);
        QTemporaryDir dir; init(dir.path());
        const QByteArray list = "{\"sources\": [], \"unknown\": \"" + (target == "list_v2.json" ? invalid : QByteArray()) + "\"}";
        const QByteArray readme = "# Tracks\n" + (target == "README.md" ? invalid : QByteArray());
        put(dir.path() + "/list_v2.json", list); put(dir.path() + "/README.md", readme);
        git(dir.path(), {"add", "--", "list_v2.json", "README.md"});
        git(dir.path(), {"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false", "commit", "-qm", "invalid fixture"});
        const auto head = git(dir.path(), {"rev-parse", "HEAD"});
        Uploader uploader(dir.path()); uploader.send("2", "Song", "Artist");
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QVERIFY2(uploader.error().contains("Invalid UTF-8"), qPrintable(uploader.error()));
        QCOMPARE(get(dir.path() + "/list_v2.json"), list); QCOMPARE(get(dir.path() + "/README.md"), readme);
        QCOMPARE(git(dir.path(), {"rev-parse", "HEAD"}), head);
        QVERIFY(git(dir.path(), {"status", "--porcelain"}).isEmpty());
    }
    void invalidAndDirtyTargets() {
        QTemporaryDir dir; init(dir.path()); Uploader uploader(dir.path());
        uploader.send("../escape", "x", "y");
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000); QVERIFY(!uploader.error().isEmpty());
        QVERIFY(!QFile::exists(dir.path() + "/list_v2.json"));
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
        QVERIFY(QFile::link(outside.path() + "/list", dir.path() + "/list_v2.json"));
        uploader.send("12", "x", "y");
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QVERIFY(uploader.error().contains("symlink"));
        QCOMPARE(get(outside.path() + "/list"), QByteArray("do not touch"));
        QVERIFY(QFile::remove(dir.path() + "/list_v2.json"));
        git(dir.path(), {"config", "user.name", ""});
        const auto head = git(dir.path(), {"rev-parse", "HEAD"});
        QSignalSpy done(&uploader, &Uploader::completed);
        uploader.send("12", "x", "y");
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QVERIFY2(uploader.error().contains("Git failed"), qPrintable(uploader.error())); QCOMPARE(done.count(), 0);
        QCOMPARE(git(dir.path(), {"rev-parse", "HEAD"}), head);
        QVERIFY(QFile::exists(dir.path() + "/list_v2.json"));
    }
    void concurrentEditsDuringDownload_data() {
        QTest::addColumn<QString>("change");
        for (const auto &change : {"README.md", "list_v2.json", "tracks/99", "index", "HEAD", "symlink", "tracks-symlink", "operation"})
            QTest::newRow(change) << QString(change);
    }
    void concurrentEditsDuringDownload() {
        QFETCH(QString, change);
        QTemporaryDir dir; QVERIFY(dir.isValid()); init(dir.path());
        const auto originalHead = git(dir.path(), {"rev-parse", "HEAD"});
        const auto originalReadme = get(dir.path() + "/README.md");
        QTcpServer server; QVERIFY(server.listen(QHostAddress::LocalHost));
        QPointer<QTcpSocket> socket;
        bool requested = false;
        connect(&server, &QTcpServer::newConnection, this, [&] {
            socket = server.nextPendingConnection();
            connect(socket, &QTcpSocket::readyRead, socket, [&] { socket->readAll(); requested = true; });
        });
        Uploader uploader(dir.path()); QSignalSpy done(&uploader, &Uploader::completed);
        uploader.send("99", "Song", "Artist", "http://127.0.0.1:" + QString::number(server.serverPort()) + "/audio");
        QTRY_VERIFY(requested); QVERIFY(uploader.busy());
        QTemporaryDir outside;
        if (change == "index") {
            put(dir.path() + "/index-version", "external staged work");
            const auto blob = git(dir.path(), {"hash-object", "-w", "index-version"}).trimmed();
            git(dir.path(), {"update-index", "--cacheinfo", "100644", QString::fromLatin1(blob), "README.md"});
        } else if (change == "HEAD") {
            put(dir.path() + "/unrelated", "external commit");
            git(dir.path(), {"add", "--", "unrelated"});
            git(dir.path(), {"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false", "commit", "-qm", "external"});
        } else if (change == "operation") {
            put(dir.path() + "/.git/MERGE_HEAD", originalHead);
        } else if (change == "tracks-symlink") {
            QVERIFY(QFile::link(outside.path(), dir.path() + "/tracks"));
        } else if (change == "symlink") {
            put(outside.path() + "/README.md", "outside work");
            QVERIFY(QFile::remove(dir.path() + "/README.md"));
            QVERIFY(QFile::link(outside.path() + "/README.md", dir.path() + "/README.md"));
        } else {
            if (change.startsWith("tracks/")) QVERIFY(QDir().mkpath(dir.path() + "/tracks"));
            put(dir.path() + "/" + change, "external edits");
        }
        const auto expectedHead = git(dir.path(), {"rev-parse", "HEAD"});
        const auto expectedIndex = git(dir.path(), {"diff", "--cached", "--binary"});
        socket->write("HTTP/1.1 200 OK\r\nContent-Length: 3\r\nConnection: close\r\n\r\nmp3"); socket->disconnectFromHost();
        QTRY_VERIFY_WITH_TIMEOUT(!uploader.busy(), 20000);
        QVERIFY2(!uploader.error().isEmpty(), qPrintable(uploader.status())); QCOMPARE(done.count(), 0);
        QCOMPARE(git(dir.path(), {"rev-parse", "HEAD"}), expectedHead);
        QCOMPARE(git(dir.path(), {"diff", "--cached", "--binary"}), expectedIndex);
        if (change == "symlink") QCOMPARE(get(outside.path() + "/README.md"), QByteArray("outside work"));
        else if (change == "index") QVERIFY(!QFile::exists(dir.path() + "/list_v2.json"));
        else if (change == "operation") QCOMPARE(get(dir.path() + "/.git/MERGE_HEAD"), originalHead);
        else if (change == "tracks-symlink") QVERIFY(!QFile::exists(outside.path() + "/99"));
        else if (change != "HEAD") QCOMPARE(get(dir.path() + "/" + change), QByteArray("external edits"));
        if (change != "README.md" && change != "symlink") QCOMPARE(get(dir.path() + "/README.md"), originalReadme);
        if (change != "tracks/99") QVERIFY(!QFile::exists(dir.path() + "/tracks/99"));
        if (change != "HEAD") QCOMPARE(expectedHead, originalHead);
        if (socket) delete socket;
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
#ifdef UPLOADER_QML_INTEGRATION
QTEST_MAIN(UploaderTests)
#else
QTEST_GUILESS_MAIN(UploaderTests)
#endif
#include "uploader_tests.moc"

#include "player.h"

#include <QCryptographicHash>
#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>
#include <QNetworkReply>
#include <QNetworkRequest>
#include <QRegularExpression>
#include <QTimer>
#include <QUrlQuery>
#include <QXmlStreamReader>
#include <cmath>
#include <limits>

namespace {
const QString api = QStringLiteral("https://api.music.yandex.net");
bool validTrackId(const QString &id) {
    static const QRegularExpression pattern(QStringLiteral("\\A[0-9]{1,20}\\z"));
    return pattern.match(id).hasMatch();
}
qint64 lyricTimestamp(const QString &timestamp) {
    static const QRegularExpression pattern(QStringLiteral("\\A([0-9]+):([0-5][0-9])(?::([0-5][0-9]))?(?:\\.([0-9]+))?\\z"));
    const auto match = pattern.match(timestamp);
    if (!match.hasMatch()) return -1;
    bool ok = false;
    const auto first = match.captured(1).toLongLong(&ok);
    const qint64 factor = match.captured(3).isEmpty() ? 60000 : 3600000;
    if (!ok || first > (std::numeric_limits<qint64>::max() - 3599999) / factor) return -1;
    const auto fraction = (match.captured(4) + "000").left(3).toInt();
    return first * factor + match.captured(2).toInt() * (factor == 60000 ? 1000 : 60000)
        + match.captured(3).toInt() * 1000 + fraction;
}
bool trustedUrl(const QUrl &url) {
    const auto host = url.host().toLower();
    return url.isValid() && url.scheme() == "https" && url.userInfo().isEmpty()
        && (url.port(-1) == -1 || url.port() == 443) && !url.hasFragment()
        && (host.endsWith(".yandex.net") || host.endsWith(".yandex.ru"));
}
QJsonObject jsonObject(const QByteArray &data) {
    return QJsonDocument::fromJson(data).object();
}
QUrl mediaUrl(const QByteArray &descriptor, QString *error = nullptr) {
    if (error) error->clear();
    const auto invalid = [error](const QString &reason) {
        // Report only the failing field, never signed URLs or descriptor values.
        if (error) *error = QStringLiteral("Invalid download descriptor: %1.").arg(reason);
        return QUrl();
    };
    QString host, path, ts, salt;
    const auto json = jsonObject(descriptor);
    if (!json.isEmpty()) {
        host = json.value("host").toString();
        path = json.value("path").toString();
        ts = json.value("ts").toVariant().toString();
        salt = json.value("s").toString();
    } else {
        QXmlStreamReader reader(descriptor);
        while (!reader.atEnd()) {
            reader.readNext();
            if (!reader.isStartElement()) continue;
            const auto name = reader.name().toString();
            if (name == "host") host = reader.readElementText();
            else if (name == "path") path = reader.readElementText();
            else if (name == "ts") ts = reader.readElementText();
            else if (name == "s") salt = reader.readElementText();
        }
        if (reader.hasError()) return invalid(QStringLiteral("malformed XML or JSON"));
    }
    const QUrl origin("https://" + host);
    if (!trustedUrl(origin) || !origin.path().isEmpty() || origin.hasQuery())
        return invalid(QStringLiteral("missing or untrusted host"));
    if (!path.startsWith('/') || path.contains('#'))
        return invalid(QStringLiteral("path must start with '/' and have no fragment"));
    // The service's ts is an opaque URL segment, not a decimal timestamp.
    // Preserve it verbatim (including hexadecimal letters) when building the URL.
    if (!QRegularExpression(QStringLiteral("\\A[A-Za-z0-9._~-]{1,128}\\z")).match(ts).hasMatch())
        return invalid(QStringLiteral("ts must be a nonempty safe URL segment"));
    if (salt.isEmpty()) return invalid(QStringLiteral("missing s"));
    // Match yandex-music DownloadInfo: hash the raw path minus its first slash,
    // including the query and percent escapes, then append that same path.
    const auto sign = QCryptographicHash::hash(
        (QStringLiteral("XGRlBW9FXlekgbPrRHuSiA") + path.mid(1) + salt).toUtf8(),
        QCryptographicHash::Md5).toHex();
    const QUrl media("https://" + host + "/get-mp3/" + QString::fromLatin1(sign) + "/" + ts + path);
    return trustedUrl(media) ? media : invalid(QStringLiteral("invalid signed URL"));
}
}

MusicPlayer::MusicPlayer(QObject *parent) : QObject(parent) {
    m_player.setAudioOutput(&m_audio);
    m_audio.setVolume(0.5f);
    m_mediaTimeout.setSingleShot(true);
    m_mediaTimeout.setInterval(20000);
    connect(&m_mediaTimeout, &QTimer::timeout, this, [this] {
        m_player.stop();
        m_player.setSource({});
        fail(QStringLiteral("Media loading timed out."));
    });
    connect(&m_player, &QMediaPlayer::playbackStateChanged, this, &MusicPlayer::stateChanged);
    connect(&m_player, &QMediaPlayer::positionChanged, this, &MusicPlayer::positionChanged);
    connect(&m_player, &QMediaPlayer::durationChanged, this, &MusicPlayer::durationChanged);
    connect(&m_player, &QMediaPlayer::seekableChanged, this, [this] { applyPendingSeek(); });
    connect(&m_audio, &QAudioOutput::volumeChanged, this, &MusicPlayer::stateChanged);
    connect(&m_player, &QMediaPlayer::errorOccurred, this, [this](QMediaPlayer::Error error, const QString &) {
        if (error != QMediaPlayer::NoError && m_sourceGeneration == m_generation && !m_source.isEmpty()) {
            m_mediaTimeout.stop();
            fail(QStringLiteral("Media playback failed."));
        }
    });
    connect(&m_player, &QMediaPlayer::mediaStatusChanged, this, [this](QMediaPlayer::MediaStatus status) {
        if (m_source.isEmpty() || m_sourceGeneration != m_generation || m_player.source() != m_source) return;
        if (status == QMediaPlayer::LoadedMedia || status == QMediaPlayer::BufferedMedia) {
            m_mediaTimeout.stop();
            m_busy = false;
            applyPendingSeek();
            emit stateChanged();
        } else if (status == QMediaPlayer::EndOfMedia) {
            next();
        }
    });
}

void MusicPlayer::cancelRequests() {
    ++m_generation;
    m_pendingSeek = -1;
    m_source.clear();
    m_trackId.clear();
    m_mediaTimeout.stop();
    const auto replies = m_replies;
    for (auto reply : replies) reply->abort();
}

void MusicPlayer::fail(const QString &message) {
    cancelRequests();
    m_player.stop();
    m_player.setSource({});
    m_busy = false;
    m_error = message;
    emit stateChanged();
}

void MusicPlayer::get(const QUrl &url, bool oauth, quint64 generation,
                      std::function<void(const QByteArray &)> callback) {
    if (!trustedUrl(url) || (oauth && url.host() != "api.music.yandex.net")) {
        fail(QStringLiteral("Untrusted download URL."));
        return;
    }
#ifdef MUSIC_PLAYER_TEST
    if (m_requestHook) { m_requestHook(); return; }
#endif
    QNetworkRequest request(url);
    // Never follow redirects with an OAuth header, even to another HTTPS host.
    request.setAttribute(QNetworkRequest::RedirectPolicyAttribute, QNetworkRequest::ManualRedirectPolicy);
    request.setTransferTimeout(15000);
    if (oauth) request.setRawHeader("Authorization", "OAuth " + m_token);
    auto reply = m_network.get(request);
    m_replies.insert(reply);
    auto timer = new QTimer(reply);
    timer->setSingleShot(true);
    connect(timer, &QTimer::timeout, reply, &QNetworkReply::abort);
    timer->start(15000); // Absolute deadline, not merely an inactivity timeout.
    connect(reply, &QNetworkReply::readyRead, reply, [reply] {
        if (reply->bytesAvailable() > 2 * 1024 * 1024) reply->abort();
    });
    connect(reply, &QNetworkReply::finished, this, [this, reply, generation, callback = std::move(callback)] {
        m_replies.remove(reply);
        reply->deleteLater();
        if (generation != m_generation) return;
        const auto status = reply->attribute(QNetworkRequest::HttpStatusCodeAttribute).toInt();
        if (reply->error() != QNetworkReply::NoError || status != 200 || reply->bytesAvailable() > 2 * 1024 * 1024) {
            // Do not expose URLs, server response bodies, or credentials in errors.
            fail(QStringLiteral("Yandex Music request failed (HTTP %1); check credentials, subscription, or network.").arg(status));
            return;
        }
        callback(reply->readAll());
    });
}

void MusicPlayer::setToken(QString token) {
    cancelRequests();
    m_enabled = false;
    m_busy = false;
    m_token.clear();
    m_player.stop();
    m_player.setSource({});
    m_title.clear(); m_artist.clear(); m_cover.clear(); m_error.clear();
    m_duration = 0;
    emit durationChanged();
    m_history.clear(); m_historyIndex = -1;
    token = token.trimmed();
    if (token.isEmpty()) { emit stateChanged(); return; }
    // OAuth bearer tokens must not contain header delimiters or non-ASCII characters.
    if (token.size() > 4096 || !QRegularExpression(QStringLiteral("\\A[A-Za-z0-9._~+/=-]+\\z")).match(token).hasMatch()) {
        fail(QStringLiteral("Invalid OAuth token format."));
        return;
    }
    m_token = token.toUtf8();
    m_busy = true;
    emit stateChanged();
    get(QUrl(api + "/account/status"), true, m_generation, [this](const QByteArray &data) {
        const auto root = jsonObject(data);
        const auto account = root.value("result").toObject().value("account").toObject();
        if (root.contains("error") || account.value("uid").toVariant().toLongLong() <= 0) {
            m_token.clear();
            fail(QStringLiteral("OAuth account validation failed."));
            return;
        }
        m_enabled = true; m_busy = false;
        emit stateChanged();
    });
}

void MusicPlayer::playTrack(QString trackId) {
    if (!validTrackId(trackId)) { fail(QStringLiteral("Track ID must contain 1–20 ASCII digits.")); return; }
    if (!m_enabled) { fail(QStringLiteral("Set and validate an OAuth token before playing.")); return; }
    if (m_historyIndex < 0 || m_history.value(m_historyIndex) != trackId) {
        m_history = m_history.mid(0, m_historyIndex + 1);
        m_history.append(trackId);
        m_historyIndex = m_history.size() - 1;
    }
    loadTrack(trackId);
}

void MusicPlayer::playAt(QString trackId, QString timestamp) {
    const auto milliseconds = lyricTimestamp(timestamp);
    if (!m_enabled || !validTrackId(trackId) || milliseconds < 0) return;
    if (m_trackId != trackId) playTrack(trackId);
    if (m_trackId != trackId) return;
    m_pendingSeek = milliseconds;
    m_pendingGeneration = m_generation;
    applyPendingSeek();
}

void MusicPlayer::applyPendingSeek() {
    const auto status = m_player.mediaStatus();
    if (m_pendingSeek < 0 || m_pendingGeneration != m_generation
        || m_sourceGeneration != m_generation || m_source.isEmpty()
        || m_player.source() != m_source || !m_player.isSeekable()
        || (status != QMediaPlayer::LoadedMedia && status != QMediaPlayer::BufferedMedia
            && status != QMediaPlayer::EndOfMedia)) return;
    const auto target = m_pendingSeek;
    m_pendingSeek = -1;
    m_player.setPosition(duration() > 0 ? qMin(target, duration()) : target);
    m_player.play();
}

void MusicPlayer::loadTrack(const QString &id) {
    cancelRequests();
    m_player.stop(); m_player.setSource({});
    m_title.clear(); m_artist.clear(); m_cover.clear(); m_error.clear(); m_duration = 0;
    m_trackId = id;
    m_busy = true;
    emit durationChanged();
    emit stateChanged();
    const auto generation = m_generation;
    QUrl url(api + "/tracks");
    QUrlQuery query; query.addQueryItem("trackIds", id); url.setQuery(query);
    get(url, true, generation, [this, id, generation](const QByteArray &data) {
        const auto tracks = jsonObject(data).value("result").toArray();
        if (tracks.isEmpty()) { fail(QStringLiteral("Track metadata is unavailable.")); return; }
        const auto track = tracks.first().toObject();
        if (track.value("id").toVariant().toString() != id || track.value("available").toBool(true) == false) {
            fail(QStringLiteral("Track is unavailable for this account.")); return;
        }
        m_title = track.value("title").toString();
        QStringList artists;
        for (const auto &artist : track.value("artists").toArray()) artists.append(artist.toObject().value("name").toString());
        m_artist = artists.join(", ");
        m_duration = qMax(qint64(0), track.value("durationMs").toVariant().toLongLong());
        emit durationChanged();
        auto cover = track.value("coverUri").toString();
        cover.replace("%%", "400x400");
        if (!cover.isEmpty()) {
            QUrl coverUrl(cover.startsWith("https://") ? cover : "https://" + cover);
            if (trustedUrl(coverUrl)) m_cover = coverUrl.toString();
        }
        emit stateChanged();
        get(QUrl(api + "/tracks/" + id + "/download-info"), true, generation,
            [this, generation](const QByteArray &data) {
            QUrl download;
            int bestBitrate = -1;
            for (const auto &entry : jsonObject(data).value("result").toArray()) {
                const auto info = entry.toObject();
                const QUrl candidate(info.value("downloadInfoUrl").toString());
                const int bitrate = info.value("bitrateInKbps").toInt();
                if (info.value("codec").toString() == "mp3" && !info.value("preview").toBool()
                    && trustedUrl(candidate) && bitrate > bestBitrate) {
                    download = candidate; bestBitrate = bitrate;
                }
            }
            if (download.isEmpty()) { fail(QStringLiteral("No full MP3 stream is available.")); return; }
            get(download, false, generation, [this](const QByteArray &xml) {
                QString descriptorError;
                const auto media = mediaUrl(xml, &descriptorError);
                if (media.isEmpty()) { fail(descriptorError); return; }
                // QMediaPlayer receives only a signed URL, never the OAuth request/header.
                m_source = media;
                m_sourceGeneration = m_generation;
                m_mediaTimeout.start();
                m_player.setSource(media);
                if (m_pendingSeek < 0 && !m_source.isEmpty()) m_player.play();
                else applyPendingSeek();
                emit stateChanged();
            });
        });
    });
}

void MusicPlayer::toggle() {
    if (!m_enabled || m_player.source().isEmpty()) return;
    if (playing()) m_player.pause(); else m_player.play();
}
void MusicPlayer::seek(qint64 milliseconds) {
    if (m_enabled && m_player.isSeekable()) m_player.setPosition(qBound(qint64(0), milliseconds, duration()));
}
void MusicPlayer::setVolume(double volume) {
    if (std::isfinite(volume)) m_audio.setVolume(float(qBound(0.0, volume, 1.0)));
}
void MusicPlayer::next() {
    if (m_enabled && m_historyIndex + 1 < m_history.size()) loadTrack(m_history.at(++m_historyIndex));
}
void MusicPlayer::previous() {
    if (m_enabled && m_historyIndex > 0) loadTrack(m_history.at(--m_historyIndex));
}

#ifdef MUSIC_PLAYER_TEST
#include <QtTest>
#include <QTemporaryFile>
#include <QDataStream>
class MusicPlayerTests final : public QObject {
    Q_OBJECT
private slots:
    void inputValidation() {
        QVERIFY(validTrackId("123456"));
        for (const auto &id : {QString(), QString("1:2"), QString("../1"), QString("12\n"), QString("１２")})
            QVERIFY(!validTrackId(id));
        QVERIFY(trustedUrl(QUrl("https://storage.mds.yandex.net/file")));
        QVERIFY(!trustedUrl(QUrl("http://storage.mds.yandex.net/file")));
        QVERIFY(!trustedUrl(QUrl("https://yandex.net.evil.test/file")));
        QVERIFY(!trustedUrl(QUrl("https://user@storage.mds.yandex.net/file")));
    }
    void downloadDescriptors() {
        const auto xml = QByteArray("<download-info><host>storage.mds.yandex.net</host><path>/file.mp3</path><ts>123</ts><s>salt</s></download-info>");
        const auto json = QByteArray(R"({"host":"storage.mds.yandex.net","path":"/file.mp3","ts":"123","s":"salt"})");
        QVERIFY(!mediaUrl(xml).isEmpty());
        QCOMPARE(mediaUrl(json), mediaUrl(xml));
        QCOMPARE(mediaUrl(QByteArray(R"({"host":"storage.mds.yandex.net","path":"/file.mp3","ts":123,"s":"salt"})")), mediaUrl(xml));
        QVERIFY(mediaUrl("<download-info><host>evil.test</host></download-info>").isEmpty());
        QVERIFY(mediaUrl("not a descriptor").isEmpty());
    }
    void downloadDescriptorQuery() {
        // Synthetic storage-shaped fixture; the expected digest is independently
        // calculated with Python hashlib using the upstream DownloadInfo formula.
        const QByteArray path = "/data/136146_d158926e.14534319.6.10994777/320?track-id=10994777&from=service&token=a%2Fb%3D";
        const auto json = QJsonDocument(QJsonObject{{"host", "storage.mds.yandex.net"},
            {"path", QString::fromLatin1(path)}, {"ts", "1730000000"}, {"s", "sample-salt"}}).toJson();
        auto escapedPath = path;
        escapedPath.replace("&", "&amp;");
        const auto xml = QByteArray("<download-info><host>storage.mds.yandex.net</host><path>")
            + escapedPath + "</path><ts>1730000000</ts><s>sample-salt</s></download-info>";
        const auto expected = QByteArray("https://storage.mds.yandex.net/get-mp3/99b451638fd216edd9fd4b2a2373b981/1730000000") + path;
        QString error;
        QCOMPARE(mediaUrl(json, &error).toEncoded(), expected);
        QVERIFY(error.isEmpty());
        QCOMPARE(mediaUrl(xml).toEncoded(), expected);
        QVERIFY(mediaUrl(json).hasQuery());
        QVERIFY(mediaUrl(json).query(QUrl::FullyEncoded).contains("token=a%2Fb%3D"));
    }
    void opaqueDownloadTimestamp() {
        const QString ts = "65f1aBc0";
        const QJsonObject descriptor{{"host", "storage.mds.yandex.net"}, {"path", "/file.mp3"},
                                     {"ts", ts}, {"s", "salt"}};
        const auto json = QJsonDocument(descriptor).toJson();
        const QByteArray xml("<download-info><host>storage.mds.yandex.net</host><path>/file.mp3</path><ts>65f1aBc0</ts><s>salt</s></download-info>");
        QString error;
        const auto url = mediaUrl(json, &error);
        QVERIFY2(!url.isEmpty(), qPrintable(error));
        QVERIFY(url.path().endsWith("/" + ts + "/file.mp3"));
        QCOMPARE(mediaUrl(xml), url);
        auto decimal = descriptor;
        decimal.insert("ts", "1730000000");
        // ts does not participate in the MP3 signature.
        QCOMPARE(mediaUrl(QJsonDocument(decimal).toJson()).path().section('/', 2, 2),
                 url.path().section('/', 2, 2));
    }
    void invalidDownloadDescriptors() {
        const QJsonObject valid{{"host", "storage.mds.yandex.net"}, {"path", "/file.mp3?x=1"},
                                {"ts", "1730000000"}, {"s", "sample-salt"}};
        const auto rejects = [&valid](const QString &field, const QString &value, const QString &reason) {
            auto descriptor = valid;
            descriptor.insert(field, value);
            QString error;
            QVERIFY(mediaUrl(QJsonDocument(descriptor).toJson(), &error).isEmpty());
            QCOMPARE(error, QStringLiteral("Invalid download descriptor: %1.").arg(reason));
        };
        for (const auto &host : {"", "evil.test", "yandex.net.evil.test", "user@storage.mds.yandex.net",
                                 "storage.mds.yandex.net:444", "storage.mds.yandex.net/path",
                                 "storage.mds.yandex.net?secret=1", "storage.mds.yandex.net#fragment"})
            rejects("host", host, "missing or untrusted host");
        for (const auto &path : {"", "file.mp3", "/file.mp3?secret=1#fragment"})
            rejects("path", path, "path must start with '/' and have no fragment");
        for (const auto &ts : {"", "123/456", "123?x=1", "123#fragment", "123\n", "123%2F456"})
            rejects("ts", ts, "ts must be a nonempty safe URL segment");
        rejects("s", "", "missing s");
        QString error;
        QVERIFY(mediaUrl("<download-info><host>", &error).isEmpty());
        QCOMPARE(error, QStringLiteral("Invalid download descriptor: malformed XML or JSON."));
        QVERIFY(!mediaUrl(QJsonDocument(valid).toJson(), &error).isEmpty());
        QVERIFY(error.isEmpty());
    }
    void lyricTimestamps() {
        QCOMPARE(lyricTimestamp("00:00"), qint64(0));
        QCOMPARE(lyricTimestamp("12:34.5"), qint64(754500));
        QCOMPARE(lyricTimestamp("01:02:03.0456"), qint64(3723045));
        QCOMPARE(lyricTimestamp("120:00"), qint64(7200000));
        for (const auto &value : {QString(), QString("null"), QString("1:2"), QString("00:60"),
             QString("1:60:00"), QString("-1:00"), QString("00:01."), QString("00:01\n"),
             QString(" 00:01"), QString("99999999999999999999:00")})
            QCOMPARE(lyricTimestamp(value), qint64(-1));
    }
    void loadedLyricClick() {
        QTemporaryFile file;
        QVERIFY(file.open());
        QDataStream stream(&file);
        stream.setByteOrder(QDataStream::LittleEndian);
        const QByteArray samples(8000 * 2 * 3, '\0');
        stream.writeRawData("RIFF", 4); stream << quint32(36 + samples.size());
        stream.writeRawData("WAVEfmt ", 8); stream << quint32(16) << quint16(1)
            << quint16(1) << quint32(8000) << quint32(16000) << quint16(2) << quint16(16);
        stream.writeRawData("data", 4); stream << quint32(samples.size());
        stream.writeRawData(samples.constData(), samples.size());
        file.flush();
        MusicPlayer player;
        int requests = 0;
        player.m_requestHook = [&] { ++requests; };
        player.m_enabled = true;
        player.playAt("1", "00:01");
        player.m_source = QUrl::fromLocalFile(file.fileName());
        player.m_sourceGeneration = player.m_generation;
        player.m_player.setSource(player.m_source);
        QTRY_VERIFY(player.m_player.isSeekable());
        QTRY_VERIFY(player.playing());
        QCOMPARE(player.m_pendingSeek, qint64(-1));
        player.m_player.pause();
        const auto generation = player.m_generation;
        player.playAt("1", "00:02");
        QCOMPARE(requests, 1);
        QCOMPARE(player.m_generation, generation);
        QTRY_VERIFY(player.playing());
        QVERIFY(player.position() >= 2000);
        QCOMPARE(player.m_pendingSeek, qint64(-1));
    }
    void pendingLyricClicks() {
        MusicPlayer player;
        int requests = 0;
        player.m_requestHook = [&] { ++requests; };
        player.playAt("1", "00:01");
        QCOMPARE(requests, 0);
        player.m_enabled = true;
        player.playAt("1", "00:01");
        QCOMPARE(requests, 1);
        QCOMPARE(player.trackId(), QString("1"));
        QCOMPARE(player.m_pendingSeek, qint64(1000));
        const auto generation = player.m_generation;
        player.playAt("1", "00:02.5");
        QCOMPARE(requests, 1);
        QCOMPARE(player.m_generation, generation);
        QCOMPARE(player.m_pendingSeek, qint64(2500));
        player.playAt("2", "01:00");
        QCOMPARE(requests, 2);
        QVERIFY(player.m_generation != generation);
        QCOMPARE(player.trackId(), QString("2"));
        QCOMPARE(player.m_pendingGeneration, player.m_generation);
        QCOMPARE(player.m_pendingSeek, qint64(60000));
        player.playAt("3", "invalid");
        QCOMPARE(requests, 2);
        QCOMPARE(player.trackId(), QString("2"));
        player.applyPendingSeek();
        QCOMPARE(player.m_pendingSeek, qint64(60000));
        QVERIFY(!player.playing());
        player.setToken({});
        QVERIFY(player.trackId().isEmpty());
        QCOMPARE(player.m_pendingSeek, qint64(-1));
        player.m_enabled = true;
        player.playAt("4", "00:03");
        player.fail("test failure");
        QVERIFY(player.trackId().isEmpty());
        QCOMPARE(player.m_pendingSeek, qint64(-1));
    }
    void publicState() {
        MusicPlayer player;
        QVERIFY(!player.enabled()); QVERIFY(!player.busy()); QVERIFY(!player.playing());
        QVERIFY(player.metaObject()->indexOfProperty("token") == -1);
        player.setToken("bad\r\nheader");
        QVERIFY(!player.enabled()); QVERIFY(!player.busy()); QVERIFY(!player.error().isEmpty());
        player.setToken({}); QVERIFY(player.error().isEmpty());
        player.playTrack("1:2"); QVERIFY(!player.busy()); QVERIFY(!player.error().isEmpty());
        player.setVolume(2); QCOMPARE(player.volume(), 1.0);
        player.setVolume(-1); QCOMPARE(player.volume(), 0.0);
        player.setVolume(std::nan("")); QCOMPARE(player.volume(), 0.0);
        player.next(); player.previous(); player.toggle(); player.seek(-1);
        QVERIFY(!player.playing());
    }
};
QTEST_MAIN(MusicPlayerTests)
#include "player.moc"
#endif

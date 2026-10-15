#include "player.h"

#include <QAudioBuffer>
#include <QCryptographicHash>
#include <QClipboard>
#include <QGuiApplication>
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
        if (error) *error = QStringLiteral("Invalid download descriptor: %1").arg(reason);
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
    m_spectrumClock.start();
    m_spectrumTimer.setTimerType(Qt::PreciseTimer);
    m_spectrumTimer.setInterval(std::chrono::microseconds(16667));
    connect(&m_spectrumTimer, &QChronoTimer::timeout, this, &MusicPlayer::updateSpectrum);
    resetSpectrum();
    connect(&m_player, &QMediaPlayer::playbackStateChanged, this, [this] {
        if (!playing()) m_spectrumTap.clear();
    });
    m_audio.setVolume(0.5f);
    m_mediaTimeout.setSingleShot(true);
    m_mediaTimeout.setInterval(20000);
    connect(&m_mediaTimeout, &QTimer::timeout, this, [this] {
        m_player.stop();
        m_player.setSource({});
        fail(QStringLiteral("Media loading timed out"));
    });
    connect(&m_player, &QMediaPlayer::playbackStateChanged, this, &MusicPlayer::stateChanged);
    connect(&m_player, &QMediaPlayer::positionChanged, this, &MusicPlayer::positionChanged);
    connect(&m_player, &QMediaPlayer::durationChanged, this, &MusicPlayer::durationChanged);
    connect(&m_player, &QMediaPlayer::seekableChanged, this, [this] { applyPendingSeek(); });
    connect(&m_audio, &QAudioOutput::volumeChanged, this, &MusicPlayer::stateChanged);
    connect(&m_player, &QMediaPlayer::errorOccurred, this, [this](QMediaPlayer::Error error, const QString &) {
        if (error != QMediaPlayer::NoError && m_sourceGeneration == m_generation && !m_source.isEmpty()) {
            m_mediaTimeout.stop();
            fail(QStringLiteral("Media playback failed"));
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

void MusicPlayer::setVisualizerEnabled(bool enabled) {
    if (enabled == m_visualizerEnabled) return;
    m_visualizerEnabled = enabled;
    resetSpectrum();
    if (enabled) m_spectrumTimer.start();
    else m_spectrumTimer.stop();
    emit visualizerEnabledChanged();
}

void MusicPlayer::attachSpectrumOutput() {
    m_player.setAudioBufferOutput(nullptr);
    m_bufferOutput.reset();
    if (!m_visualizerEnabled) return;
    m_bufferOutput = std::make_unique<QAudioBufferOutput>();
    const auto epoch = m_spectrumEpoch;
    connect(m_bufferOutput.get(), &QAudioBufferOutput::audioBufferReceived, this,
            [this, epoch](const QAudioBuffer &buffer) {
        if (epoch == m_spectrumEpoch) receiveSpectrumBuffer(buffer);
    });
    m_player.setAudioBufferOutput(m_bufferOutput.get());
}

void MusicPlayer::resetSpectrum() {
    ++m_spectrumEpoch;
    m_spectrumSeekFloor = -1;
    m_spectrumTap.clear();
    m_spectrumAnalyser.reset();
    QVariantList zeros;
    for (int i = 0; i < Spectrum::Bands; ++i) zeros.append(0.0f);
    if (m_spectrumLevels != zeros) { m_spectrumLevels = zeros; emit spectrumLevelsChanged(); }
    if (m_spectrumPeaks != zeros) { m_spectrumPeaks = zeros; emit spectrumPeaksChanged(); }
    // A new tap identity also rejects queued delivery from the previous source/seek.
    attachSpectrumOutput();
}

void MusicPlayer::receiveSpectrumBuffer(const QAudioBuffer &buffer) {
    if (!m_visualizerEnabled || !playing() || m_source.isEmpty()
        || m_sourceGeneration != m_generation || m_player.source() != m_source) return;
    if (!buffer.isValid() || buffer.frameCount() == 0) { m_spectrumTap.clear(); return; }
    if (m_spectrumSeekFloor >= 0) {
        // Reject old decoder packets, including backward-seek packets from ahead
        // of the new position. Buffers can straddle the requested seek point.
        if (buffer.startTime() < 0 || buffer.startTime() + buffer.duration() <= m_spectrumSeekFloor
            || buffer.startTime() > m_spectrumSeekFloor + 250000) return;
        m_spectrumSeekFloor = -1;
    }
    const auto format = buffer.format();
    const int channels = format.channelCount();
    if (channels <= 0 || format.sampleRate() <= 0) return;
    const char *data = buffer.constData<char>();
    const int bytes = format.bytesPerSample();
    if (bytes <= 0) return;
    // Qt's normalizedSampleValue handles UInt8, Int16, Int32 and Float PCM.
    // QAudioBufferOutput delivers decoded samples independently of output volume.
    for (int frame = 0; frame < buffer.frameCount(); ++frame) {
        double sum = 0.0;
        for (int channel = 0; channel < channels; ++channel)
            sum += format.normalizedSampleValue(data + (frame * channels + channel) * bytes);
        const float mono = float(sum) / float(channels);
        m_spectrumTap.push(std::isfinite(mono) ? mono : 0.0f, format.sampleRate());
    }
}

void MusicPlayer::updateSpectrum() {
    if (!m_visualizerEnabled) return;
    m_spectrumAnalyser.step(playing() ? m_spectrumTap.window() : Spectrum::Wave{},
                            double(m_spectrumClock.nsecsElapsed()) / 1e9);
    QVariantList levels, peaks;
    for (int i = 0; i < Spectrum::Bands; ++i) {
        levels.append(m_spectrumAnalyser.levels[i]);
        peaks.append(m_spectrumAnalyser.peaks[i]);
    }
    if (levels != m_spectrumLevels) { m_spectrumLevels = levels; emit spectrumLevelsChanged(); }
    if (peaks != m_spectrumPeaks) { m_spectrumPeaks = peaks; emit spectrumPeaksChanged(); }
}

void MusicPlayer::cancelRequests() {
    ++m_generation;
    resetSpectrum();
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
        fail(QStringLiteral("Untrusted download URL"));
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
            fail(QStringLiteral("Yandex Music request failed (HTTP %1); check credentials, subscription, or network").arg(status));
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
    m_metadata.clear();
    m_duration = 0;
    emit durationChanged();
    m_history.clear(); m_historyIndex = -1;
    token = token.trimmed();
    if (token.isEmpty()) { emit stateChanged(); return; }
    // OAuth bearer tokens must not contain header delimiters or non-ASCII characters.
    if (token.size() > 4096 || !QRegularExpression(QStringLiteral("\\A[A-Za-z0-9._~+/=-]+\\z")).match(token).hasMatch()) {
        fail(QStringLiteral("Invalid OAuth token format"));
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
            fail(QStringLiteral("OAuth account validation failed"));
            return;
        }
        m_enabled = true; m_busy = false;
        emit stateChanged();
    });
}

void MusicPlayer::playFromClipboard() {
    if (!m_enabled || m_busy) return;
    const QString text = QGuiApplication::clipboard()->text().trimmed();
    QString id = text;
    if (!validTrackId(id)) {
        const QUrl url(text, QUrl::StrictMode);
        if (!url.isValid() || (url.scheme() != "https" && url.scheme() != "http") || url.host().isEmpty()) return;
        static const QRegularExpression trackPath(QStringLiteral("/track/([0-9]{1,20})(?:/|$)"));
        const auto match = trackPath.match(url.path());
        if (!match.hasMatch()) return;
        id = match.captured(1);
    }
    if (id != m_trackId) playTrack(id);
}

void MusicPlayer::playTrack(QString trackId) {
    if (!validTrackId(trackId)) { fail(QStringLiteral("Track ID must contain 1-20 ASCII digits")); return; }
    if (!m_enabled) { fail(QStringLiteral("Set and validate an OAuth token before playing")); return; }
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
    resetSpectrum();
    m_spectrumSeekFloor = (duration() > 0 ? qMin(target, duration()) : target) * 1000;
    m_player.setPosition(duration() > 0 ? qMin(target, duration()) : target);
    m_player.play();
}

void MusicPlayer::loadTrack(const QString &id) {
    cancelRequests();
    m_player.stop(); m_player.setSource({});
    m_title.clear(); m_artist.clear(); m_cover.clear(); m_error.clear(); m_duration = 0;
    m_metadata.clear();
    m_trackId = id;
    m_busy = true;
    emit durationChanged();
    emit stateChanged();
    const auto generation = m_generation;
    QUrl url(api + "/tracks");
    QUrlQuery query; query.addQueryItem("trackIds", id); url.setQuery(query);
    get(url, true, generation, [this, id, generation](const QByteArray &data) {
        const auto tracks = jsonObject(data).value("result").toArray();
        if (tracks.isEmpty()) { fail(QStringLiteral("Track metadata is unavailable")); return; }
        const auto track = tracks.first().toObject();
        if (track.value("id").toVariant().toString() != id || track.value("available").toBool(true) == false) {
            fail(QStringLiteral("Track is unavailable for this account")); return;
        }
        m_metadata = {{"title", track.value("title").toVariant()},
                      {"artists", track.value("artists").toArray().toVariantList()},
                      {"coverUri", track.value("coverUri").toVariant()},
                      {"durationMs", track.value("durationMs").toVariant()}};
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
            if (download.isEmpty()) { fail(QStringLiteral("No full MP3 stream is available")); return; }
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

QUrl MusicPlayer::streamUrl() const {
    // Track changes invalidate the source and generation together in cancelRequests().
    if (!m_enabled || m_trackId.isEmpty() || m_sourceGeneration != m_generation
        || m_source.isEmpty() || !m_source.isValid()) return {};
    return m_source;
}

void MusicPlayer::pause() {
    if (playing()) m_player.pause();
}

void MusicPlayer::toggle() {
    if (!m_enabled || m_player.source().isEmpty()) return;
    if (playing()) m_player.pause(); else m_player.play();
}
void MusicPlayer::seek(qint64 milliseconds) {
    if (m_enabled && m_player.isSeekable()) {
        const auto target = qBound(qint64(0), milliseconds, duration());
        resetSpectrum();
        m_spectrumSeekFloor = target * 1000;
        m_player.setPosition(target);
    }
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
            QCOMPARE(error, QStringLiteral("Invalid download descriptor: %1").arg(reason));
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
        QCOMPARE(error, QStringLiteral("Invalid download descriptor: malformed XML or JSON"));
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
        player.setVisualizerEnabled(true);
        QAudioFormat format;
        format.setSampleRate(1000);
        format.setChannelCount(2);
        format.setSampleFormat(QAudioFormat::Float);
        QByteArray pcm(1000 * 2 * int(sizeof(float)), Qt::Uninitialized);
        auto *stereo = reinterpret_cast<float *>(pcm.data());
        for (int i = 0; i < 1000; ++i) { stereo[2 * i] = 0.5f; stereo[2 * i + 1] = 0.0f; }
        player.setVolume(0.0);
        player.receiveSpectrumBuffer(QAudioBuffer(pcm, format, 1000000));
        QCOMPARE(player.m_spectrumTap.window().back(), 0.25f);
        player.updateSpectrum();
        QVERIFY(!player.m_spectrumAnalyser.settled());
        const auto levelsBeforePause = player.spectrumLevels();
        const auto peaksBeforePause = player.spectrumPeaks();
        const auto epochBeforePause = player.m_spectrumEpoch;
        const auto sourceBeforePause = player.streamUrl();
        QVERIFY(!sourceBeforePause.isEmpty());
        player.pause();
        QCOMPARE(player.m_player.playbackState(), QMediaPlayer::PausedState);
        player.pause();
        QCOMPARE(player.m_player.playbackState(), QMediaPlayer::PausedState);
        QCOMPARE(player.streamUrl(), sourceBeforePause);
        QVERIFY(!player.m_spectrumAnalyser.settled());
        QCOMPARE(player.spectrumLevels(), levelsBeforePause);
        QCOMPARE(player.spectrumPeaks(), peaksBeforePause);
        QCOMPARE(player.m_spectrumEpoch, epochBeforePause);
        QCOMPARE(player.m_spectrumTap.window(), Spectrum::Wave{});
        QTRY_VERIFY_WITH_TIMEOUT(player.m_spectrumAnalyser.settled(), 3000);
        for (const auto &level : player.spectrumLevels()) QCOMPARE(level.toFloat(), 0.0f);
        for (const auto &peak : player.spectrumPeaks()) QCOMPARE(peak.toFloat(), 0.0f);
        const auto generation = player.m_generation;
        player.playAt("1", "00:02");
        QCOMPARE(requests, 1);
        QCOMPARE(player.m_generation, generation);
        QTRY_VERIFY(player.playing());
        QVERIFY(player.position() >= 2000);
        QCOMPARE(player.m_pendingSeek, qint64(-1));
        player.receiveSpectrumBuffer(QAudioBuffer(pcm, format, 2000000));
        player.updateSpectrum();
        QVERIFY(!player.m_spectrumAnalyser.settled());
        const auto levelsBeforeStop = player.spectrumLevels();
        const auto peaksBeforeStop = player.spectrumPeaks();
        const auto epochBeforeStop = player.m_spectrumEpoch;
        player.m_player.stop();
        QCOMPARE(player.spectrumLevels(), levelsBeforeStop);
        QCOMPARE(player.spectrumPeaks(), peaksBeforeStop);
        QCOMPARE(player.m_spectrumEpoch, epochBeforeStop);
        QCOMPARE(player.m_spectrumTap.window(), Spectrum::Wave{});
        QVERIFY(!player.m_spectrumAnalyser.settled());
        QTRY_VERIFY_WITH_TIMEOUT(player.m_spectrumAnalyser.settled(), 3000);
        for (const auto &level : player.spectrumLevels()) QCOMPARE(level.toFloat(), 0.0f);
        for (const auto &peak : player.spectrumPeaks()) QCOMPARE(peak.toFloat(), 0.0f);
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
    void spectrumReference() {
        Spectrum::Wave tone{}, silence{};
        for (int i = 0; i < Spectrum::Samples; ++i)
            tone[i] = 0.8f * std::sin(float(i) / 44100.0f * 1000.0f * Spectrum::Tau);
        Spectrum::Fft fft;
        const auto bins = fft.spectrum(tone);
        QCOMPARE(int(std::max_element(bins.begin(), bins.end()) - bins.begin()), 12);
        Spectrum::WideAnalyser analyser;
        analyser.step(tone, 0.0);
        const int band = int(std::max_element(analyser.levels.begin(), analyser.levels.end()) - analyser.levels.begin());
        QVERIFY(analyser.levels[band] > 0.99f);
        QVERIFY(analyser.levels.front() < 0.3f);
        QVERIFY(analyser.levels.back() < 0.3f);
        const float top = analyser.peaks[band];
        for (int i = 1; i <= 12; ++i) analyser.step(silence, i * double(Spectrum::Step));
        QCOMPARE(analyser.peaks[band], top);
        QVERIFY(analyser.levels[band] < top);
        for (int i = 13; i < 120; ++i) {
            analyser.step(silence, i * double(Spectrum::Step));
            QVERIFY(analyser.peaks[band] >= analyser.levels[band]);
        }
        QVERIFY(analyser.settled());
        Spectrum::WideAnalyser fast, slow;
        fast.step(tone, 0.0); slow.step(tone, 0.0);
        for (int i = 1; i <= 12; ++i) fast.step(silence, i * 0.01);
        for (int i = 1; i <= 3; ++i) slow.step(silence, i * 0.04);
        for (int i = 0; i < Spectrum::Bands; ++i)
            QVERIFY(std::abs(fast.levels[i] - slow.levels[i]) < 0.00001f);
        fast.reset(); fast.step(silence, 1.0); QVERIFY(fast.settled());
    }
    void spectrumTapAndLifecycle() {
        Spectrum::AudioTap tap;
        for (int i = 0; i < 1000; ++i) tap.push(float(i), 1000);
        const auto wave = tap.window();
        QCOMPARE(wave.back(), 849.0f);
        QCOMPARE(wave[161], 0.0f);
        QCOMPARE(wave[162], 500.0f);
        tap.clear(); QCOMPARE(tap.window(), Spectrum::Wave{});
        MusicPlayer player;
        QVERIFY(!player.visualizerEnabled());
        QVERIFY(!player.m_bufferOutput);
        QCOMPARE(player.spectrumLevels().size(), Spectrum::Bands);
        player.setVisualizerEnabled(true);
        QVERIFY(player.m_bufferOutput);
        QVERIFY(player.m_spectrumTimer.isActive());
        const auto epoch = player.m_spectrumEpoch;
        player.m_spectrumTap.push(1.0f, 1);
        player.m_spectrumAnalyser.levels.fill(1.0f);
        player.setToken({});
        QVERIFY(player.m_spectrumEpoch > epoch);
        QVERIFY(player.m_spectrumAnalyser.settled());
        QCOMPARE(player.m_spectrumTap.window(), Spectrum::Wave{});
        player.setVisualizerEnabled(false);
        QVERIFY(!player.m_bufferOutput);
        QVERIFY(!player.m_spectrumTimer.isActive());
    }
    void streamUrlLifecycle() {
        MusicPlayer player;
        player.m_requestHook = [] {};
        QVERIFY(player.streamUrl().isEmpty());
        const auto property = player.metaObject()->property(player.metaObject()->indexOfProperty("streamUrl"));
        QCOMPARE(property.metaType(), QMetaType::fromType<QUrl>());
        QVERIFY(property.isReadable());
        QVERIFY(!property.isWritable());
        QCOMPARE(property.notifySignal().name(), QByteArray("stateChanged"));
        QVERIFY(player.metaObject()->indexOfMethod("pause()") >= 0);
        player.pause();
        QCOMPARE(player.m_player.playbackState(), QMediaPlayer::StoppedState);

        player.m_enabled = true;
        player.playTrack("1");
        QVERIFY(player.streamUrl().isEmpty());
        const QUrl source("https://storage.mds.yandex.net/current.mp3");
        player.m_source = source;
        player.m_sourceGeneration = player.m_generation;
        QVERIFY(player.streamUrl() == source);
        --player.m_sourceGeneration;
        QVERIFY(player.streamUrl().isEmpty());
        player.m_sourceGeneration = player.m_generation;
        player.m_source = QUrl("https://[");
        QVERIFY(player.streamUrl().isEmpty());
        player.m_source = source;
        player.m_trackId.clear();
        QVERIFY(player.streamUrl().isEmpty());
        player.m_trackId = "1";

        QSignalSpy changes(&player, &MusicPlayer::stateChanged);
        player.playTrack("2");
        QVERIFY(!changes.isEmpty());
        QCOMPARE(player.trackId(), QString("2"));
        QVERIFY(player.m_source.isEmpty());
        QVERIFY(player.streamUrl().isEmpty());
        player.m_source = source;
        player.m_sourceGeneration = player.m_generation;
        QVERIFY(player.streamUrl() == source);
        changes.clear();
        player.setToken({});
        QVERIFY(!changes.isEmpty());
        QVERIFY(player.m_source.isEmpty());
        QVERIFY(player.streamUrl().isEmpty());
        QVERIFY(player.trackId().isEmpty());

        player.m_enabled = true;
        player.playTrack("3");
        player.m_source = source;
        player.m_sourceGeneration = player.m_generation;
        player.fail("test failure");
        QVERIFY(player.m_source.isEmpty());
        QVERIFY(player.streamUrl().isEmpty());
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

#include "uploader.h"
#include <QDir>
#include <QCryptographicHash>
#include <QHash>
#include <QFile>
#include <QFileInfo>
#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>
#include <QLockFile>
#include <QNetworkAccessManager>
#include <QNetworkReply>
#include <QEventLoop>
#include <QProcess>
#include <QProcessEnvironment>
#include <QRegularExpression>
#include <QSaveFile>
#include <QStringDecoder>
#include <QTimer>
#include <QTemporaryFile>
#include <QElapsedTimer>
#include <QUrl>
#include <memory>
#include <cmath>
#include <QSet>
#include <stdexcept>

namespace {
constexpr qint64 maxAudio = 128 * 1024 * 1024;
struct Result { QString action, error, status; };
void require(bool ok, const QString &message) {
    if (!ok) throw std::runtime_error(message.toStdString());
}
QByteArray read(const QString &path) {
    if (!QFileInfo::exists(path)) return {};
    QFile file(path);
    require(file.open(QIODevice::ReadOnly), "Cannot read " + path);
    constexpr qint64 maximumMetadata = 8 * 1024 * 1024;
    auto bytes = file.read(maximumMetadata + 1);
    require(bytes.size() <= maximumMetadata, "Metadata exceeds 8 MiB: " + path);
    require(file.error() == QFileDevice::NoError, "Cannot read " + path);
    return bytes;
}
void write(const QString &path, const QByteArray &bytes) {
    QSaveFile file(path);
    file.setDirectWriteFallback(false);
    require(file.open(QIODevice::WriteOnly) && file.write(bytes) == bytes.size() && file.commit(),
            "Cannot save " + path);
}
struct Snapshot {
    bool exists = false;
    QByteArray digest;
    bool operator==(const Snapshot &other) const { return exists == other.exists && digest == other.digest; }
};
Snapshot snapshot(const QString &path) {
    QFileInfo info(path);
    require(!info.isSymLink(), "Upload target became a symlink");
    if (!info.exists()) return {};
    require(info.isFile() && info.size() <= maxAudio, "Upload target is not a bounded regular file");
    QFile file(path);
    require(file.open(QIODevice::ReadOnly), "Cannot inspect upload target");
    QCryptographicHash hash(QCryptographicHash::Sha256);
    qint64 total = 0;
    while (!file.atEnd()) {
        const auto bytes = file.read(64 * 1024);
        require(file.error() == QFileDevice::NoError, "Cannot inspect upload target");
        total += bytes.size();
        require(total <= maxAudio, "Upload target exceeds 128 MiB");
        hash.addData(bytes);
    }
    return {true, hash.result()};
}
QString decode(const QByteArray &bytes, const QString &name) {
    QStringDecoder decoder(QStringDecoder::Utf8, QStringConverter::Flag::ConvertInitialBom |
                                                      QStringConverter::Flag::Stateless);
    const QString text = decoder(bytes);
    require(!decoder.hasError(), "Invalid UTF-8 in " + name + "; original preserved");
    return text;
}
QString newline(const QString &text) {
    int crlf = 0, lf = 0, cr = 0;
    for (qsizetype i = 0; i < text.size(); ++i) {
        if (text[i] == '\r') {
            if (i + 1 < text.size() && text[i + 1] == '\n') { ++crlf; ++i; }
            else ++cr;
        } else if (text[i] == '\n') ++lf;
    }
    if (crlf > 0 && crlf >= lf && crlf >= cr) return "\r\n";
    if (cr > lf) return "\r";
    return "\n";
}
QByteArray git(const QString &root, const QStringList &args) {
    QProcess process;
    auto env = QProcessEnvironment::systemEnvironment();
    // Do not inherit an alternate repository/index or interactive credential UI.
    for (const auto &key : env.keys()) if (key.startsWith("GIT_")) env.remove(key);
    env.insert("GIT_TERMINAL_PROMPT", "0");
    env.insert("GIT_EDITOR", "true");
    env.insert("GIT_PAGER", "cat");
    env.insert("GIT_OPTIONAL_LOCKS", "0");
    process.setProcessEnvironment(env);
    process.setWorkingDirectory(root);
    process.start("git", QStringList{"--no-pager", "-c", "core.hooksPath=/dev/null",
                                         "-c", "commit.gpgSign=false", "-c", "core.fsmonitor=false"} + args);
    require(process.waitForStarted(3000), "Cannot start Git");
    if (!process.waitForFinished(15000)) {
        process.kill(); process.waitForFinished(3000);
        require(false, "Git timed out; inspect repository state before retrying");
    }
    require(process.exitStatus() == QProcess::NormalExit && process.exitCode() == 0,
            "Git failed: " + QString::fromUtf8(process.readAllStandardError()).trimmed());
    return process.readAllStandardOutput();
}
QByteArray download(const QUrl &url) {
    QNetworkAccessManager network;
    QNetworkRequest request(url);
    request.setAttribute(QNetworkRequest::RedirectPolicyAttribute, QNetworkRequest::NoLessSafeRedirectPolicy);
    request.setTransferTimeout(30000);
    auto reply = network.get(request);
    reply->setReadBufferSize(maxAudio + 1);
    QEventLoop loop;
    QTimer timer;
    timer.setSingleShot(true);
    QByteArray bytes;
    bool tooLarge = false, timedOut = false;
    QObject::connect(reply, &QNetworkReply::readyRead, &loop, [&] {
        bytes += reply->read(maxAudio + 1 - bytes.size());
        if (bytes.size() > maxAudio) { tooLarge = true; reply->abort(); }
    });
    QObject::connect(&timer, &QTimer::timeout, &loop, [&] { timedOut = true; reply->abort(); });
    QObject::connect(reply, &QNetworkReply::finished, &loop, &QEventLoop::quit);
    timer.start(30000);
    if (!reply->isFinished()) loop.exec();
    bytes += reply->read(maxAudio + 1 - bytes.size());
    require(!timedOut, "Download timed out");
    require(!tooLarge && bytes.size() <= maxAudio, "Download exceeds 128 MiB");
    const int code = reply->attribute(QNetworkRequest::HttpStatusCodeAttribute).toInt();
    require(reply->error() == QNetworkReply::NoError && code >= 200 && code < 300,
            "Download failed (HTTP " + QString::number(code) + ")");
    require(!bytes.isEmpty(), "Download is empty");
    return bytes;
}
QString trackID(QJsonValue value) {
    if (value.isObject()) value = value.toObject().value("id");
    if (value.isString()) return value.toString();
    if (value.isDouble()) return QString::fromUtf8(QJsonDocument(QJsonArray{value}).toJson(QJsonDocument::Compact)).mid(1).chopped(1);
    return {};
}
QString trackURL(const QString &pattern, QJsonValue value) {
    if (value.isObject()) value = value.toObject().value("url");
    QString url = trackID(value);
    require(!url.isEmpty(), "Invalid v2 track URL; original preserved");
    if (!pattern.isEmpty()) {
        url = QString(pattern).replace("%%", url);
        if (!url.contains("://")) url.prepend("https://");
    }
    const QUrl parsed(url, QUrl::StrictMode);
    require(parsed.isValid() && !parsed.host().isEmpty() && parsed.userInfo().isEmpty()
                && (parsed.scheme() == "https" || parsed.scheme() == "http"),
            "Invalid v2 track URL; original preserved");
    return url;
}
qint64 audioDuration(const QString &path) {
    QProcess process;
    process.setStandardErrorFile(QProcess::nullDevice());
    process.start("ffmpeg", {"-nostdin", "-v", "error", "-xerror", "-protocol_whitelist", "file,pipe",
                             "-i", path, "-map", "0:a:0", "-vn", "-ac", "1", "-ar", "48000",
                             "-f", "s16le", "pipe:1"});
    require(process.waitForStarted(3000), "Cannot start ffmpeg to measure audio duration");
    QElapsedTimer timer;
    timer.start();
    qint64 bytes = 0;
    while (process.state() != QProcess::NotRunning) {
        process.waitForReadyRead(100);
        bytes += process.readAllStandardOutput().size();
        if (timer.elapsed() >= 60000) {
            process.kill(); process.waitForFinished(3000);
            require(false, "Audio duration decoding timed out");
        }
    }
    bytes += process.readAllStandardOutput().size();
    require(process.exitStatus() == QProcess::NormalExit && process.exitCode() == 0 && bytes >= 96,
            "Cannot measure duration: no readable audio");
    return bytes / 96; // 48 kHz mono, two bytes per sample; truncate to milliseconds.
}
QJsonObject updateList(QJsonObject doc, const QString &id, const QString &url, QString &action,
                       qint64 durationMs = -1, bool *needsDuration = nullptr) {
    require(doc.value("sources").isArray(), "Invalid list_v2.json sources; original preserved");
    auto sources = doc.value("sources").toArray();
    QString previous;
    bool found = false;
    const auto accept = [&](const QString &candidate) {
        require(!found || previous == candidate, "Conflicting v2 track mappings; original preserved");
        previous = candidate;
        found = true;
    };
    for (qsizetype i = 0; i < sources.size(); ++i) {
        require(sources[i].isObject(), "Invalid v2 source; original preserved");
        auto source = sources[i].toObject();
        if (!source.contains("tracksStorages")) continue;
        require(source.value("tracksStorages").isArray(), "Invalid v2 storages; original preserved");
        const auto stores = source.value("tracksStorages").toArray();
        auto updated = stores;
        for (qsizetype j = 0; j < stores.size(); ++j) {
            require(stores[j].isObject(), "Invalid v2 storage; original preserved");
            auto store = stores[j].toObject();
            QString pattern;
            if (store.contains("urlTemplate")) {
                require(store.value("urlTemplate").isString(), "Invalid v2 URL template; original preserved");
                pattern = store.value("urlTemplate").toString();
                require(pattern.contains("%%"), "Invalid v2 URL template; original preserved");
            }
            bool matched = false;
            QJsonObject replacement{{"url", url}};
            const auto preserve = [&](QJsonValue entry) {
                matched = true;
                const auto metadata = entry.toObject();
                for (auto it = metadata.begin(); it != metadata.end(); ++it)
                    if (it.key() != "id" && it.key() != "url" && it.key() != "durationMs")
                        replacement.insert(it.key(), it.value());
            };
            if (store.contains("trackIds")) {
                require(store.value("trackIds").isArray() && !pattern.isEmpty(), "Invalid v2 track IDs; original preserved");
                QJsonArray keep;
                for (const auto entry : store.value("trackIds").toArray()) {
                    if (trackID(entry) == id) {
                        accept(trackURL(pattern, id));
                        preserve(entry);
                    } else keep.append(entry);
                }
                store.insert("trackIds", keep);
            }
            if (store.contains("tracks")) {
                require(store.value("tracks").isObject(), "Invalid v2 tracks; original preserved");
                auto tracks = store.value("tracks").toObject();
                if (tracks.contains(id)) {
                    accept(trackURL(pattern, tracks.value(id)));
                    preserve(tracks.take(id));
                }
                store.insert("tracks", tracks);
            }
            updated[j] = store;
            if (matched) updated.append(QJsonObject{{"tracks", QJsonObject{{id, replacement}}}});
        }
        source.insert("tracksStorages", updated);
        sources[i] = source;
    }
    action = !found ? "added" : previous == url ? "skipped" : "updated";
    if (action == "skipped") return doc;
    if (!found) {
        qsizetype targetSource = -1, targetStore = -1;
        for (qsizetype i = 0; i < sources.size(); ++i) {
            if (sources[i].toObject().value("supportedVersions") != QJsonValue(">=2.0.0")) continue;
            require(targetSource < 0, "Ambiguous >=2.0.0 source; original preserved");
            targetSource = i;
        }
        if (targetSource < 0) {
            targetSource = sources.size();
            sources.append(QJsonObject{{"supportedVersions", ">=2.0.0"}});
        }
        QString compatiblePattern;
        for (qsizetype i = 0; i < sources.size(); ++i) {
            bool matchedTemplate = false;
            const auto stores = sources[i].toObject().value("tracksStorages").toArray();
            for (qsizetype j = 0; j < stores.size(); ++j) {
                const auto store = stores[j].toObject();
                const auto pattern = store.value("urlTemplate").toString();
                if (pattern.isEmpty() || trackURL(pattern, id) != url) continue;
                require(compatiblePattern.isEmpty() || compatiblePattern == pattern,
                        "Ambiguous compatible v2 templates; original preserved");
                require(!matchedTemplate, "Ambiguous compatible v2 storages; original preserved");
                matchedTemplate = true;
                compatiblePattern = pattern;
                if (i != targetSource) continue;
                require(targetStore < 0, "Ambiguous compatible v2 storages; original preserved");
                targetStore = j;
            }
        }
        {
            auto source = sources[targetSource].toObject();
            auto stores = source.value("tracksStorages").toArray();
            if (targetStore < 0) {
                targetStore = stores.size();
                stores.append(compatiblePattern.isEmpty()
                    ? QJsonObject{{"tracks", QJsonObject{}}}
                    : QJsonObject{{"urlTemplate", compatiblePattern}, {"trackIds", QJsonArray{}}});
            }
            auto store = stores[targetStore].toObject();
            if (store.contains("trackIds")) {
                // Parse the decimal directly: never round IDs through a double.
                const auto number = QJsonDocument::fromJson(("[" + id + "]").toUtf8()).array();
                require(number.size() == 1 && trackID(number[0]) == id,
                        "Track ID cannot be represented exactly in v2 trackIds; original preserved");
                auto ids = store.value("trackIds").toArray();
                if (needsDuration) *needsDuration = true;
                ids.append(QJsonObject{{"id", number[0]}, {"durationMs", durationMs}});
                store.insert("trackIds", ids);
            } else {
                auto tracks = store.value("tracks").toObject();
                tracks.insert(id, store.contains("urlTemplate") ? id : url);
                store.insert("tracks", tracks);
            }
            stores[targetStore] = store;
            source.insert("tracksStorages", stores);
            sources[targetSource] = source;
        }
    }
    doc.insert("sources", sources);
    return doc;
}

// Optional metadata belongs to the same source as the intended audio storage.
QJsonObject editMetadata(QJsonObject doc, const QString &id, const QString &url,
                         const QJsonObject &patch) {
    if (patch.isEmpty()) return doc;
    auto sources = doc.value("sources").toArray();
    int target = -1;
    for (int i = 0; i < sources.size(); ++i) {
        const auto source = sources[i].toObject();
        for (const auto value : source.value("tracksStorages").toArray()) {
            const auto store = value.toObject();
            const auto pattern = store.value("urlTemplate").toString();
            bool matches = false;
            for (const auto entry : store.value("trackIds").toArray())
                if (trackID(entry) == id) matches = trackURL(pattern, id) == url;
            if (store.value("tracks").toObject().contains(id))
                matches |= trackURL(pattern, store.value("tracks").toObject().value(id)) == url;
            if (matches) {
                require(target < 0 || target == i, "Ambiguous metadata source; original preserved");
                target = i;
            }
        }
    }
    require(target >= 0, "No compatible metadata source; original preserved");
    const auto version = sources[target].toObject().value("supportedVersions");
    require(version.isUndefined() || version == QJsonValue(">0.0.0") || version == QJsonValue(">=2.0.0"),
            "Metadata source version range cannot be safely verified");
    for (int i = 0; i < sources.size(); ++i) {
        const auto source = sources[i].toObject();
        require(i == target || !source.value("tracks").toObject().contains(id),
                "Metadata exists in another source; ambiguous or shadowed edit refused");

    }
    auto source = sources[target].toObject();
    require(!source.contains("tracks") || source.value("tracks").isObject(), "Invalid source metadata");
    auto tracks = source.value("tracks").toObject();
    require(!tracks.contains(id) || tracks.value(id).isObject(), "Invalid track metadata");
    auto track = tracks.value(id).toObject();
    auto changes = patch;
    if (changes.contains("artists")) {
        auto artists = changes.value("artists").toArray();
        const auto existing = track.value("artists").toArray();
        for (int i = 0; i < artists.size(); ++i) {
            auto artist = artists[i].toObject();
            for (const auto old : existing) if (trackID(old) == trackID(artist)) {
                auto preserved = old.toObject();
                preserved.insert("name", artist.value("name")); artist = preserved; break;
            }
            artists[i] = artist;
        }
        changes.insert("artists", artists);
        require(!track.contains("__fckCensor") || track.value("__fckCensor").isObject(), "Invalid insertion metadata");
        auto options = track.value("__fckCensor").toObject();
        if (!options.contains("insertionIndex")) options.insert("insertionIndex", QJsonValue::Null);
        track.insert("__fckCensor", options);
    }
    for (auto it = changes.begin(); it != changes.end(); ++it) track.insert(it.key(), it.value());
    tracks.insert(id, track); source.insert("tracks", tracks); sources[target] = source;
    doc.insert("sources", sources); return doc;
}
QJsonObject validateMetadata(const QVariantMap &metadata) {
    auto patch = QJsonObject::fromVariantMap(metadata);
    for (auto it = patch.begin(); it != patch.end(); ++it) {
        const auto key = it.key(); const auto value = it.value();
        require(key == "title" || key == "artists" || key == "coverUri" || key == "durationMs", "Unsupported metadata field");
        if (key == "durationMs") {
            const double n = value.toDouble(-1);
            require(value.isDouble() && n >= 0 && n <= 9007199254740991.0 && std::floor(n) == n, "Duration must be a non-negative safe integer in milliseconds");
        } else if (key == "artists") {
            require(value.isArray() && !value.toArray().isEmpty() && value.toArray().size() <= 100, "Provide 1-100 artists with real IDs and names");
            QSet<QString> ids;
            for (const auto entry : value.toArray()) {
                const auto artist = entry.toObject(); const auto id = trackID(artist);
                require(artist.size() == 2 && QRegularExpression("\\A[A-Za-z0-9_-]{1,128}\\z").match(id).hasMatch() && !ids.contains(id), "Artist IDs must be valid and unique");
                require(artist.value("name").isString() && !artist.value("name").toString().trimmed().isEmpty() && artist.value("name").toString().size() <= 4096 && !artist.value("name").toString().contains(QChar(0)) && !artist.value("name").toString().contains('\n') && !artist.value("name").toString().contains('\r'), "Artist names are required (maximum 4096 characters)");
                ids.insert(id);
            }
        } else {
            require(value.isString() && !value.toString().trimmed().isEmpty() && value.toString().size() <= 4096 && !value.toString().contains(QChar(0)) && !value.toString().contains('\n') && !value.toString().contains('\r'), "Metadata text must be nonempty single-line text (maximum 4096 characters)");
            QString text = value.toString().trimmed();
            if (key == "coverUri") {
                if (text.startsWith("https://", Qt::CaseInsensitive)) text.remove(0, 8);
                const auto address = text.contains("://") ? text : "https://" + text;
                const QUrl cover(QString(address).replace("%%", "100x100"), QUrl::StrictMode);
                require(cover.isValid() && !cover.host().isEmpty() && cover.userInfo().isEmpty() && (cover.scheme() == "https" || cover.scheme() == "http"), "Cover URI must be an HTTP(S) address; HTTPS is stored scheme-less");
            }
            it.value() = text;
        }
    }
    return patch;
}

// Preserve existing JSON entries and separators verbatim, including unknown
// metadata. Serialize new values using the surrounding indentation and line endings.
QByteArray jsonBytes(const QJsonValue &value) {
    return QJsonDocument(QJsonArray{value}).toJson(QJsonDocument::Compact).mid(1).chopped(1);
}
qsizetype jsonEnd(const QString &text, qsizetype start) {
    bool quoted = false, escaped = false;
    int depth = 0;
    for (qsizetype i = start; i < text.size(); ++i) {
        const QChar c = text[i];
        if (quoted) {
            if (escaped) escaped = false;
            else if (c == '\\') escaped = true;
            else if (c == '"') { quoted = false; if (!depth) return i + 1; }
        } else if (c == '"') quoted = true;
        else if (c == '{' || c == '[') ++depth;
        else if (c == '}' || c == ']') { if (!depth) return i; if (--depth == 0) return i + 1; }
        else if (!depth && (c == ',' || c.isSpace())) return i;
    }
    return text.size();
}
QString patchJSON(const QString &text, const QJsonValue &before, const QJsonValue &after,
                  QString base = {}, QString unit = {}, QString newline = {}) {
    if (before == after) return text;
    if (newline.isEmpty()) {
        newline = text.contains("\r\n") ? "\r\n" : text.contains('\r') ? "\r" : "\n";
        const auto line = text.indexOf(newline);
        if (line >= 0) {
            auto p = line + newline.size();
            while (p < text.size() && (text[p] == ' ' || text[p] == '\t')) unit += text[p++];
        }
        if (unit.isEmpty()) unit = "    ";
    }
    const bool multiline = text.contains(newline);
    const auto serialize = [&](const QJsonValue &value, const QString &indent) {
        if (!multiline || (!value.isObject() && !value.isArray())) return QString::fromUtf8(jsonBytes(value));
        QString pretty = QString::fromUtf8(value.isObject()
            ? QJsonDocument(value.toObject()).toJson(QJsonDocument::Indented)
            : QJsonDocument(value.toArray()).toJson(QJsonDocument::Indented));
        pretty.chop(1);
        auto lines = pretty.split('\n');
        for (int i = 1; i < lines.size(); ++i) {
            int spaces = 0;
            while (spaces < lines[i].size() && lines[i][spaces] == ' ') ++spaces;
            lines[i] = indent + QString(unit).repeated(spaces / 4) + lines[i].mid(spaces);
        }
        return lines.join(newline);
    };
    const bool object = before.isObject() && after.isObject();
    const bool array = before.isArray() && after.isArray();
    if (!object && !array) return serialize(after, base);
    QStringList entries, seen;
    QString prefix, suffix, separator;
    qsizetype pos = 1, previousEnd = 1;
    int index = 0;
    QString entryIndent = base + unit;
    while (pos < text.size()) {
        while (pos < text.size() && (text[pos].isSpace() || text[pos] == ',')) ++pos;
        if (pos == text.size() || text[pos] == '}' || text[pos] == ']') break;
        const auto start = pos;
        if (!index) prefix = text.mid(1, start - 1);
        else if (index == 1) separator = text.mid(previousEnd, start - previousEnd);
        const auto line = text.lastIndexOf(newline, start - 1);
        if (line >= 0) {
            const auto indent = text.mid(line + newline.size(), start - line - newline.size());
            if (indent.trimmed().isEmpty()) entryIndent = indent;
        }
        QString key;
        if (object) {
            const auto end = jsonEnd(text, pos);
            key = QJsonDocument::fromJson(("[" + text.mid(pos, end - pos) + "]").toUtf8()).array()[0].toString();
            pos = end;
            while (pos < text.size() && (text[pos].isSpace() || text[pos] == ':')) ++pos;
        }
        const auto end = jsonEnd(text, pos);
        if (object ? after.toObject().contains(key) : index < after.toArray().size()) {
            const auto oldValue = object ? before.toObject().value(key) : before.toArray()[index];
            const auto newValue = object ? after.toObject().value(key) : after.toArray()[index];
            entries.append((entries.isEmpty() ? QString() : text.mid(previousEnd, start - previousEnd))
                + text.mid(start, pos - start) + patchJSON(text.mid(pos, end - pos), oldValue, newValue, entryIndent, unit, newline));
        }
        seen.append(key);
        pos = previousEnd = end;
        ++index;
    }
    suffix = text.mid(previousEnd, pos - previousEnd);
    if (!index) {
        suffix = text.mid(1, pos - 1);
        prefix = multiline ? newline + entryIndent : QString();
    }
    if (separator.isEmpty()) separator = "," + prefix;
    if (object) {
        const auto values = after.toObject();
        for (auto it = values.begin(); it != values.end(); ++it) {
            if (!seen.contains(it.key()))
                entries.append((entries.isEmpty() ? QString() : separator)
                               + QString::fromUtf8(jsonBytes(it.key())) + (multiline ? ": " : ":")
                               + serialize(it.value(), entryIndent));
        }
    } else {
        for (int i = index; i < after.toArray().size(); ++i)
            entries.append((entries.isEmpty() ? QString() : separator) + serialize(after.toArray()[i], entryIndent));
    }
    return text.left(1) + prefix + entries.join(QString()) + suffix + text.mid(pos);
}
Result perform(const QString &root, const QString &id, const QString &title,
               const QString &artist, const QString &url, const QJsonObject &metadata) {
    Result result;
    bool saved = false, overwritten = false;
    try {
        require(QRegularExpression("\\A[0-9]{1,32}\\z").match(id).hasMatch(), "Track ID must contain 1-32 ASCII digits");
        require(QDir(root).exists(), "Data directory does not exist");
        require(QFileInfo(root).canonicalFilePath() == root, "Data directory must be canonical (no symlinks)");
        QLockFile lock(root + "/.ddetector-upload.lock");
        require(lock.tryLock(0), "Another uploader is using this repository");
        require(QString::fromUtf8(git(root, {"rev-parse", "--show-toplevel"})).trimmed() == root,
                "Data directory must be the Git repository root");
        const auto originalHead = git(root, {"rev-parse", "--verify", "HEAD"});
        QStringList operationPaths;
        for (const auto &name : {QString("MERGE_HEAD"), QString("CHERRY_PICK_HEAD"), QString("REVERT_HEAD"),
                                 QString("rebase-merge"), QString("rebase-apply")}) {
            auto path = QString::fromUtf8(git(root, {"rev-parse", "--git-path", name})).trimmed();
            if (QDir::isRelativePath(path)) path = root + "/" + path;
            operationPaths.append(path);
            require(!QFileInfo::exists(path), "Finish the current Git operation first");
        }
        const QStringList paths{"list_v2.json", "README.md", "tracks/" + id};
        for (const auto &path : paths) require(!QFileInfo(root + "/" + path).isSymLink(), "Upload target is a symlink");
        require(!QFileInfo(root + "/tracks").isSymLink(), "Tracks directory is a symlink");
        require(git(root, QStringList{"status", "--porcelain", "--untracked-files=all", "--"} + paths).isEmpty(),
                "Upload targets have existing changes; commit or move them first");
        const auto originalIndex = git(root, QStringList{"ls-files", "--stage", "-z", "--"} + paths);
        QHash<QString, Snapshot> expected;
        for (const auto &path : paths) expected.insert(path, snapshot(root + "/" + path));
        const auto verifyFiles = [&] {
            require(QFileInfo(root).canonicalFilePath() == root && !QFileInfo(root + "/tracks").isSymLink(),
                    "Upload directory changed during send");
            for (const auto &path : paths)
                require(snapshot(root + "/" + path) == expected.value(path),
                        "Upload target changed during send; external edits preserved: " + path);
        };
        const auto verifyRepository = [&] {
            for (const auto &path : operationPaths)
                require(!QFileInfo::exists(path), "A Git operation started during send; send refused");
            require(git(root, {"rev-parse", "--verify", "HEAD"}) == originalHead,
                    "Git HEAD changed during send; send refused");
        };
        const auto verifyGit = [&] {
            verifyRepository();
            require(git(root, QStringList{"ls-files", "--stage", "-z", "--"} + paths) == originalIndex,
                    "Upload index changed during send; send refused");
        };
        const auto saveTarget = [&](const QString &path, const QByteArray &bytes) {
            verifyFiles();
            write(root + "/" + path, bytes);
            expected[path] = {true, QCryptographicHash::hash(bytes, QCryptographicHash::Sha256)};
        };
        const QString github = "https://raw.githubusercontent.com/Hazzz895/FckCensorData/refs/heads/main/tracks/" + id;
        const QByteArray list = read(root + "/list_v2.json");
        QString text = decode(list, "list_v2.json");
        const bool listBom = text.startsWith(QChar(0xfeff));
        if (listBom) text.remove(0, 1);
        if (list.isEmpty()) {
            require(!QFileInfo::exists(root + "/list_v2.json"), "Empty list_v2.json; original preserved");
            text = "{\n    \"sources\": []\n}\n";
        }
        QJsonParseError parse;
        const auto doc = QJsonDocument::fromJson(text.toUtf8(), &parse);
        require(parse.error == QJsonParseError::NoError && doc.isObject(), "Invalid list_v2.json; original preserved");
        // Reject shadowing before audio migration can remove storage durations.
        if (metadata.contains("durationMs")) {
            for (const auto source : doc.object().value("sources").toArray()) {
                for (const auto value : source.toObject().value("tracksStorages").toArray()) {
                    const auto store = value.toObject();
                    const auto check = [&](const QJsonObject &track) {
                        require(!track.contains("durationMs") || track.value("durationMs") == metadata.value("durationMs"),
                                "Storage duration may shadow metadata; edit refused");
                    };
                    check(store.value("tracks").toObject().value(id).toObject());
                    for (const auto entry : store.value("trackIds").toArray())
                        if (trackID(entry) == id) check(entry.toObject());
                }
            }
        }
        // Preflight routing and metadata before downloading. The placeholder duration
        // stays in memory; rebuild with decoded audio duration before serializing.
        bool needsDuration = false;
        auto updated = updateList(doc.object(), id, github, result.action, -1, &needsDuration);
        auto audioUpdated = updated;
        updated = editMetadata(updated, id, github, metadata);
        if (updated != audioUpdated && result.action == "skipped") result.action = "updated";
        QByteArray audio;
        if (!url.isEmpty()) {
            QUrl source(url, QUrl::StrictMode);
            require(source.isValid() && (source.scheme() == "http" || source.scheme() == "https") &&
                    !source.host().isEmpty() && source.userInfo().isEmpty(), "MP3 URL must be HTTP(S), without credentials");
            audio = download(source);
        }
        if (needsDuration) {
            QTemporaryFile temporary;
            QString path = root + "/tracks/" + id;
            if (!audio.isEmpty()) {
                require(temporary.open() && temporary.write(audio) == audio.size() && temporary.flush(),
                        "Cannot prepare audio for duration decoding");
                path = temporary.fileName();
            }
            const auto duration = audioDuration(path);
            require(!metadata.contains("durationMs") || metadata.value("durationMs").toInteger() == duration,
                    "Requested metadata durationMs conflicts with measured audio duration");
            audioUpdated = updateList(doc.object(), id, github, result.action, duration);
            updated = editMetadata(audioUpdated, id, github, metadata);
            if (updated != audioUpdated && result.action == "skipped") result.action = "updated";
        }
        // Exclude surrounding whitespace from the recursive JSON patch.
        qsizetype start = 0;
        while (start < text.size() && text[start].isSpace()) ++start;
        const auto end = jsonEnd(text, start);
        text.replace(start, end - start, patchJSON(text.mid(start, end - start), doc.object(), updated));
        require(QJsonDocument::fromJson(text.toUtf8()).object() == updated, "List edit validation failed");
        QString readme;
        if (result.action != "skipped") {
            readme = decode(read(root + "/README.md"), "README.md");
            const QString eol = newline(readme);
            if (!readme.isEmpty() && !readme.endsWith('\n') && !readme.endsWith('\r') &&
                readme != QString(QChar(0xfeff))) readme += eol;
            readme += "- [" + title + " - " + artist + "](https://music.yandex.ru/track/" + id + ")" + eol;
        }
        // Downloading is outside the write phase; revalidate every target and Git
        // snapshot before any mutation. The lock is also respected by workspace saves.
        verifyFiles();
        verifyGit();
        require(git(root, QStringList{"status", "--porcelain", "--untracked-files=all", "--"} + paths).isEmpty(),
                "Upload targets changed during send; external edits preserved");
        if (!audio.isEmpty()) {
            require(QDir().mkpath(root + "/tracks"), "Cannot create tracks directory");
            overwritten = expected.value("tracks/" + id).exists;
            saveTarget("tracks/" + id, audio);
            saved = true;
        }
        if (result.action != "skipped") {
            saveTarget("list_v2.json", (listBom ? QByteArray::fromHex("efbbbf") : QByteArray()) + text.toUtf8());
            saveTarget("README.md", readme.toUtf8());
            QStringList changed{"list_v2.json", "README.md"};
            if (saved) changed << "tracks/" + id;
            verifyFiles();
            verifyGit();
            git(root, QStringList{"add", "--sparse", "--"} + changed);
            verifyFiles();
            verifyRepository();
            git(root, QStringList{"commit", "--only", "-m", "add track «" + title + " - " + artist + "»", "--"} + changed);
        }
        result.status = (saved ? (overwritten ? "File overwritten · " : "File saved · ") : "") + QString("List: ") + result.action +
                        (result.action == "skipped" ? " (no commit)" : " · committed");
    } catch (const std::exception &error) {
        result.error = QString::fromUtf8(error.what());
        result.status = "Failed; inspect local files/index before retrying";
        if (saved) result.status += " (audio was saved)";
    }
    return result;
}
}
Uploader::Uploader(QObject *parent) : QObject(parent) {}
Uploader::Uploader(const QString &directory, QObject *parent) : Uploader(parent) {
    setDirectory(directory);
}
Uploader::~Uploader() {
    for (auto thread : findChildren<QThread *>()) thread->wait();
    if (m_worker) { m_worker->wait(); delete m_worker; }
}
bool Uploader::setDirectory(QString directory) {
    if (m_busy) return false;
    directory = directory.isEmpty() ? QString() : QDir::cleanPath(QFileInfo(directory).absoluteFilePath());
    if (m_directoryValidator) {
        const auto rejection = m_directoryValidator(directory);
        if (!rejection.isEmpty()) { m_error = rejection; emit stateChanged(); return false; }
    }
    m_directory = directory;
    return true;
}
void Uploader::send(QString id, QString title, QString artist, QString url, QVariantMap metadata) {
    if (m_busy) return;
    if (m_sendValidator) {
        const auto rejection = m_sendValidator(id);
        if (!rejection.isEmpty()) {
            m_error = rejection; m_action.clear(); m_status = "Send refused"; emit stateChanged(); return;
        }
    }
    if (m_directory.isEmpty()) {
        m_error = "Configure a data directory before sending";
        m_action.clear(); m_status = "Data directory is not configured";
        emit stateChanged(); return;
    }
    title = title.trimmed();
    if (title.isEmpty()) title = id;
    QStringList artists;
    for (const auto &part : artist.split(',')) if (!part.trimmed().isEmpty()) artists << part.trimmed();
    artist = artists.isEmpty() ? "Unknown" : artists.join(", ");
    if (title.contains('\n') || title.contains('\r') || artist.contains('\n') || artist.contains('\r') ||
        title.contains(QChar(0)) || artist.contains(QChar(0))) {
        m_error = "Title and artist must be single-line text without NUL";
        m_action.clear(); m_status = "Invalid upload fields";
        emit stateChanged(); return;
    }
    QJsonObject patch;
    try { patch = validateMetadata(metadata); }
    catch (const std::exception &error) { m_error = QString::fromUtf8(error.what()); m_action.clear(); m_status = "Invalid metadata"; emit stateChanged(); return; }
    m_busy = true; m_error.clear(); m_action.clear(); m_status = "Sending…";
    emit stateChanged();
    auto result = std::make_shared<Result>();
    const auto root = m_directory;
    m_worker = QThread::create([root, id, title, artist, url = url.trimmed(), patch, result] {
        *result = perform(root, id, title, artist, url, patch);
    });
    connect(m_worker, &QThread::finished, this, [this, result] {
        m_worker->deleteLater(); m_worker = nullptr;
        m_busy = false; m_error = result->error; m_action = result->action; m_status = result->status;
        emit stateChanged();
        if (m_error.isEmpty()) emit completed(m_action);
    });
    m_worker->start();
}

int Uploader::requestDuration(QString id, QString url) {
    const int request = ++m_durationRequest;
    const auto root = m_directory;
    auto duration = std::make_shared<qint64>(-1);
    auto worker = QThread::create([root, id, url = url.trimmed(), duration] {
        try {
            require(QRegularExpression("\\A[0-9]{1,32}\\z").match(id).hasMatch(), "Invalid track ID");
            QTemporaryFile temporary;
            QString path = root + "/tracks/" + id;
            if (!url.isEmpty()) {
                const QUrl source(url, QUrl::StrictMode);
                require(source.isValid() && !source.host().isEmpty() && source.userInfo().isEmpty()
                            && (source.scheme() == "http" || source.scheme() == "https"), "Invalid audio URL");
                const auto bytes = download(source);
                require(temporary.open() && temporary.write(bytes) == bytes.size() && temporary.flush(),
                        "Cannot prepare audio for duration decoding");
                path = temporary.fileName();
            } else {
                require(!root.isEmpty(), "No data directory");
                require(snapshot(path).exists, "No local audio");
            }
            *duration = audioDuration(path);
        } catch (const std::exception &) {} // A failed preview must not change Send state.
    });
    worker->setParent(this);
    connect(worker, &QThread::finished, this, [this, worker, request, duration] {
        worker->deleteLater();
        emit durationReady(request, *duration);
    });
    worker->start();
    return request;
}

QVariantMap Uploader::trackMetadata(QString id) const {
    try {
        const auto doc = QJsonDocument::fromJson(read(m_directory + "/list_v2.json"));
        for (const auto value : doc.object().value("sources").toArray()) {
            const auto tracks = value.toObject().value("tracks").toObject();
            if (tracks.contains(id)) return tracks.value(id).toObject().toVariantMap();
        }
    } catch (const std::exception &) {}
    return {};
}

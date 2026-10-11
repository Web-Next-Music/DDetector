#include "uploader.h"
#include <QDir>
#include <QCryptographicHash>
#include <QHash>
#include <QFile>
#include <QFileInfo>
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
#include <QUrl>
#include <memory>
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
Result perform(const QString &root, const QString &id, const QString &title,
               const QString &artist, const QString &url) {
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
        const QStringList paths{"list.json", "README.md", "tracks/" + id};
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
        QByteArray list = read(root + "/list.json");
        QString text = decode(list, "list.json");
                const bool listBom = text.startsWith(QChar(0xfeff));
                if (listBom) text.remove(0, 1);
        if (!list.isEmpty()) {
            QJsonParseError parse;
            const auto doc = QJsonDocument::fromJson(text.toUtf8(), &parse);
            require(parse.error == QJsonParseError::NoError && doc.isObject() && doc.object().value("tracks").isObject(),
                    "Invalid list.json; original preserved");
            const auto tracks = doc.object().value("tracks").toObject();
            if (tracks.contains(id)) {
                require(tracks.value(id).isString(), "Existing track URL is not a string");
                if (tracks.value(id).toString() == github) result.action = "skipped";
                else {
                    QRegularExpression re("((?:\\A|[\\r\\n])        \"" + id + "\": \")([^\"]*)(\")");
                    auto match = re.match(text);
                    require(match.hasMatch(), "Unsupported list formatting; original preserved");
                    text.replace(match.capturedStart(2), match.capturedLength(2), github);
                    result.action = "updated";
                }
            } else {
                QRegularExpression closingLine("(?:\\r\\n|\\n|\\r)    }");
                auto matches = closingLine.globalMatch(text);
                qsizetype closing = -1;
                while (matches.hasNext()) closing = matches.next().capturedStart();
                require(closing >= 0, "Unsupported list formatting; original preserved");
                qsizetype end = closing;
                while (end > 0 && text[end - 1].isSpace()) --end;
                const QString eol = newline(text);
                text.insert(closing, eol + "        \"" + id + "\": \"" + github + "\"");
                if (end > 0 && text[end - 1] != '{') text.insert(end, ',');
                result.action = "added";
            }
        } else {
            require(!QFileInfo::exists(root + "/list.json"), "Empty list.json; original preserved");
            text = "{\n    \"tracks\": {\n        \"" + id + "\": \"" + github + "\"\n    }\n}\n";
            result.action = "added";
        }
        // Validate the surgical edit before writing any upload files.
        require(QJsonDocument::fromJson(text.toUtf8()).object().value("tracks").toObject().value(id).toString() == github,
                "List edit validation failed");
        QString readme;
        if (result.action != "skipped") {
            readme = decode(read(root + "/README.md"), "README.md");
            const QString eol = newline(readme);
            if (!readme.isEmpty() && !readme.endsWith('\n') && !readme.endsWith('\r') &&
                readme != QString(QChar(0xfeff))) readme += eol;
            readme += "- [" + title + " - " + artist + "](https://music.yandex.ru/track/" + id + ")" + eol;
        }
        QByteArray audio;
        if (!url.isEmpty()) {
            QUrl source(url, QUrl::StrictMode);
            require(source.isValid() && (source.scheme() == "http" || source.scheme() == "https") &&
                    !source.host().isEmpty() && source.userInfo().isEmpty(), "MP3 URL must be HTTP(S), without credentials");
            audio = download(source);
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
            saveTarget("list.json", (listBom ? QByteArray::fromHex("efbbbf") : QByteArray()) + text.toUtf8());
            saveTarget("README.md", readme.toUtf8());
            QStringList changed{"list.json", "README.md"};
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
Uploader::~Uploader() { if (m_worker) { m_worker->wait(); delete m_worker; } }
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
void Uploader::send(QString id, QString title, QString artist, QString url) {
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
    m_busy = true; m_error.clear(); m_action.clear(); m_status = "Sending…";
    emit stateChanged();
    auto result = std::make_shared<Result>();
    const auto root = m_directory;
    m_worker = QThread::create([root, id, title, artist, url = url.trimmed(), result] {
        *result = perform(root, id, title, artist, url);
    });
    connect(m_worker, &QThread::finished, this, [this, result] {
        m_worker->deleteLater(); m_worker = nullptr;
        m_busy = false; m_error = result->error; m_action = result->action; m_status = result->status;
        emit stateChanged();
        if (m_error.isEmpty()) emit completed(m_action);
    });
    m_worker->start();
}

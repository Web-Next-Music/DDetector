#include "coverimageprovider.h"
#include <QCryptographicHash>
#include <QDir>
#include <QEventLoop>
#include <QFile>
#include <QImageReader>
#include <QBuffer>
#include <QMutex>
#include <QMutexLocker>
#include <QNetworkAccessManager>
#include <QNetworkReply>
#include <QQuickTextureFactory>
#include <QSaveFile>
#include <QStandardPaths>
#include <QTimer>
#include <atomic>
#include <memory>
#include <algorithm>

namespace {
constexpr qint64 maxBytes = 4 * 1024 * 1024;
constexpr qint64 maxDiskBytes = 128 * 1024 * 1024;
constexpr qsizetype maxDiskEntries = 1024;
QMutex cacheMutex;
struct Result {
    QImage image;
    QString error;
    std::atomic_bool canceled{false};
};
class Response final : public QQuickImageResponse {
public:
    std::shared_ptr<Result> result = std::make_shared<Result>();
    QQuickTextureFactory *textureFactory() const override { return QQuickTextureFactory::textureFactoryForImage(result->image); }
    QString errorString() const override { return result->error; }
    void cancel() override { result->canceled = true; }
    void complete() { emit finished(); }
};

QString cacheDirectory() {
#ifdef Q_OS_WIN
    const QString base = qEnvironmentVariable("APPDATA");
#else
    const QString base = QStandardPaths::writableLocation(QStandardPaths::GenericConfigLocation);
#endif
    return base.isEmpty() ? QString() : base + "/DDetector/cache/track-covers";
}
QByteArray cached(const QString &path) {
    if (path.isEmpty()) return {};
    QMutexLocker lock(&cacheMutex);
    QFile file(path);
    if (!file.open(QIODevice::ReadOnly) || file.size() > maxBytes) return {};
    return file.read(maxBytes + 1);
}
}

void CoverImageProvider::store(const QString &directory, const QString &path, const QByteArray &bytes) {
    if (directory.isEmpty()) return;
    QMutexLocker lock(&cacheMutex);
    if (!QDir().mkpath(directory)) return;
    QSaveFile file(path);
    if (!file.open(QIODevice::WriteOnly) || file.write(bytes) != bytes.size() || !file.commit()) return;
    QDir dir(directory);
    const auto entries = dir.entryInfoList({"*.cover"}, QDir::Files, QDir::Time | QDir::Reversed);
    qint64 total = 0;
    for (const auto &entry : entries) total += entry.size();
    qsizetype count = entries.size();
    for (const auto &entry : entries) {
        if (total <= maxDiskBytes && count <= maxDiskEntries) break;
        if (QFile::remove(entry.absoluteFilePath())) {
            total -= entry.size();
            --count;
        }
    }
}

bool CoverImageProvider::isSafeUrl(const QUrl &url) {
    // Covers are public CDN objects: never accept API endpoints, credentials or arbitrary hosts.
    return url.isValid() && url.scheme() == "https" && url.userInfo().isEmpty()
        && (url.port(-1) == -1 || url.port() == 443) && !url.hasQuery() && !url.hasFragment()
        && (((url.host() == "avatars.mds.yandex.net" || url.host() == "avatars.mds.yandex.ru"
              || url.host() == "avatars.yandex.net" || url.host() == "avatars.yandex.ru")
             && (url.path().startsWith("/get-music/") || url.path().startsWith("/get-music-content/")))
            || (url.host() == "i.scdn.co" && url.path().startsWith("/image/")
                && url.path().size() > QStringLiteral("/image/").size()));
}
struct CoverImageProvider::State {
    QMutex mutex;
    QList<std::weak_ptr<Result>> jobs;
    std::atomic_bool stopping{false};
    int pending = 0;
};
QImage CoverImageProvider::decode(const QByteArray &bytes, const QSize &requested) {
    if (bytes.isEmpty() || bytes.size() > maxBytes) return {};
    QBuffer buffer;
    buffer.setData(bytes);
    if (!buffer.open(QIODevice::ReadOnly)) return {};
    QImageReader reader(&buffer);
    const QByteArray format = reader.format().toLower();
    if (format != "png" && format != "jpeg" && format != "jpg" && format != "webp" && format != "gif") return {};
    const QSize size = reader.size();
    // Bound both source decoding (plugins may ignore scaledSize) and the output.
    constexpr qint64 maximumSourcePixels = 4 * 1024 * 1024;
    if (!size.isValid() || size.width() > 4096 || size.height() > 4096
        || qint64(size.width()) * size.height() > maximumSourcePixels) return {};
    const QSize target = !requested.isEmpty() ? requested.boundedTo(QSize(1024, 1024)) : QSize(512, 512);
    QSize scaled = size.scaled(target, Qt::KeepAspectRatioByExpanding);
    if (scaled.width() > 1024 || scaled.height() > 1024)
        scaled = size.scaled(QSize(1024, 1024), Qt::KeepAspectRatio);
    scaled = QSize(qBound(1, scaled.width(), size.width()), qBound(1, scaled.height(), size.height()));
    reader.setScaledSize(scaled);
    QImage image = reader.read();
    if (image.isNull() || image.sizeInBytes() > maximumSourcePixels * 4) return {};
    if (image.width() > scaled.width() || image.height() > scaled.height())
        image = image.scaled(scaled, Qt::IgnoreAspectRatio, Qt::SmoothTransformation);
    return image.sizeInBytes() <= 4 * 1024 * 1024 ? image : QImage();
}
QByteArray CoverImageProvider::download(const QUrl &url, const std::function<bool()> &canceled) {
    if (canceled()) return {};
    // The public provider validates URLs before reaching this transport. Keep
    // credentials and cookies separate from the bridge and playback managers.
    QNetworkAccessManager manager;
    QNetworkRequest request(url);
    request.setAttribute(QNetworkRequest::RedirectPolicyAttribute, QNetworkRequest::ManualRedirectPolicy);
    request.setAttribute(QNetworkRequest::CookieLoadControlAttribute, QNetworkRequest::Manual);
    request.setAttribute(QNetworkRequest::CookieSaveControlAttribute, QNetworkRequest::Manual);
    request.setTransferTimeout(10000);
    auto *reply = manager.get(request);
    reply->setReadBufferSize(maxBytes + 1);
    QEventLoop loop;
    QTimer deadline, cancellation;
    deadline.setSingleShot(true);
    QObject::connect(&deadline, &QTimer::timeout, reply, &QNetworkReply::abort);
    QObject::connect(&cancellation, &QTimer::timeout, reply, [&] { if (canceled()) reply->abort(); });
    QByteArray bytes;
    const auto drain = [&] {
        if (reply->isOpen() && reply->bytesAvailable() > 0) bytes += reply->read(maxBytes + 1 - bytes.size());
        const auto length = reply->header(QNetworkRequest::ContentLengthHeader);
        if (bytes.size() > maxBytes || (length.isValid() && length.toLongLong() > maxBytes)) reply->abort();
    };
    QObject::connect(reply, &QIODevice::readyRead, reply, drain);
    QObject::connect(reply, &QNetworkReply::metaDataChanged, reply, drain);
    QObject::connect(reply, &QNetworkReply::finished, &loop, &QEventLoop::quit);
    deadline.start(10000); cancellation.start(25);
    if (canceled()) reply->abort();
    if (!reply->isFinished()) loop.exec();
    drain();
    return !canceled() && reply->error() == QNetworkReply::NoError
        && reply->attribute(QNetworkRequest::HttpStatusCodeAttribute).toInt() == 200
        && bytes.size() <= maxBytes ? bytes : QByteArray();
}
CoverImageProvider::CoverImageProvider() : state(std::make_shared<State>()) { pool.setMaxThreadCount(4); }
CoverImageProvider::~CoverImageProvider() {
    {
        QMutexLocker lock(&state->mutex);
        state->stopping = true;
        for (const auto &job : state->jobs)
            if (const auto result = job.lock()) result->canceled = true;
    }
    // Queued jobs still run their completion path, but skip all I/O and decoding.
    pool.waitForDone();
}
QQuickImageResponse *CoverImageProvider::requestImageResponse(const QString &id, const QSize &requestedSize) {
    auto *response = new Response;
    const auto result = response->result;
    const QUrl url(QUrl::fromPercentEncoding(id.toUtf8()), QUrl::StrictMode);
    auto finish = [response] { QMetaObject::invokeMethod(response, [response] { response->complete(); }, Qt::QueuedConnection); };
    const auto sharedState = state;
    {
        QMutexLocker lock(&sharedState->mutex);
        if (!isSafeUrl(url) || sharedState->stopping || sharedState->pending >= 132) {
            result->error = !isSafeUrl(url) ? "Unsupported cover URL" : "Cover queue full or shutting down";
            finish();
            return response;
        }
        ++sharedState->pending;
        for (auto it = sharedState->jobs.begin(); it != sharedState->jobs.end();) {
            if (it->expired()) it = sharedState->jobs.erase(it); else ++it;
        }
        sharedState->jobs.append(result);
    }
    pool.start(QRunnable::create([sharedState, result, url, requestedSize, finish] {
        const auto canceled = [&] { return result->canceled.load() || sharedState->stopping.load(); };
        const auto complete = [&] {
            if (canceled()) { result->image = {}; result->error = "Cover canceled"; }
            else if (result->image.isNull()) result->error = "Cover unavailable";
            {
                QMutexLocker lock(&sharedState->mutex);
                --sharedState->pending;
                sharedState->jobs.erase(std::remove_if(sharedState->jobs.begin(), sharedState->jobs.end(),
                    [&](const auto &job) { return job.expired() || job.lock() == result; }), sharedState->jobs.end());
            }
            finish();
        };
        if (canceled()) { complete(); return; }
        const QString directory = cacheDirectory();
        const QString path = directory.isEmpty() ? QString() : directory + "/"
            + QString::fromLatin1(QCryptographicHash::hash(url.toEncoded(), QCryptographicHash::Sha256).toHex()) + ".cover";
        QByteArray bytes = cached(path);
        if (!canceled()) result->image = CoverImageProvider::decode(bytes, requestedSize);
        if (result->image.isNull() && !canceled()) {
            bytes = CoverImageProvider::download(url, canceled);
            if (!canceled() && !bytes.isEmpty()) {
                result->image = CoverImageProvider::decode(bytes, requestedSize);
                if (!canceled() && !result->image.isNull()) store(directory, path, bytes);
            }
        }
        complete();
    }));
    return response;
}

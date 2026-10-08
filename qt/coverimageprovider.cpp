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

namespace {
constexpr qint64 maxBytes = 4 * 1024 * 1024;
constexpr qint64 maxDiskBytes = 128 * 1024 * 1024;
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
QImage decode(const QByteArray &bytes, const QSize &requested) {
    QBuffer buffer;
    buffer.setData(bytes);
    buffer.open(QIODevice::ReadOnly);
    QImageReader reader(&buffer);
    const QSize size = reader.size();
    if (!size.isValid() || size.width() > 4096 || size.height() > 4096) return {};
    QSize target = requested.isValid() ? requested.boundedTo(QSize(1024, 1024)) : QSize(512, 512);
    reader.setScaledSize(size.scaled(target, Qt::KeepAspectRatioByExpanding));
    return reader.read();
}
QString cacheDirectory() {
    const QString base = QStandardPaths::writableLocation(QStandardPaths::CacheLocation);
    return base.isEmpty() ? QString() : base + "/track-covers";
}
QByteArray cached(const QString &path) {
    if (path.isEmpty()) return {};
    QMutexLocker lock(&cacheMutex);
    QFile file(path);
    if (!file.open(QIODevice::ReadOnly) || file.size() > maxBytes) return {};
    return file.readAll();
}
void store(const QString &directory, const QString &path, const QByteArray &bytes) {
    if (directory.isEmpty()) return;
    QMutexLocker lock(&cacheMutex);
    if (!QDir().mkpath(directory)) return;
    QSaveFile file(path);
    if (!file.open(QIODevice::WriteOnly) || file.write(bytes) != bytes.size() || !file.commit()) return;
    QDir dir(directory);
    const auto entries = dir.entryInfoList({"*.cover"}, QDir::Files, QDir::Time | QDir::Reversed);
    qint64 total = 0;
    for (const auto &entry : entries) total += entry.size();
    for (const auto &entry : entries) {
        if (total <= maxDiskBytes) break;
        if (QFile::remove(entry.absoluteFilePath())) total -= entry.size();
    }
}
}

bool CoverImageProvider::isSafeUrl(const QUrl &url) {
    // Covers are public CDN objects: never accept API endpoints, credentials or arbitrary hosts.
    return url.isValid() && url.scheme() == "https" && url.userInfo().isEmpty()
        && (url.port(-1) == -1 || url.port() == 443) && !url.hasQuery() && !url.hasFragment()
        && (url.host() == "avatars.mds.yandex.net" || url.host() == "avatars.mds.yandex.ru"
            || url.host() == "avatars.yandex.net" || url.host() == "avatars.yandex.ru")
        && (url.path().startsWith("/get-music/") || url.path().startsWith("/get-music-content/"));
}
CoverImageProvider::CoverImageProvider() { pool.setMaxThreadCount(4); }
CoverImageProvider::~CoverImageProvider() { pool.waitForDone(); }
QQuickImageResponse *CoverImageProvider::requestImageResponse(const QString &id, const QSize &requestedSize) {
    auto *response = new Response;
    const auto result = response->result;
    const QUrl url(QUrl::fromPercentEncoding(id.toUtf8()), QUrl::StrictMode);
    auto finish = [response] { QMetaObject::invokeMethod(response, [response] { response->complete(); }, Qt::QueuedConnection); };
    if (!isSafeUrl(url) || pending.load() >= 132) {
        result->error = !isSafeUrl(url) ? "Unsupported cover URL" : "Cover queue full";
        finish();
        return response;
    }
    ++pending;
    pool.start(QRunnable::create([this, result, url, requestedSize, finish] {
        const QString directory = cacheDirectory();
        const QString path = directory.isEmpty() ? QString() : directory + "/"
            + QString::fromLatin1(QCryptographicHash::hash(url.toEncoded(), QCryptographicHash::Sha256).toHex()) + ".cover";
        QByteArray bytes = cached(path);
        result->image = decode(bytes, requestedSize);
        if (result->image.isNull() && !result->canceled) {
            // Dedicated manager has no OAuth headers, cookie jar state or bridge credentials.
            QNetworkAccessManager manager;
            QNetworkRequest request(url);
            request.setAttribute(QNetworkRequest::RedirectPolicyAttribute, QNetworkRequest::ManualRedirectPolicy);
            request.setAttribute(QNetworkRequest::CookieLoadControlAttribute, QNetworkRequest::Manual);
            request.setAttribute(QNetworkRequest::CookieSaveControlAttribute, QNetworkRequest::Manual);
            request.setTransferTimeout(10000);
            auto *reply = manager.get(request);
            QEventLoop loop;
            QTimer deadline;
            deadline.setSingleShot(true);
            QObject::connect(&deadline, &QTimer::timeout, reply, &QNetworkReply::abort);
            QTimer cancellation;
            QObject::connect(&cancellation, &QTimer::timeout, reply, [reply, result] { if (result->canceled) reply->abort(); });
            bytes.clear();
            QObject::connect(reply, &QIODevice::readyRead, reply, [&] {
                bytes += reply->read(maxBytes + 1 - bytes.size());
                if (bytes.size() > maxBytes) reply->abort();
            });
            QObject::connect(reply, &QNetworkReply::finished, &loop, &QEventLoop::quit);
            deadline.start(10000);
            cancellation.start(100);
            loop.exec();
            bytes += reply->read(maxBytes + 1 - bytes.size());
            if (!result->canceled && reply->error() == QNetworkReply::NoError
                && reply->attribute(QNetworkRequest::HttpStatusCodeAttribute).toInt() == 200 && bytes.size() <= maxBytes) {
                result->image = decode(bytes, requestedSize);
                if (!result->image.isNull()) store(directory, path, bytes);
            }
        }
        if (result->image.isNull()) result->error = "Cover unavailable";
        --pending;
        finish();
    }));
    return response;
}

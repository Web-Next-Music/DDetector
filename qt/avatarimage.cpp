#include "avatarimage.h"

#include <QBuffer>
#include <QImageReader>
#include <QNetworkAccessManager>
#include <QNetworkReply>
#include <QNetworkRequest>
#include <QPainter>
#include <QPainterPath>
#include <QTimer>
#include <memory>

namespace {
constexpr qint64 maxBytes = 2 * 1024 * 1024;
constexpr int deadlineMs = 10000;
constexpr int maxRedirects = 3;
}

AvatarImage::AvatarImage(QQuickItem *parent)
    : QQuickPaintedItem(parent), m_manager(new QNetworkAccessManager(this)),
      m_deadline(new QTimer(this)) {
    setAntialiasing(true);
    setOpaquePainting(false);
    m_deadline->setSingleShot(true);
    connect(m_deadline, &QTimer::timeout, this, [this] {
        ++m_generation;
        cancelRequest();
    });
}

AvatarImage::~AvatarImage() {
    ++m_generation;
    cancelRequest();
}

bool AvatarImage::isSafeUrl(const QUrl &url) {
    return url.isValid() && !url.isRelative() && url.scheme() == "https"
        && !url.host().isEmpty() && !url.authority().contains('@')
        && (url.port(-1) == -1 || url.port() == 443)
        && !url.hasFragment();
}

void AvatarImage::cancelRequest() {
    m_deadline->stop();
    if (m_reply) {
        auto *reply = m_reply.data();
        m_reply.clear();
        reply->disconnect(this);
        reply->abort();
        reply->deleteLater();
    }
}

void AvatarImage::setSource(const QUrl &source) {
    if (source == m_source) return;
    const quint64 generation = ++m_generation;
    cancelRequest();
    m_source = source;
    applyImage(generation, {});
    emit sourceChanged();
    // A signal handler may have selected a different source.
    if (generation != m_generation || !isSafeUrl(source)) return;
    m_deadline->start(deadlineMs);
    request(source, generation, 0);
}

void AvatarImage::request(const QUrl &url, quint64 generation, int redirects) {
    QNetworkRequest request(url);
    request.setAttribute(QNetworkRequest::RedirectPolicyAttribute, QNetworkRequest::ManualRedirectPolicy);
    request.setAttribute(QNetworkRequest::CookieLoadControlAttribute, QNetworkRequest::Manual);
    request.setAttribute(QNetworkRequest::CookieSaveControlAttribute, QNetworkRequest::Manual);
    request.setAttribute(QNetworkRequest::AuthenticationReuseAttribute, QNetworkRequest::Manual);
    request.setTransferTimeout(deadlineMs);
    auto *reply = m_manager->get(request);
    m_reply = reply;
    reply->setReadBufferSize(maxBytes + 1);
    auto bytes = std::make_shared<QByteArray>();
    auto drain = [reply, bytes] {
        const QVariant length = reply->header(QNetworkRequest::ContentLengthHeader);
        if (length.isValid() && length.toLongLong() > maxBytes) {
            reply->abort();
            return;
        }
        bytes->append(reply->read(maxBytes + 1 - bytes->size()));
        if (bytes->size() > maxBytes) reply->abort();
    };
    connect(reply, &QNetworkReply::metaDataChanged, this, drain);
    connect(reply, &QIODevice::readyRead, this, drain);
    connect(reply, &QNetworkReply::finished, this, [this, reply, bytes, drain, generation, redirects] {
        reply->deleteLater();
        if (generation != m_generation || m_reply != reply) return;
        drain();
        m_reply.clear();
        const int status = reply->attribute(QNetworkRequest::HttpStatusCodeAttribute).toInt();
        const QUrl target = reply->attribute(QNetworkRequest::RedirectionTargetAttribute).toUrl();
        if (reply->error() == QNetworkReply::NoError && bytes->size() <= maxBytes) {
            if (status >= 300 && status < 400 && !target.isEmpty()) {
                const QUrl next = reply->url().resolved(target);
                // Never forward even an opaque path token to a different origin.
                if (redirects < maxRedirects && isSafeUrl(next) && next.host() == m_source.host()) {
                    this->request(next, generation, redirects + 1);
                    return;
                }
            } else if (status == 200) {
                m_deadline->stop();
                applyImage(generation, decode(*bytes));
                return;
            }
        }
        m_deadline->stop();
    });
}

QImage AvatarImage::decode(const QByteArray &bytes) {
    if (bytes.isEmpty() || bytes.size() > maxBytes) return {};
    QBuffer buffer;
    buffer.setData(bytes);
    if (!buffer.open(QIODevice::ReadOnly)) return {};
    QImageReader reader(&buffer);
    // Provider avatars can be PNG, JPEG, WebP or the first frame of a GIF.
    const QByteArray format = reader.format().toLower();
    if (format != "png" && format != "jpeg" && format != "jpg" && format != "webp" && format != "gif") return {};
    const QSize size = reader.size();
    if (!size.isValid() || size.width() > 2048 || size.height() > 2048) return {};
    if (size.width() > 136 || size.height() > 136)
        reader.setScaledSize(size.scaled(QSize(136, 136), Qt::KeepAspectRatio));
    reader.setAutoTransform(true);
    QImage image = reader.read();
    if (image.isNull() || image.sizeInBytes() > maxBytes) return {};
    return image;
}

void AvatarImage::applyImage(quint64 generation, const QImage &image) {
    if (generation != m_generation) return;
    const bool wasReady = ready();
    m_image = image;
    update();
    if (wasReady != ready()) emit statusChanged();
}

void AvatarImage::setImageForTest(const QImage &image) {
    ++m_generation;
    cancelRequest();
    applyImage(m_generation, image);
}

void AvatarImage::paint(QPainter *painter) {
    if (!ready() || width() <= 0 || height() <= 0) return;
    const QRectF bounds(0, 0, width(), height());
    const qreal scale = qMax(width() / m_image.width(), height() / m_image.height());
    const QSizeF crop(width() / scale, height() / scale);
    const QRectF source((m_image.width() - crop.width()) / 2,
                        (m_image.height() - crop.height()) / 2, crop.width(), crop.height());
    painter->save();
    painter->setRenderHint(QPainter::Antialiasing);
    painter->setRenderHint(QPainter::SmoothPixmapTransform);
    QPainterPath clip;
    clip.addEllipse(bounds);
    painter->setClipPath(clip, Qt::IntersectClip);
    painter->drawImage(bounds, m_image, source);
    painter->restore();
}

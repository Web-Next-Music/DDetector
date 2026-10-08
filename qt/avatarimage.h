#pragma once

#include <QImage>
#include <QPointer>
#include <QQuickPaintedItem>
#include <QUrl>

class QNetworkAccessManager;
class QNetworkReply;
class QTimer;
class AvatarImageTests;

class AvatarImage : public QQuickPaintedItem {
    Q_OBJECT
    Q_PROPERTY(QUrl source READ source WRITE setSource NOTIFY sourceChanged)
    Q_PROPERTY(bool ready READ ready NOTIFY statusChanged)
public:
    explicit AvatarImage(QQuickItem *parent = nullptr);
    ~AvatarImage() override;
    QUrl source() const { return m_source; }
    void setSource(const QUrl &source);
    bool ready() const { return !m_image.isNull(); }
    void paint(QPainter *painter) override;

signals:
    void sourceChanged();
    void statusChanged();

private:
    friend class AvatarImageTests;
    static bool isSafeUrl(const QUrl &url);
    static QImage decode(const QByteArray &bytes);
    void setImageForTest(const QImage &image);
    void applyImage(quint64 generation, const QImage &image);
    void cancelRequest();
    void request(const QUrl &url, quint64 generation, int redirects);

    QUrl m_source;
    QImage m_image;
    quint64 m_generation = 0;
    QNetworkAccessManager *m_manager;
    QTimer *m_deadline;
    QPointer<QNetworkReply> m_reply;
};

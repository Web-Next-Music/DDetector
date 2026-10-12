#include "avatarimage.h"

#include <QBuffer>
#include <QPainter>
#include <QSignalSpy>
#include <QTimer>
#include <QtTest>

class AvatarImageTests : public QObject {
    Q_OBJECT
    static QImage render(AvatarImage &item) {
        QImage output(64, 64, QImage::Format_ARGB32_Premultiplied);
        output.fill(Qt::transparent);
        QPainter painter(&output);
        item.setWidth(64);
        item.setHeight(64);
        item.paint(&painter);
        return output;
    }
    static QByteArray png(const QImage &image) {
        QByteArray bytes;
        QBuffer buffer(&bytes);
        buffer.open(QIODevice::WriteOnly);
        if (!image.save(&buffer, "PNG")) return {};
        return bytes;
    }
private slots:
    void circularCoverCrop() {
        AvatarImage item;
        QImage image(120, 40, QImage::Format_ARGB32);
        image.fill(Qt::green);
        QPainter painter(&image);
        painter.fillRect(0, 0, 40, 40, Qt::red);
        painter.fillRect(80, 0, 40, 40, Qt::blue);
        painter.end();
        item.setImageForTest(image);
        const QImage output = render(item);
        QCOMPARE(output.pixelColor(32, 32), QColor(Qt::green));
        QCOMPARE(output.pixelColor(8, 32), QColor(Qt::green));
        QCOMPARE(output.pixelColor(56, 32), QColor(Qt::green));
        for (const QPoint corner : {QPoint(0, 0), QPoint(63, 0), QPoint(0, 63), QPoint(63, 63)})
            QCOMPARE(output.pixelColor(corner).alpha(), 0);
        bool partialAlpha = false;
        for (int y = 0; y < 64; ++y)
            for (int x = 0; x < 64; ++x) {
                const int alpha = output.pixelColor(x, y).alpha();
                partialAlpha |= alpha > 0 && alpha < 255;
            }
        QVERIFY(partialAlpha);
        image = image.transformed(QTransform().rotate(90));
        item.setImageForTest(image);
        const QImage portrait = render(item);
        QCOMPARE(portrait.pixelColor(32, 8), QColor(Qt::green));
        QCOMPARE(portrait.pixelColor(32, 56), QColor(Qt::green));
    }
    void statusAndGeneration() {
        AvatarImage item;
        QSignalSpy status(&item, &AvatarImage::statusChanged);
        QSignalSpy source(&item, &AvatarImage::sourceChanged);
        QVERIFY(!item.ready());
        QImage image(16, 16, QImage::Format_ARGB32);
        image.fill(Qt::cyan);
        item.setImageForTest(image);
        QVERIFY(item.ready());
        QCOMPARE(status.count(), 1);
        item.setImageForTest(image);
        QCOMPARE(status.count(), 1);
        const quint64 oldGeneration = item.m_generation;
        item.setSource(QUrl("file:///not-an-avatar.png"));
        QCOMPARE(source.count(), 1);
        QCOMPARE(status.count(), 2);
        QVERIFY(!item.ready());
        item.applyImage(oldGeneration, image);
        QVERIFY(!item.ready());
        QVERIFY(!item.m_reply);
        QCOMPARE(render(item).pixelColor(32, 32).alpha(), 0);
        item.setSource(item.source());
        QCOMPARE(source.count(), 1);
        item.setSource({});
        QCOMPARE(source.count(), 2);
    }
    void rejectedSources() {
        AvatarImage item;
        for (const QString &value : {
                 QString("http://example.com/avatar.png"), QString("file:///tmp/avatar.png"),
                 QString("qrc:/avatar.png"), QString("data:image/png;base64,AAAA"),
                 QString("ftp://example.com/avatar.png"), QString("//example.com/avatar.png"),
                 QString("https://user:password@example.com/avatar.png"),
                 QString("https://@example.com/avatar.png"), QString("https://example.com:444/avatar.png"),

                 QString("https://example.com/avatar.png#secret")}) {
            const QUrl url(value);
            QVERIFY2(!AvatarImage::isSafeUrl(url), qPrintable(value));
            item.setSource(url);
            QCOMPARE(item.source(), url);
            QVERIFY(!item.ready());
            QVERIFY(!item.m_reply);
            QVERIFY(!item.m_deadline->isActive());
        }
        QVERIFY(AvatarImage::isSafeUrl(QUrl("https://example.com/avatar.png")));
        QVERIFY(AvatarImage::isSafeUrl(QUrl("https://example.com:443/avatar.png")));
        QVERIFY(AvatarImage::isSafeUrl(QUrl("https://avatars.githubusercontent.com/u/1?v=4")));
        QVERIFY(AvatarImage::isSafeUrl(QUrl("https://cdn.discordapp.com/avatars/1/avatar.webp?size=128")));
    }
    void boundedDecode() {
        QImage image(40, 20, QImage::Format_ARGB32);
        image.fill(Qt::magenta);
        const QByteArray bytes = png(image);
        QVERIFY(!bytes.isEmpty());
        QCOMPARE(AvatarImage::decode(bytes).size(), image.size());
        QVERIFY(AvatarImage::decode({}).isNull());
        QVERIFY(AvatarImage::decode("not an image").isNull());
        QVERIFY(AvatarImage::decode(QByteArray(2 * 1024 * 1024 + 1, 'x')).isNull());
        QImage regular(1024, 1024, QImage::Format_ARGB32);
        regular.fill(Qt::cyan);
        QCOMPARE(AvatarImage::decode(png(regular)).size(), QSize(136, 136));
        QImage oversized(2049, 2049, QImage::Format_ARGB32);
        oversized.fill(Qt::cyan);
        const QByteArray compressed = png(oversized);
        QVERIFY(compressed.size() < 2 * 1024 * 1024);
        QVERIFY(AvatarImage::decode(compressed).isNull());
    }
};

QTEST_MAIN(AvatarImageTests)
#include "avatarimage_tests.moc"

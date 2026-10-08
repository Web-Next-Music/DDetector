#include "coverimageprovider.h"
#include <QSignalSpy>
#include <QCryptographicHash>
#include <QDir>
#include <QFile>
#include <QStandardPaths>
#include <QQuickTextureFactory>
#include <QtTest>

class CoverImageProviderTests : public QObject {
    Q_OBJECT
private slots:
    void cachedYandexContentCover() {
        QStandardPaths::setTestModeEnabled(true);
        const QUrl url("https://avatars.yandex.net/get-music-content/123/cover/200x200");
        const auto directory = QStandardPaths::writableLocation(QStandardPaths::CacheLocation) + "/track-covers";
        QVERIFY(QDir().mkpath(directory));
        const auto path = directory + "/" + QString::fromLatin1(QCryptographicHash::hash(url.toEncoded(), QCryptographicHash::Sha256).toHex()) + ".cover";
        QImage image(40, 40, QImage::Format_ARGB32);
        image.fill(Qt::cyan);
        QVERIFY(image.save(path, "PNG"));
        CoverImageProvider provider;
        auto *response = provider.requestImageResponse(QString::fromLatin1(QUrl::toPercentEncoding(url.toString())), QSize(32, 32));
        QSignalSpy finished(response, &QQuickImageResponse::finished);
        QVERIFY(finished.wait(3000));
        QVERIFY2(response->errorString().isEmpty(), qPrintable(response->errorString()));
        auto *texture = response->textureFactory();
        QVERIFY(texture);
        QCOMPARE(texture->image().pixelColor(0, 0), QColor(Qt::cyan));
        delete texture;
        delete response;
        QVERIFY(QFile::remove(path));
    }
    void safeUrls() {
        QVERIFY(CoverImageProvider::isSafeUrl(QUrl("https://avatars.mds.yandex.net/get-music/123/cover/200x200")));
        QVERIFY(CoverImageProvider::isSafeUrl(QUrl("https://avatars.yandex.net/get-music-content/123/cover/200x200")));
        for (const QString &url : {
                 QString("http://avatars.mds.yandex.net/get-music/1"),
                 QString("https://token@avatars.mds.yandex.net/get-music/1"),
                 QString("https://avatars.mds.yandex.net:444/get-music/1"),
                 QString("https://avatars.mds.yandex.net.evil.test/get-music/1"),
                 QString("https://127.0.0.1/get-music/1"),
                 QString("https://avatars.yandex.net.evil.test/get-music-content/1"),
                 QString("https://avatars.yandex.net/other/1"),
                 QString("https://api.music.yandex.net/get-music/1"),
                 QString("https://avatars.mds.yandex.net/get-music/1?oauth=secret"),
                 QString("https://avatars.mds.yandex.net/get-music/1#secret")})
            QVERIFY2(!CoverImageProvider::isSafeUrl(QUrl(url)), qPrintable(url));
    }
    void rejectsAsynchronously() {
        CoverImageProvider provider;
        auto *response = provider.requestImageResponse("https%3A%2F%2F127.0.0.1%2Fcover", QSize(64, 64));
        QSignalSpy finished(response, &QQuickImageResponse::finished);
        QVERIFY(finished.wait(1000));
        QVERIFY(!response->errorString().isEmpty());
        delete response;
    }
};
QTEST_MAIN(CoverImageProviderTests)
#include "coverimageprovider_tests.moc"

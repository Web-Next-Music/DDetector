#include "coverimageprovider.h"
#include <QSignalSpy>
#include <QCryptographicHash>
#include <QDir>
#include <QFile>
#include <QStandardPaths>
#include <QQuickTextureFactory>
#include <QtTest>
#include <QBuffer>
#include <QSemaphore>
#include <QThread>
#include <QElapsedTimer>
#include <QTcpServer>
#include <QTcpSocket>
#include <QPointer>

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
    void extremeAspectRatioBudgets() {
        for (const QSize source : {QSize(4096, 1), QSize(1, 4096), QSize(2048, 2048), QSize(40, 40)}) {
            QImage image(source, QImage::Format_ARGB32); image.fill(Qt::cyan);
            QByteArray bytes; QBuffer buffer(&bytes); QVERIFY(buffer.open(QIODevice::WriteOnly));
            QVERIFY(image.save(&buffer, "PNG"));
            for (const QSize requested : {QSize(64, 64), QSize(512, 512), QSize(100000, 100000)}) {
                const auto decoded = CoverImageProvider::decode(bytes, requested);
                QVERIFY(!decoded.isNull());
                QVERIFY(decoded.width() <= 1024 && decoded.height() <= 1024);
                QVERIFY(decoded.sizeInBytes() <= 4 * 1024 * 1024);
                QVERIFY(decoded.width() <= source.width() && decoded.height() <= source.height());
            }
        }
        QImage image(4096, 4096, QImage::Format_ARGB32); image.fill(Qt::cyan);
        QByteArray bytes; QBuffer buffer(&bytes); QVERIFY(buffer.open(QIODevice::WriteOnly));
        QVERIFY(image.save(&buffer, "PNG"));
        QVERIFY(CoverImageProvider::decode(bytes, QSize(64, 64)).isNull());
        QVERIFY(CoverImageProvider::decode(QByteArray(4 * 1024 * 1024 + 1, 'x'), QSize(64, 64)).isNull());
    }
    void cancellationAndShutdownDrainQueueWithoutNetwork() {
        auto *provider = new CoverImageProvider;
        QSemaphore started, release;
        for (int i = 0; i < 4; ++i) provider->pool.start(QRunnable::create([&] { started.release(); release.acquire(); }));
        QVERIFY(started.tryAcquire(4, 3000));
        QList<QQuickImageResponse *> responses;
        int completions = 0;
        const QString url = "https%3A%2F%2Favatars.yandex.net%2Fget-music-content%2Ftest%2Fcover";
        for (int i = 0; i < 133; ++i) {
            auto *response = provider->requestImageResponse(url, QSize(64, 64));
            connect(response, &QQuickImageResponse::finished, this, [&] { ++completions; });
            responses.append(response);
        }
        QVERIFY(responses.last()->errorString().contains("queue full"));
        responses.first()->cancel();
        auto *unblock = QThread::create([&] { QThread::msleep(100); release.release(4); });
        unblock->start();
        QElapsedTimer elapsed; elapsed.start();
        delete provider;
        QVERIFY(elapsed.elapsed() < 2000);
        unblock->wait(); delete unblock;
        QTRY_COMPARE(completions, 133);
        for (int i = 0; i < 132; ++i) QVERIFY(responses[i]->errorString().contains("canceled"));
        qDeleteAll(responses);
    }
    void cancelBeforeDequeuing() {
        CoverImageProvider provider; provider.pool.setMaxThreadCount(1);
        QSemaphore started, release;
        provider.pool.start(QRunnable::create([&] { started.release(); release.acquire(); }));
        QVERIFY(started.tryAcquire(1, 3000));
        auto *response = provider.requestImageResponse("https%3A%2F%2Favatars.yandex.net%2Fget-music-content%2Ftest%2Fcover", QSize(64, 64));
        QSignalSpy finished(response, &QQuickImageResponse::finished);
        response->cancel(); release.release();
        QVERIFY(finished.wait(3000)); QVERIFY(response->errorString().contains("canceled"));
        delete response;
    }
    void activeNetworkCancellationIsBounded() {
        QTcpServer server; QVERIFY(server.listen(QHostAddress::LocalHost));
        bool requested = false;
        QPointer<QTcpSocket> socket;
        connect(&server, &QTcpServer::newConnection, this, [&] {
            socket = server.nextPendingConnection();
            connect(socket, &QTcpSocket::readyRead, socket, [&] { socket->readAll(); requested = true; });
        });
        std::atomic_bool canceled{false}; QByteArray bytes;
        const QUrl url("http://127.0.0.1:" + QString::number(server.serverPort()) + "/stalled-cover");
        auto *worker = QThread::create([&] { bytes = CoverImageProvider::download(url, [&] { return canceled.load(); }); });
        worker->start();
        QTRY_VERIFY_WITH_TIMEOUT(requested, 3000);
        QElapsedTimer elapsed; elapsed.start(); canceled = true;
        QVERIFY(worker->wait(1500)); QVERIFY(elapsed.elapsed() < 1500); QVERIFY(bytes.isEmpty());
        delete worker; if (socket) delete socket;
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

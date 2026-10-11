#include "bridge.h"
#include <QGuiApplication>
#include <QDebug>
#include <memory>
#include <QElapsedTimer>
#include <QJsonDocument>
#include <QQmlComponent>
#include <QQmlEngine>
#include <QTcpServer>
#include <QTcpSocket>
#include <QThread>
#include <QUrlQuery>
#include <functional>

// Standalone regression executable, linked with the native-ui target's support
// objects. It deliberately exercises the HTTP detail protocol, not inline DTOs.
static bool waitFor(const std::function<bool()> &condition) {
    QElapsedTimer timer;
    timer.start();
    while (!condition() && timer.elapsed() < 5000) {
        QCoreApplication::processEvents();
        QThread::msleep(5);
    }
    return condition();
}

int main(int argc, char **argv) {
    QGuiApplication app(argc, argv);
    QTcpServer server;
    if (!server.listen(QHostAddress::LocalHost)) return 1;
    bool sawEncodedId = false;
    QObject::connect(&server, &QTcpServer::newConnection, &server, [&] {
        while (auto socket = server.nextPendingConnection()) {
            auto buffer = std::make_shared<QByteArray>();
            QObject::connect(socket, &QTcpSocket::readyRead, socket, [&, socket, buffer] {
                *buffer += socket->readAll();
                if (!buffer->contains("\r\n\r\n")) return;
                const auto request = *buffer;
                buffer->clear();
                if (!request.contains("Authorization: Bearer detail-test-key")) qFatal("Missing bridge authorization");
                const auto target = request.split(' ').value(1);
                const QUrl url(QString::fromUtf8(target));
                const QUrlQuery query(url);
                QJsonObject body;
                int delay = 0;
                if (url.path() == "/api/censor/job") {
                    const auto id = query.queryItemValue("id", QUrl::FullyDecoded);
                    sawEncodedId = sawEncodedId || target.contains("B%20%2F%3F");
                    body = {{"job", QJsonObject{{"id", id}, {"status", "review_required"},
                        {"request", QJsonObject{{"id", "request"}, {"track_id", 42}}},
                        {"candidates", QJsonArray{QJsonObject{{"id", "correct-candidate"}}}}}}};
                    if (id == "A") delay = 250;
                } else if (url.path().startsWith("/api/censor/")) {
                    const int offset = query.queryItemValue("offset").toInt();
                    body = {{"configured", true}, {"jobs_total", 201}, {"offset", offset}, {"next_offset", offset == 0 ? 100 : 200},
                        {"jobs", QJsonArray{QJsonObject{{"id", "A"}, {"details_omitted", true}, {"request", QJsonObject{{"id", "request-A"}, {"title", "A"}, {"artist", "Artist"}}}},
                            QJsonObject{{"id", "B /?"}, {"details_omitted", true}, {"request", QJsonObject{{"id", "request-B"}, {"title", "B"}, {"artist", "Artist"}}}}}}};
                } else {
                    body = {{"authenticated", true}, {"hasAccess", true}, {"tracks", QJsonArray{}}};
                }
                const auto data = QJsonDocument(body).toJson(QJsonDocument::Compact);
                QTimer::singleShot(delay, socket, [socket, data] {
                    socket->write("HTTP/1.1 200 OK\r\nConnection: close\r\nContent-Type: application/json\r\nContent-Length: "
                        + QByteArray::number(data.size()) + "\r\n\r\n" + data);
                    socket->disconnectFromHost();
                });
            });
            QObject::connect(socket, &QTcpSocket::disconnected, socket, &QObject::deleteLater);
        }
    });
    Bridge bridge(QUrl("http://127.0.0.1:" + QString::number(server.serverPort())), "detail-test-key");
    if (!waitFor([&] { return bridge.censorState().value("configured").toBool(); })) return 2;
    QQmlEngine engine;
    QQmlComponent component(&engine, QUrl("qrc:/CensorDetector.qml"));
    QScopedPointer<QObject> view(component.createWithInitialProperties({{"bridge", QVariant::fromValue(&bridge)}}));
    if (!view) { qWarning() << component.errors(); return 3; }
    view->setProperty("selectedId", "A");
    QCoreApplication::processEvents();
    view->setProperty("selectedId", "B /?");
    if (!waitFor([&] { return view->property("job").toMap().value("id").toString() == "B /?"; })) return 4;
    if (!sawEncodedId || bridge.censorSelectedJob().value("id").toString() != "B /?") return 5;
    view->setProperty("candidateId", "correct-candidate");
    if (view->property("candidate").toMap().value("id") != "correct-candidate") return 6;
    bridge.censorPage(100);
    if (!waitFor([&] { return bridge.censorState().value("offset").toInt() == 100; })) return 7;
    if (view->property("job").toMap().value("id") != "B /?") return 8;
    bridge.censorSelectJob("A");
    QCoreApplication::processEvents();
    bridge.logout();
    QElapsedTimer elapsed;
    elapsed.start();
    while (elapsed.elapsed() < 500) { QCoreApplication::processEvents(); QThread::msleep(5); }
    if (!bridge.censorSelectedJob().isEmpty()) return 9;
    qInfo("Censor detail selection, encoded IDs, paging and logout regressions passed");
    return 0;
}

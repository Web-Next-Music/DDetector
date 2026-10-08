#include "bridge.h"
#include "player.h"
#include <QCommandLineParser>
#include <QDirIterator>
#include <QFontDatabase>
#include <QGuiApplication>
#include <QQmlApplicationEngine>
#include <QQmlContext>
#include <QQuickWindow>
#include <QQuickStyle>
#include <QTimer>

int main(int argc, char **argv) {
    QGuiApplication app(argc, argv);
    app.setApplicationName("DDetector");
    app.setOrganizationName("DDetector");
    QQuickStyle::setStyle("Basic");
    QCommandLineParser parser;
    parser.setApplicationDescription("Native Qt Quick DDetector client; launch through the Go backend.");
    parser.addHelpOption();
    parser.addOptions({{"backend-url", "Loopback backend URL", "url"},
                       {"bridge-key", "Private backend bearer key (C++ only)", "key"},
                       {"demo", "Use bundled UI-only demonstration data; never contacts the backend"},
                       {"smoke-test", "Exit after approximately two seconds"},
                       {"screenshot", "Save a screenshot before smoke-test exit", "file"},
                       {"width", "Initial window width", "pixels", "1200"},
                       {"height", "Initial window height", "pixels", "800"}});
    parser.process(app);
    const bool demo = parser.isSet("demo");
    QUrl base(parser.value("backend-url"));
    if (!demo && (base.scheme() != "http" || base.host() != "127.0.0.1" || base.port() <= 0
                  || !base.userInfo().isEmpty() || base.hasQuery() || base.hasFragment()
                  || (!base.path().isEmpty() && base.path() != "/") || parser.value("bridge-key").isEmpty())) {
        qCritical("Launch via the Go backend with --backend-url http://127.0.0.1:PORT --bridge-key KEY, or explicitly use --demo.");
        return 2;
    }
    QDirIterator fonts(":/assets", {"*.ttf", "*.otf"}, QDir::Files);
    while (fonts.hasNext()) QFontDatabase::addApplicationFont(fonts.next());
    QFont font("DDetector Nunito"); font.setWeight(QFont::Bold); font.setPixelSize(13); app.setFont(font);
    Bridge bridge(base, parser.value("bridge-key").toUtf8(), demo);
    MusicPlayer musicPlayer;
    QQmlApplicationEngine engine;
    engine.rootContext()->setContextProperty("backend", &bridge);
    engine.rootContext()->setContextProperty("musicPlayer", &musicPlayer);
    engine.rootContext()->setContextProperty("demoMode", demo);
    engine.load(QUrl("qrc:/Main.qml"));
    if (engine.rootObjects().isEmpty()) return 1;
    auto window = qobject_cast<QQuickWindow *>(engine.rootObjects().first());
    if (!window) return 1;
    window->resize(qMax(320, parser.value("width").toInt()), qMax(320, parser.value("height").toInt()));
    if (parser.isSet("smoke-test")) {
        const QString screenshot = parser.value("screenshot");
        QTimer::singleShot(2100, &app, [window, screenshot, &app] {
            if (!screenshot.isEmpty() && !window->grabWindow().save(screenshot)) {
                qCritical("Unable to save smoke-test screenshot"); app.exit(3); return;
            }
            app.quit();
        });
    }
    return app.exec();
}

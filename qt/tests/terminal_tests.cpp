#include "terminal.h"
#include "workspace.h"

#include <QApplication>
#include <QFileInfo>
#include <QFile>
#include <QLibraryInfo>
#include <QResource>
#include <QPainter>
#include <QProcess>
#include <QQmlComponent>
#include <QQmlEngine>
#include <QQuickWindow>
#include <QTemporaryDir>
#include <QtTest>
#ifndef Q_OS_WIN
#include <qtermwidget.h>
#include <sys/ioctl.h>
#include <unistd.h>
#include <signal.h>
#endif

class TerminalTest : public QObject {
    Q_OBJECT
    QTemporaryDir resources;
private slots:
    void initTestCase() {
        // This standalone target has no bundled UI assets; exercise the same resource path as the app.
        QVERIFY(resources.isValid());
        const QString scheme = QFileInfo(QString::fromUtf8(__FILE__)).absoluteDir()
                                   .filePath("../assets/themes/DDetector.colorscheme");
        QFile manifest(resources.filePath("terminal.qrc"));
        QVERIFY(manifest.open(QIODevice::WriteOnly));
        manifest.write(("<RCC><qresource prefix=\"/assets\"><file alias=\"DDetector.colorscheme\">"
                        + scheme.toHtmlEscaped() + "</file></qresource></RCC>").toUtf8());
        manifest.close();
        QProcess rcc;
        rcc.start(QLibraryInfo::path(QLibraryInfo::LibraryExecutablesPath) + "/rcc",
                  {"--binary", "-o", resources.filePath("terminal.rcc"), manifest.fileName()});
        QVERIFY(rcc.waitForFinished());
        QCOMPARE(rcc.exitCode(), 0);
        QVERIFY(QResource::registerResource(resources.filePath("terminal.rcc")));
        QVERIFY(QFile::exists(":/assets/DDetector.colorscheme"));
    }
    void cleanupTestCase() {
        QVERIFY(QResource::unregisterResource(resources.filePath("terminal.rcc")));
    }
    void shellSelection() {
        const QByteArray previous = qgetenv("SHELL");
        qputenv("SHELL", "/bin/sh");
        QCOMPARE(TerminalSession::defaultShell(), QString("/bin/sh"));
        qputenv("SHELL", "/does/not/exist");
        QVERIFY(QFileInfo(TerminalSession::defaultShell()).isExecutable());
        if (previous.isNull()) qunsetenv("SHELL"); else qputenv("SHELL", previous);
    }
    void ptyAndEmulator() {
#ifdef Q_OS_WIN
        QSKIP("Unix PTY backend only");
#else
        QTemporaryDir directory;
        QVERIFY(directory.isValid());
        TerminalSession session;
        QString output;
        connect(&session, &TerminalSession::output, this, [&](const QString &text) { output += text; });
        EmbeddedTerminal view;
        view.setSize(QSizeF(720, 320)); view.setSession(&session);
        QVERIFY2(session.start(directory.path()), qPrintable(session.error()));
        QVERIFY(session.running());
        QVERIFY(isatty(session.widget()->getPtySlaveFd()));
        // Disabling echo means assertions cannot pass merely by matching command echo.
        QVERIFY(session.sendText("stty -echo\n"));
        QTest::qWait(100);
        output.clear();
        QVERIFY(session.sendText("test -t 0 && test -t 1 && test -t 2 && printf '\\120\\124\\131\\137\\117\\113\\n'; pwd\n"));
        QTRY_VERIFY(output.contains("PTY_OK"));
        QTRY_VERIFY(output.contains(directory.path()));
        struct winsize before{}, after{};
        QCOMPARE(ioctl(session.widget()->getPtySlaveFd(), TIOCGWINSZ, &before), 0);
        view.setSize(QSizeF(1000, 520));
        QTest::qWait(100);
        QCOMPARE(ioctl(session.widget()->getPtySlaveFd(), TIOCGWINSZ, &after), 0);
        QVERIFY(after.ws_col > before.ws_col);
        QVERIFY(after.ws_row > before.ws_row);
        output.clear();
        QVERIFY(session.sendText("sleep 30\n"));
        QTRY_VERIFY(session.widget()->getForegroundProcessId() != session.widget()->getShellPID());
        const int foreground = session.widget()->getForegroundProcessId();
        QVERIFY(foreground > 0);
        QKeyEvent interrupt(QEvent::KeyPress, Qt::Key_C, Qt::ControlModifier, QString(QChar(3)));
        QCoreApplication::sendEvent(&view, &interrupt);
        QTRY_VERIFY_WITH_TIMEOUT(kill(foreground, 0) == -1 && errno == ESRCH, 3000);
        QVERIFY(session.sendText("printf '\\111\\116\\124\\105\\122\\122\\125\\120\\124\\105\\104\\n'\n"));
        QTRY_VERIFY_WITH_TIMEOUT(output.contains("INTERRUPTED"), 3000);
        // Exercise alternate screen, truecolor, UTF-8 and cursor addressing in the library.
        QVERIFY(session.sendText("printf '\\033[?1049h\\033[2J\\033[H\\033[38;2;255;0;0mRED界\\033[0m'\n"));
        QTest::qWait(100);
        session.widget()->setSelectionStart(0, 0);
        session.widget()->setSelectionEnd(0, 5);
        QVERIFY(session.widget()->selectedText().contains("RED界"));
        session.widget()->setSelectionStart(0, 0);
        session.widget()->setSelectionEnd(0, 0);
        QTest::qWait(100);
        QImage image(view.size().toSize(), QImage::Format_ARGB32_Premultiplied);
        image.fill(Qt::transparent);
        QPainter painter(&image); view.paint(&painter); painter.end();
        bool red = false;
        for (int y = 0; y < image.height() && !red; ++y)
            for (int x = 0; x < image.width(); ++x) {
                const QColor color = image.pixelColor(x, y);
                if (color.red() > 120 && color.green() < 80 && color.blue() < 80) { red = true; break; }
            }
        QVERIFY(red);

        // Sample empty cells, away from text, cursor and the widget's scrollbar.
        for (int y = image.height() / 4; y < image.height() / 2; ++y)
            for (int x = image.width() / 4; x < image.width() / 2; ++x)
                QCOMPARE(image.pixelColor(x, y), QColor(0, 0, 0, 255));
        QVERIFY(session.sendText("printf '\\033[?1049l'; exit\n"));
        QTRY_VERIFY(!session.running());
        QVERIFY(session.start(directory.path()));
        const int pid = session.widget()->getShellPID();
        QVERIFY(pid > 0);
        session.stop();
        QVERIFY(!session.running()); QVERIFY(!session.widget());
        QTRY_VERIFY(kill(pid, 0) == -1 && errno == ESRCH);
        QVERIFY(session.start(directory.path())); // No stale stop timer can kill this shell.
        QTest::qWait(100); QVERIFY(session.running());
#endif
    }
    void dirtyCaptureReusesIdleFrameAndObservesWidgetUpdates() {
#ifndef Q_OS_WIN
        QTemporaryDir directory; TerminalSession session; QQuickWindow window; EmbeddedTerminal view;
        window.resize(720, 320); view.setParentItem(window.contentItem()); window.show();
        view.setSize(QSizeF(720, 320)); view.setSession(&session);
        QVERIFY(session.start(directory.path()));
        session.widget()->setBlinkingCursor(false);
        QTest::qWait(200); view.capture();
        const auto count = view.m_captureCount;
        const auto buffer = view.m_frame.constBits();
        for (int i = 0; i < 30; ++i) view.capture();
        QCOMPARE(view.m_captureCount, count); QCOMPARE(view.m_frame.constBits(), buffer);
        QTest::qWait(250);
        QVERIFY(view.m_captureCount <= count + 1); // Idle timer ticks must not rasterize every frame.
        for (auto *child : session.widget()->findChildren<QWidget *>()) child->update();
        QTRY_VERIFY(view.m_captureCount > count);
        view.setVisible(false);
        QVERIFY(!view.m_repaint.isActive());
        view.setVisible(true); QTRY_VERIFY(view.m_captureCount > count + 1);
        session.stop(); QVERIFY(!view.m_repaint.isActive()); QVERIFY(view.m_frame.isNull());
#endif
    }
    void workspaceCompatibilityAndQml() {
        qmlRegisterType<EmbeddedTerminal>("DDetector.Terminal", 1, 0, "EmbeddedTerminal");
        qmlRegisterUncreatableType<TerminalSession>("DDetector.Terminal", 1, 0, "TerminalSession", "Owned by GitWorkspace");
        QTemporaryDir directory;
        QProcess git; git.start("git", {"init", directory.path()});
        QVERIFY(git.waitForFinished()); QCOMPARE(git.exitCode(), 0);
        GitWorkspace workspace;
        workspace.setRepositoryPath(directory.path());
        QTRY_VERIFY(!workspace.resolving());
        QQmlEngine engine;
        QSignalSpy warnings(&engine, &QQmlEngine::warnings);
        QQmlComponent component(&engine, QUrl("qrc:/terminal.qml"));
        QVERIFY2(component.isReady(), qPrintable(component.errorString()));
        QScopedPointer<QObject> object(component.createWithInitialProperties(
            {{"workspaceBackend", QVariant::fromValue(&workspace)}, {"visible", false}}));
        QVERIFY2(object, qPrintable(component.errorString()));
        auto *item = qobject_cast<QQuickItem *>(object.data()); QVERIFY(item);
        QVERIFY(!workspace.terminalRunning()); // An unattached/hidden panel must not spawn a shell.
        QQuickWindow window; window.resize(800, 400);
        item->setParentItem(window.contentItem()); item->setSize(QSizeF(800, 400));
        window.show(); QTest::qWait(50);
        QVERIFY(!workspace.terminalRunning());
        const QString blocked = "Repository publication is busy";
        workspace.setWriteValidator([blocked] { return blocked; });
        item->setVisible(true);
        QTest::qWait(100);
        QVERIFY(!workspace.terminalRunning());
        QVERIFY(workspace.error().isEmpty()); // Tab activation silently skips a blocked automatic start.
        workspace.startTerminal();
        QVERIFY(!workspace.terminalRunning());
        QCOMPARE(workspace.error(), blocked); // Explicit requests still report the lock.
        workspace.setWriteValidator({});
        item->setVisible(false);
        object->setProperty("workspaceBackend", QVariant::fromValue<QObject *>(nullptr));
        object->setProperty("workspaceBackend", QVariant::fromValue(&workspace));
        item->setVisible(true);
        QTRY_VERIFY(workspace.terminalRunning());
        item->setSize(QSizeF(320, 120));
        QTest::qWait(50);
        const auto views = item->findChildren<EmbeddedTerminal *>();
        QCOMPARE(views.size(), 1);
        QVERIFY(views.first()->width() > 0);
        QVERIFY(views.first()->height() >= 24);
        item->setSize(QSizeF(800, 400));
        QVERIFY(workspace.sendTerminalInput("printf '\\127\\117\\122\\113\\123\\120\\101\\103\\105\\n'\n"));
        QTRY_VERIFY(workspace.terminalOutput().contains("WORKSPACE"));
        workspace.clearTerminal(); QCOMPARE(workspace.terminalOutput(), QString());
        workspace.stopTerminal(); QVERIFY(!workspace.terminalRunning());
        workspace.startTerminal(); QVERIFY(workspace.terminalRunning());
        QVERIFY(workspace.sendTerminalInput("exit\n"));
        QTRY_VERIFY(!workspace.terminalRunning());
        item->setVisible(false); item->setVisible(true);
        QTest::qWait(100); QVERIFY(!workspace.terminalRunning()); // No automatic respawn.
        QCOMPARE(warnings.count(), 0);
        object->setProperty("workspaceBackend", QVariant::fromValue<QObject *>(nullptr));
    }
};

QTEST_MAIN(TerminalTest)
#include "terminal_tests.moc"

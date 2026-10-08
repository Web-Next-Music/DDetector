#include "terminalsession.h"

#include <QApplication>
#include <QDir>
#include <QFileInfo>

#include <QFontDatabase>
#include <QFontInfo>
#include <QProcessEnvironment>
#ifndef Q_OS_WIN
#include <qtermwidget.h>
#include <pwd.h>
#include <unistd.h>
#endif

TerminalSession::TerminalSession(QObject *parent) : QObject(parent) {}
TerminalSession::~TerminalSession() { stop(); }

QString TerminalSession::defaultShell() {
#ifndef Q_OS_WIN
    // SHELL represents the user's chosen shell; passwd is authoritative fallback.
    QString shell = qEnvironmentVariable("SHELL");
    auto usable = [](const QString &path) {
        const QFileInfo file(path);
        return file.isAbsolute() && file.isFile() && file.isExecutable();
    };
    if (usable(shell)) return shell;
    if (const passwd *entry = getpwuid(getuid())) {
        shell = QString::fromLocal8Bit(entry->pw_shell);
        if (usable(shell)) return shell;
    }
    if (usable("/bin/sh")) return QStringLiteral("/bin/sh");
#endif
    return {};
}

void TerminalSession::setError(const QString &error) {
    m_error = error;
    emit changed();
}

bool TerminalSession::start(const QString &directory) {
    if (m_running) return true;
#ifdef Q_OS_WIN
    Q_UNUSED(directory);
    setError("Embedded terminal is Unix-only; a Windows ConPTY backend is not implemented.");
    return false;
#else
    if (!qobject_cast<QApplication *>(QCoreApplication::instance())) {
        setError("Embedded terminal requires QApplication, not QGuiApplication.");
        return false;
    }
    const QString root = QFileInfo(directory).canonicalFilePath();
    if (root.isEmpty() || !QFileInfo(root).isDir()) {
        setError("Terminal working directory must be an existing directory.");
        return false;
    }
    m_shell = defaultShell();
    if (m_shell.isEmpty()) { setError("No executable system shell was found."); return false; }
    // A completed session cannot be restarted. Keep its screen until this point.
    delete m_widget;
    m_widget = new QTermWidget(0);
    m_widget->setAttribute(Qt::WA_DontShowOnScreen);
    m_widget->setAutoClose(true);

    m_widget->setTerminalOpacity(1.0);
    QFont font = QFontDatabase::systemFont(QFontDatabase::FixedFont);
    if (!QFontInfo(font).fixedPitch()) font.setFamily("monospace");
    font.setStyleHint(QFont::Monospace);
    font.setFixedPitch(true);
    m_widget->setTerminalFont(font);
    m_widget->setHistorySize(10000);
    m_widget->setScrollBarPosition(QTermWidget::ScrollBarRight);
    m_widget->setShellProgram(m_shell);
    m_widget->setArgs({"-i"});
    m_widget->setWorkingDirectory(root);
    auto environment = QProcessEnvironment::systemEnvironment();
    environment.insert("TERM", "xterm-256color");
    environment.insert("COLORTERM", "truecolor");
    // Never inherit stale geometry from the parent terminal.
    environment.remove("LINES"); environment.remove("COLUMNS");
    m_widget->setEnvironment(environment.toStringList());
    m_widget->resize(800, 300);
    m_widget->show();
    // Apply after widget style polishing so the desktop palette cannot replace it.
    m_widget->setColorScheme(QStringLiteral(":/assets/DDetector.colorscheme"));
    connect(m_widget, &QTermWidget::receivedData, this, &TerminalSession::output);
    connect(m_widget, &QTermWidget::finished, this, [this] {
        m_running = false;
        emit changed();
        emit finished();
    });
    m_error.clear();
    emit widgetChanged(); // Adapter sets actual geometry before the shell starts.
    m_widget->startShellProgram();
    m_running = m_widget->getShellPID() > 0;
    if (!m_running) setError("Could not start shell on a PTY: " + m_shell);
    else emit changed();
    return m_running;
#endif
}

bool TerminalSession::sendText(const QString &text) {
    if (!m_running) { setError("Shell is not running."); return false; }
    if (text.toUtf8().size() > 64 * 1024) {
        setError("Terminal input is limited to 64 KiB per call."); return false;
    }
#ifndef Q_OS_WIN
    m_widget->sendText(text);
#endif
    return true;
}

void TerminalSession::stop() {
#ifndef Q_OS_WIN
    // QTermWidget owns/reaps the session and closes the PTY, hanging up its shell.
    auto *old = m_widget;
    m_widget = nullptr;
    m_running = false;
    emit widgetChanged();
    delete old;
#endif
    m_running = false;
    emit changed();
}

void TerminalSession::sendEof() {
    // A terminal has no half-close: Ctrl+D is interpreted by the line discipline.
    sendText(QString(QChar(4)));
}

void TerminalSession::clear() {
#ifndef Q_OS_WIN
    if (m_widget) m_widget->clear();
#endif
}

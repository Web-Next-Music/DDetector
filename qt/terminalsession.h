#pragma once

#include <QObject>
#include <QPointer>
#include <QString>

class QTermWidget;

// Owns one QTermWidget emulator and its genuine Unix PTY. Requires QApplication.
// Shell commands are unrestricted trusted user input, not workspace-confined.
class TerminalSession : public QObject {
    Q_OBJECT
    Q_PROPERTY(bool running READ running NOTIFY changed)
    Q_PROPERTY(QString error READ error NOTIFY changed)
    Q_PROPERTY(QString shell READ shell NOTIFY changed)
public:
    explicit TerminalSession(QObject *parent = nullptr);
    ~TerminalSession() override;
    bool running() const { return m_running; }
    QString error() const { return m_error; }
    QString shell() const { return m_shell; }
    QTermWidget *widget() const { return m_widget; }
    static QString defaultShell();
    bool start(const QString &directory);
    bool sendText(const QString &text);
    Q_INVOKABLE void stop();
    Q_INVOKABLE void sendEof();
    Q_INVOKABLE void clear();
signals:
    void changed();
    void widgetChanged();
    void output(const QString &text);
    void finished();
private:
    void setError(const QString &error);
    QTermWidget *m_widget = nullptr;
    bool m_running = false;
    QString m_error, m_shell;
};

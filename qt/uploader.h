#pragma once
#include <QObject>
#include <QString>
#include <QThread>
#include <functional>

// Local filesystem/Git backend; no localhost server or playback credentials.
class Uploader final : public QObject {
    Q_OBJECT
    Q_PROPERTY(bool busy READ busy NOTIFY stateChanged)
    Q_PROPERTY(QString status READ status NOTIFY stateChanged)
    Q_PROPERTY(QString error READ error NOTIFY stateChanged)
    Q_PROPERTY(QString action READ action NOTIFY stateChanged)
public:
    explicit Uploader(QObject *parent = nullptr);
    explicit Uploader(const QString &dataDirectory, QObject *parent = nullptr);
    ~Uploader() override;
    bool busy() const { return m_busy; }
    QString status() const { return m_status; }
    QString error() const { return m_error; }
    QString action() const { return m_action; }
    QString directory() const { return m_directory; }
    void setDirectoryValidator(std::function<QString(const QString &)> validator) { m_directoryValidator = std::move(validator); }
    void setSendValidator(std::function<QString(const QString &)> validator) { m_sendValidator = std::move(validator); }
    Q_INVOKABLE bool setDirectory(QString directory);
    Q_INVOKABLE void send(QString id, QString title, QString artist, QString mp3Url = {});
signals:
    void stateChanged();
    void completed(const QString &action);
private:
    QString m_directory, m_status, m_error, m_action;
    bool m_busy = false;
    QThread *m_worker = nullptr;
    std::function<QString(const QString &)> m_directoryValidator, m_sendValidator;
};

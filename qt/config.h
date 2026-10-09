#pragma once
#include <QObject>
#include <QString>
#include <QTimer>
#include <functional>

class Config final : public QObject {
    Q_OBJECT
    Q_PROPERTY(QString musicToken READ musicToken WRITE setMusicToken NOTIFY settingsChanged)
    Q_PROPERTY(QString repositoryPath READ repositoryPath WRITE setRepositoryPath NOTIFY settingsChanged)
    Q_PROPERTY(bool hideIgnored READ hideIgnored WRITE setHideIgnored NOTIFY settingsChanged)
    Q_PROPERTY(double volume READ volume WRITE setVolume NOTIFY settingsChanged)
        Q_PROPERTY(bool hideWindowControls READ hideWindowControls WRITE setHideWindowControls NOTIFY settingsChanged)
    Q_PROPERTY(QString error READ error NOTIFY errorChanged)
public:
    explicit Config(QObject *parent = nullptr);
    // An empty path is strictly in-memory; overrides never migrate legacy settings.
    explicit Config(const QString &path, QObject *parent = nullptr);
        ~Config() override;
        // Flush debounced settings; failed writes remain pending and can be retried.
        Q_INVOKABLE bool flush();
    Q_INVOKABLE void retryCredentials();
        void setRepositoryValidator(std::function<QString(const QString &)> validator) {
            m_repositoryValidator = std::move(validator);
        }
    QString musicToken() const { return m_token; }
    QString repositoryPath() const { return m_repositoryPath; }
    void setRepositoryPath(const QString &value);
    bool hideIgnored() const { return m_hideIgnored; }
        bool hideWindowControls() const { return m_hideWindowControls; }
        void setHideWindowControls(bool value);
    double volume() const { return m_volume; }
    QString error() const { return m_error; }
    void setMusicToken(const QString &value);
    void setHideIgnored(bool value);
    void setVolume(double value);
signals:
    void settingsChanged();
    void errorChanged();
        void repositoryPathChanged();
private:
    friend class NativeTests;
    void load();
    void save();
        void scheduleSave();
    void fail(const QString &message);
    void startCredentialRead();
    bool acceptCredentialRead(bool success, bool missing, const QString &value);
    bool acceptCredentialWrite(bool success, const QString &value);
    void storeCredential();
    QTimer m_credentialRetry, m_credentialWriteRetry;
    int m_credentialReadAttempts = 0, m_credentialWriteAttempts = 0;
    bool m_useKeychain = false, m_credentialBusy = false, m_credentialReady = false;
    bool m_credentialEdited = false, m_credentialStored = false;
    QString m_storedToken;
    QString m_path, m_token, m_error, m_repositoryPath;
    bool m_hideIgnored = false, m_hideWindowControls = false, m_blocked = false;
    double m_volume = 0.5;
        QTimer m_saveDebounce;
        bool m_savePending = false;
        std::function<QString(const QString &)> m_repositoryValidator;
};

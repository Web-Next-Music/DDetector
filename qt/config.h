#pragma once
#include <QObject>
#include <QString>

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
private:
    void load();
    void save();
    void fail(const QString &message);
    QString m_path, m_token, m_error, m_repositoryPath;
    bool m_hideIgnored = false, m_hideWindowControls = false, m_blocked = false;
    double m_volume = 0.5;
};

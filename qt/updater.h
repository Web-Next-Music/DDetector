#pragma once

#include <QObject>
#include <QUrl>
#include <QByteArray>
#include <memory>
#include <functional>

class QNetworkAccessManager;
class QTemporaryDir;
class QProcessEnvironment;
class QProcess;
class UpdaterTests;

class Updater final : public QObject {
    Q_OBJECT
    Q_PROPERTY(bool available READ available NOTIFY changed)
    Q_PROPERTY(bool busy READ busy NOTIFY changed)
    Q_PROPERTY(QString version READ version NOTIFY changed)
    Q_PROPERTY(QString status READ status NOTIFY changed)
    Q_PROPERTY(QString error READ error NOTIFY changed)
    // Download fraction in [0,1]; -1 means indeterminate.
    Q_PROPERTY(double progress READ progress NOTIFY changed)
public:
    explicit Updater(QObject *parent = nullptr, bool autoCheck = true);
    ~Updater() override;
    bool available() const { return m_available; }
    bool busy() const { return m_busy; }
    QString version() const { return m_version; }
    QString status() const { return m_status; }
    QString error() const { return m_error; }
    double progress() const { return m_progress; }
    Q_INVOKABLE void check();
    Q_INVOKABLE void install();
#ifdef DDETECTOR_DEV_TOOLS
    Q_INVOKABLE void forceAvailable();
#endif
signals:
    void changed();
private:
    friend class UpdaterTests;
    enum class Platform { Browser, Windows, Debian, Arch, AppImage };
    static bool compareVersions(const QString &a, const QString &b, int *result);
    static QString artifactName(Platform platform, const QString &version, const QString &architecture);
    static bool releaseUrl(const QUrl &url, bool asset);
    static QString appImagePathError(const QString &path);
    static bool replaceAppImage(const QString &target, const QString &source, const QByteArray &digest, QString *error);
    static QProcessEnvironment restartEnvironment(QProcessEnvironment environment);
    static void configureLauncher(QProcess &process, const QString &path, bool installer = false);
    void restartLauncher(const QString &path);
    void packageFinished(int code, bool normalExit);
    std::function<bool(QProcess &)> m_startDetached;
    std::function<void()> m_quit;
    void checkRelease(bool force);
    void acceptRelease(const QByteArray &json, bool force = false);
    void detectInstallation();
    void download(Platform platform);
    void launchInstaller(Platform platform, const QString &path);
    void fallback(const QString &reason);
    void fail(const QString &message);
    QNetworkAccessManager *m_network;
    std::unique_ptr<QTemporaryDir> m_staging;
    bool m_available = false, m_busy = false, m_forced = false;
    QString m_version, m_status, m_error, m_appImagePath;
    QByteArray m_downloadDigest;
    double m_progress = -1;
    QByteArray m_release;
    QUrl m_page;
};

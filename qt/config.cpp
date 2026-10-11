#include "config.h"
#include <QDir>
#include <QFile>
#include <QFileInfo>
#include <QJsonDocument>
#include <QJsonObject>
#include <QSaveFile>

#include <QStandardPaths>
#include <cmath>
#include <qt6keychain/keychain.h>

namespace {
constexpr qint64 maximumConfigBytes = 1024 * 1024;
constexpr auto filePermissions = QFileDevice::ReadOwner | QFileDevice::WriteOwner;
constexpr auto directoryPermissions = filePermissions | QFileDevice::ExeOwner;
QString defaultPath() {
    const auto root = QStandardPaths::writableLocation(QStandardPaths::GenericConfigLocation);
    if (root.isEmpty()) return {};
    const auto directory = root + "/DDetector";
    const auto path = directory + "/Config.json";
    const auto legacyPath = directory + "/DDetector/Config.json";
    if (!QFileInfo::exists(path) && QFileInfo::exists(legacyPath)) {
        if (!QFile::rename(legacyPath, path)) return legacyPath;
        QDir().rmdir(directory + "/DDetector");
    }
    // Leave legacy settings intact: no verified migration exists for Desktop.conf.
    return path;
}
}
Config::Config(QObject *parent) : Config(defaultPath(), parent) {
    if (m_path.isEmpty()) { m_blocked = true; fail("Configuration location is unavailable"); }
    m_useKeychain = !m_path.isEmpty() && !m_blocked;
    if (m_useKeychain) {
        QTimer::singleShot(0, this, &Config::startCredentialRead);
    }

}
Config::Config(const QString &path, QObject *parent) : QObject(parent), m_path(path) {
    m_saveDebounce.setSingleShot(true);
    m_saveDebounce.setInterval(200);
    connect(&m_saveDebounce, &QTimer::timeout, this, &Config::save);
    m_credentialRetry.setSingleShot(true);
    connect(&m_credentialRetry, &QTimer::timeout, this, &Config::startCredentialRead);
    m_credentialWriteRetry.setSingleShot(true);
    connect(&m_credentialWriteRetry, &QTimer::timeout, this, &Config::storeCredential);
    load();
}
Config::~Config() { flush(); }
bool Config::repositoryDirectoryExists(const QString &path) const {
    const QFileInfo info(path.trimmed());
    return info.isAbsolute() && info.isDir();
}
void Config::scheduleSave() {
    if (m_path.isEmpty() || m_blocked) return;
    m_savePending = true;
    if (!m_saveDebounce.isActive()) m_saveDebounce.start();
}
bool Config::flush() {
    m_saveDebounce.stop();
    if (m_savePending) save();
    return !m_savePending && !m_blocked;
}
void Config::fail(const QString &message) {
    if (m_error == message) return;
    m_error = message;
    emit errorChanged();
}
void Config::load() {
    if (m_path.isEmpty() || !QFileInfo::exists(m_path)) return;
    QFile file(m_path);
    if (!file.open(QIODevice::ReadOnly)) {
        m_blocked = true; fail("Unable to read configuration"); return;
    }
    QJsonParseError parse;
    const auto bytes = file.read(maximumConfigBytes + 1);
    if (bytes.size() > maximumConfigBytes) {
        m_blocked = true; fail("Configuration exceeds the 1 MiB limit; original preserved"); return;
    }
    const auto document = QJsonDocument::fromJson(bytes, &parse);
    const auto object = document.object();
    const auto token = object.value("musicToken");
    const auto repository = object.value("repositoryPath");
    const auto hidden = object.value("hideIgnored");
        const auto windowControls = object.value("hideWindowControls");
    const auto volume = object.value("volume");
    const auto visualizer = object.value("visualizerEnabled");
    const auto approveSound = object.value("approveSoundEnabled");
    if (file.error() != QFileDevice::NoError || parse.error != QJsonParseError::NoError || !document.isObject()
        || (!token.isUndefined() && !token.isString())
        || (!repository.isUndefined() && !repository.isString())
        || (!hidden.isUndefined() && !hidden.isBool())
        || (!visualizer.isUndefined() && !visualizer.isBool())
        || (!approveSound.isUndefined() && !approveSound.isBool())
        || (!windowControls.isUndefined() && !windowControls.isBool())
        || (!volume.isUndefined() && (!volume.isDouble() || !std::isfinite(volume.toDouble())
                                     || volume.toDouble() < 0 || volume.toDouble() > 1))) {
        m_blocked = true; fail("Invalid configuration; original file has been preserved"); return;
    }
    if (!file.setPermissions(filePermissions)) {
        m_blocked = true; fail("Unable to secure configuration permissions"); return;
    }
    m_token = token.toString();
    m_credentialEdited = !token.isUndefined();
    m_repositoryPath = repository.toString();
    m_hideIgnored = hidden.toBool(false);
        m_hideWindowControls = windowControls.toBool(false);
    m_volume = volume.toDouble(0.5);
    m_visualizerEnabled = visualizer.toBool(false);
    m_approveSoundEnabled = approveSound.toBool(false);
}
void Config::save() {
    if (m_path.isEmpty() || m_blocked) return;
    const auto directory = QFileInfo(m_path).absolutePath();
    if (!QDir().mkpath(directory) || !QFile::setPermissions(directory, directoryPermissions)) {
        fail("Unable to create or secure configuration directory"); return;
    }
    QSaveFile file(m_path);
    file.setDirectWriteFallback(false);
    if (!file.open(QIODevice::WriteOnly) || !file.setPermissions(filePermissions)) {
        fail("Unable to open secure configuration file"); return;
    }
    QJsonObject settings{{"repositoryPath", m_repositoryPath}, {"hideIgnored", m_hideIgnored}, {"hideWindowControls", m_hideWindowControls}, {"volume", m_volume}, {"visualizerEnabled", m_visualizerEnabled}, {"approveSoundEnabled", m_approveSoundEnabled}};
    // Pending/failed writes remain recoverable across shutdown. Never remove the
    // plaintext copy until the OS backend confirms the exact current value.
    if ((m_credentialEdited || !m_token.isEmpty())
        && (!m_useKeychain || !m_credentialStored || m_storedToken != m_token))
        settings.insert("musicToken", m_token);
    const auto bytes = QJsonDocument(settings).toJson();
    if (bytes.size() > maximumConfigBytes) { fail("Configuration exceeds the 1 MiB limit; original preserved"); return; }
    if (file.write(bytes) != bytes.size() || !file.commit()) {
        fail("Unable to save configuration atomically"); return;
    }
    m_savePending = false;
    fail({});
}
void Config::setRepositoryPath(const QString &value) {
    const auto path = value.trimmed();
    if (m_repositoryPath == path) { if (m_savePending) scheduleSave(); return; }
    if (m_repositoryValidator) {
        const QString rejection = m_repositoryValidator(path);
        if (!rejection.isEmpty()) { fail(rejection); return; }
    }
    m_repositoryPath = path; scheduleSave(); emit repositoryPathChanged(); emit settingsChanged();
}
void Config::setMusicToken(const QString &value) {
    const bool changed = m_token != value;
    const bool newIntent = !m_credentialEdited;
    m_credentialEdited = true;
    if (!changed && !newIntent) {
        if (m_savePending) scheduleSave();
        if (m_useKeychain) storeCredential();
        return;
    }
    m_credentialWriteRetry.stop();
    m_credentialWriteAttempts = 0;
    m_token = value;
    scheduleSave();
    if (changed) emit settingsChanged();
    if (m_useKeychain) storeCredential();
}
void Config::startCredentialRead() {
    if (!m_useKeychain || m_credentialBusy || m_blocked) return;
    m_credentialBusy = true;
    auto *job = new QKeychain::ReadPasswordJob("DDetector", this);
    job->setKey("musicToken");
    job->setInsecureFallback(false);
    connect(job, &QKeychain::Job::finished, this, [this, job] {
        if (acceptCredentialRead(job->error() == QKeychain::NoError,
                                 job->error() == QKeychain::EntryNotFound, job->textData()))
            storeCredential();
    });
    job->start();
}
void Config::retryCredentials() {
    if (!m_useKeychain || m_credentialBusy || m_blocked) return;
    m_credentialRetry.stop();
    m_credentialWriteRetry.stop();
    m_credentialReadAttempts = 0;
    m_credentialWriteAttempts = 0;
    startCredentialRead();
}
bool Config::acceptCredentialRead(bool success, bool missing, const QString &value) {
    m_credentialBusy = false;
    m_credentialReady = success || missing;
    if (!m_credentialReady) {
        fail("OS keychain unavailable; recoverable credential copy preserved");
        if (m_credentialReadAttempts < 3)
            m_credentialRetry.start(1000 * (1 << m_credentialReadAttempts++));
        return false;
    }
    m_credentialRetry.stop();
    m_credentialReadAttempts = 0;
    if (success) {
        m_storedToken = value;
        m_credentialStored = true;
        if (!m_credentialEdited && m_token.isEmpty()) {
            m_token = value;
            emit settingsChanged();
        }
    } else {
        m_credentialStored = false;
    }
    return true;
}
void Config::storeCredential() {
    if (!m_useKeychain || !m_credentialReady || m_credentialBusy || m_blocked) return;
    if (m_credentialStored && m_storedToken == m_token) { scheduleSave(); return; }
    // Save the recoverable value before starting an asynchronous OS write.
    scheduleSave();
    if (!flush()) return;
    m_credentialBusy = true;
    const QString value = m_token;
    auto *job = new QKeychain::WritePasswordJob("DDetector", this);
    job->setKey("musicToken");
    job->setInsecureFallback(false);
    job->setTextData(value);
    connect(job, &QKeychain::Job::finished, this, [this, job, value] {
        if (acceptCredentialWrite(job->error() == QKeychain::NoError, value))
            storeCredential();
    });
    job->start();
}
bool Config::acceptCredentialWrite(bool success, const QString &value) {
    m_credentialBusy = false;
    if (!success) {
        fail("OS keychain write failed; recoverable credential copy preserved");
        if (m_credentialWriteAttempts < 3)
            m_credentialWriteRetry.start(1000 * (1 << m_credentialWriteAttempts++));
        return false;
    }
    m_credentialWriteRetry.stop();
    m_credentialWriteAttempts = 0;
    m_storedToken = value;
    m_credentialStored = true;
    scheduleSave();
    return m_token != value;
}
void Config::setHideIgnored(bool value) {
    if (m_hideIgnored == value) { if (m_savePending) scheduleSave(); return; }
    m_hideIgnored = value; scheduleSave(); emit settingsChanged();
}
void Config::setHideWindowControls(bool value) {
    if (m_hideWindowControls == value) { if (m_savePending) scheduleSave(); return; }
    m_hideWindowControls = value; scheduleSave(); emit settingsChanged();
}
void Config::setVisualizerEnabled(bool value) {
    if (m_visualizerEnabled == value) { if (m_savePending) scheduleSave(); return; }
    m_visualizerEnabled = value; scheduleSave(); emit settingsChanged();
}
void Config::setApproveSoundEnabled(bool value) {
    if (m_approveSoundEnabled == value) { if (m_savePending) scheduleSave(); return; }
    m_approveSoundEnabled = value; scheduleSave(); emit settingsChanged();
}
void Config::setVolume(double value) {
    if (!std::isfinite(value)) return;
    value = qBound(0.0, value, 1.0);
    if (qAbs(m_volume - value) < 0.000001) { if (m_savePending) scheduleSave(); return; }
    m_volume = value; scheduleSave(); emit settingsChanged();
}

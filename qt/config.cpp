#include "config.h"
#include <QDir>
#include <QFile>
#include <QFileInfo>
#include <QJsonDocument>
#include <QJsonObject>
#include <QSaveFile>

#include <QStandardPaths>
#include <cmath>

namespace {
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
    QFile::remove(directory + "/Desktop.conf");
    return path;
}
}
Config::Config(QObject *parent) : Config(defaultPath(), parent) {
    if (m_path.isEmpty()) { m_blocked = true; fail("Configuration location is unavailable."); }

}
Config::Config(const QString &path, QObject *parent) : QObject(parent), m_path(path) { load(); }
void Config::fail(const QString &message) {
    if (m_error == message) return;
    m_error = message;
    emit errorChanged();
}
void Config::load() {
    if (m_path.isEmpty() || !QFileInfo::exists(m_path)) return;
    QFile file(m_path);
    if (!file.open(QIODevice::ReadOnly)) {
        m_blocked = true; fail("Unable to read configuration."); return;
    }
    QJsonParseError parse;
    const auto document = QJsonDocument::fromJson(file.readAll(), &parse);
    const auto object = document.object();
    const auto token = object.value("musicToken");
    const auto repository = object.value("repositoryPath");
    const auto hidden = object.value("hideIgnored");
        const auto windowControls = object.value("hideWindowControls");
    const auto volume = object.value("volume");
    if (file.error() != QFileDevice::NoError || parse.error != QJsonParseError::NoError || !document.isObject()
        || (!token.isUndefined() && !token.isString())
        || (!repository.isUndefined() && !repository.isString())
        || (!hidden.isUndefined() && !hidden.isBool())
                || (!windowControls.isUndefined() && !windowControls.isBool())
        || (!volume.isUndefined() && (!volume.isDouble() || !std::isfinite(volume.toDouble())
                                     || volume.toDouble() < 0 || volume.toDouble() > 1))) {
        m_blocked = true; fail("Invalid configuration; original file has been preserved."); return;
    }
    if (!file.setPermissions(filePermissions)) {
        m_blocked = true; fail("Unable to secure configuration permissions."); return;
    }
    m_token = token.toString();
    m_repositoryPath = repository.toString();
    m_hideIgnored = hidden.toBool(false);
        m_hideWindowControls = windowControls.toBool(false);
    m_volume = volume.toDouble(0.5);
}
void Config::save() {
    if (m_path.isEmpty() || m_blocked) return;
    const auto directory = QFileInfo(m_path).absolutePath();
    if (!QDir().mkpath(directory) || !QFile::setPermissions(directory, directoryPermissions)) {
        fail("Unable to create or secure configuration directory."); return;
    }
    QSaveFile file(m_path);
    file.setDirectWriteFallback(false);
    if (!file.open(QIODevice::WriteOnly) || !file.setPermissions(filePermissions)) {
        fail("Unable to open secure configuration file."); return;
    }
    const auto bytes = QJsonDocument(QJsonObject{{"repositoryPath", m_repositoryPath}, {"musicToken", m_token}, {"hideIgnored", m_hideIgnored}, {"hideWindowControls", m_hideWindowControls}, {"volume", m_volume}}).toJson();
    if (file.write(bytes) != bytes.size() || !file.commit()) {
        fail("Unable to save configuration atomically."); return;
    }
    fail({});
}
void Config::setRepositoryPath(const QString &value) {
    const auto path = value.trimmed();
    if (m_repositoryPath == path) return;
    m_repositoryPath = path; save(); emit settingsChanged();
}
void Config::setMusicToken(const QString &value) {
    if (m_token == value) return;
    m_token = value; save(); emit settingsChanged();
}
void Config::setHideIgnored(bool value) {
    if (m_hideIgnored == value) return;
    m_hideIgnored = value; save(); emit settingsChanged();
}
void Config::setHideWindowControls(bool value) {
    if (m_hideWindowControls == value) return;
    m_hideWindowControls = value; save(); emit settingsChanged();
}
void Config::setVolume(double value) {
    if (!std::isfinite(value)) return;
    value = qBound(0.0, value, 1.0);
    if (qAbs(m_volume - value) < 0.000001) return;
    m_volume = value; save(); emit settingsChanged();
}

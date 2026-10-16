#include "updater.h"
#include "version.h"

#include <QCoreApplication>
#include <QCryptographicHash>
#include <QDesktopServices>
#include <QDir>
#include <QFile>
#include <QFileInfo>
#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>
#include <QNetworkAccessManager>
#include <QNetworkReply>
#include <QNetworkRequest>
#include <QProcess>
#include <QProcessEnvironment>
#include <QSaveFile>
#include <QRegularExpression>
#include <QSysInfo>
#include <QTemporaryDir>
#include <QTimer>

#ifdef Q_OS_LINUX
#include <sys/stat.h>
#include <unistd.h>
#endif

namespace {
const QString repository = QStringLiteral("Web-Next-Music/DDetector");
const qint64 metadataLimit = 2 * 1024 * 1024;
const qint64 downloadLimit = 1024LL * 1024 * 1024;
struct Semver { QStringList core, pre; };
bool parseVersion(QString text, Semver &v) {
    if (text.startsWith('v')) text.remove(0, 1);
    static const QRegularExpression re(QStringLiteral("^(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\\.[0-9A-Za-z-]+)*))?(?:\\+([0-9A-Za-z-]+(?:\\.[0-9A-Za-z-]+)*))?$"));
    const auto match = re.match(text);
    if (!match.hasMatch() || match.capturedLength() != text.size()) return false;
    v.core = {match.captured(1), match.captured(2), match.captured(3)};
    if (!match.captured(4).isEmpty()) v.pre = match.captured(4).split('.');
    for (const auto &part : v.pre) {
        bool numeric = true;
        for (QChar c : part) numeric &= c >= '0' && c <= '9';
        if (numeric && part.size() > 1 && part.startsWith('0')) return false;
    }
    return true;
}
bool numeric(const QString &s) {
    for (QChar c : s) if (c < '0' || c > '9') return false;
    return true;
}
int numberCompare(const QString &a, const QString &b) {
    if (a.size() != b.size()) return a.size() < b.size() ? -1 : 1;
    return QString::compare(a, b, Qt::CaseSensitive);
}

QNetworkRequest request(const QUrl &url) {
    QNetworkRequest r(url);
    r.setRawHeader("User-Agent", "DDetector-native-updater");
    r.setRawHeader("Accept", "application/vnd.github+json");
    r.setAttribute(QNetworkRequest::RedirectPolicyAttribute, QNetworkRequest::NoLessSafeRedirectPolicy);
    r.setMaximumRedirectsAllowed(5);
    r.setTransferTimeout(30000);
    return r;
}
}

Updater::Updater(QObject *parent, bool autoCheck) : QObject(parent), m_network(new QNetworkAccessManager(this)),
    m_page(QUrl("https://github.com/" + repository + "/releases/latest")) {
    m_startDetached = [](QProcess &process) { return process.startDetached(); };
    m_quit = [] { QCoreApplication::quit(); };
    if (autoCheck) {
        auto heartbeat = new QTimer(this);
        heartbeat->setObjectName("updateHeartbeat");
        heartbeat->setTimerType(Qt::PreciseTimer);
        heartbeat->setInterval(30 * 60 * 1000);
        connect(heartbeat, &QTimer::timeout, this, &Updater::check);
        heartbeat->start();
        QTimer::singleShot(0, this, &Updater::check);
    }
}
Updater::~Updater() = default;

bool Updater::releaseUrl(const QUrl &url, bool asset) {
    const QString repoPrefix = '/' + repository + '/';
    const QString suffix = asset ? "releases/download/" : "releases/tag/";
    return url.scheme() == "https" && url.host() == "github.com" && url.userInfo().isEmpty()
        && (url.port() == -1 || url.port() == 443)
        && url.path().startsWith(repoPrefix, Qt::CaseInsensitive)
        && url.path().mid(repoPrefix.size()).startsWith(suffix)
        && url.query().isEmpty() && url.fragment().isEmpty();
}

QString Updater::appImagePathError(const QString &path) {
#ifdef Q_OS_LINUX
    const QFileInfo file(path);
    const QFileInfo directory(file.absolutePath());
    struct stat info {}, parent {};
    if (!file.isAbsolute() || QDir::cleanPath(path) != path || file.isSymLink()
        || file.canonicalFilePath() != path || !file.isFile()
        || ::lstat(QFile::encodeName(path).constData(), &info) != 0 || !S_ISREG(info.st_mode)
        || info.st_uid != ::geteuid() || info.st_nlink != 1
        || (info.st_mode & (S_ISUID | S_ISGID)) != 0
        || (info.st_mode & S_IWUSR) == 0 || (info.st_mode & (S_IXUSR | S_IXGRP | S_IXOTH)) == 0
        || ::access(QFile::encodeName(path).constData(), W_OK | X_OK) != 0) {
        return "APPIMAGE must be an absolute, regular, user-owned writable executable with no symlinks or hard links";
    }
    if (directory.canonicalFilePath() != directory.absoluteFilePath()
        || ::stat(QFile::encodeName(directory.absoluteFilePath()).constData(), &parent) != 0
        || parent.st_uid != ::geteuid() || (parent.st_mode & (S_IWGRP | S_IWOTH)) != 0
        || (parent.st_mode & (S_IWUSR | S_IXUSR)) != (S_IWUSR | S_IXUSR)
        || ::access(QFile::encodeName(directory.absoluteFilePath()).constData(), W_OK | X_OK) != 0) {
        return "AppImage directory must be user-owned, writable, and not writable by other users";
    }
    return {};
#else
    Q_UNUSED(path);
    return "AppImage installation is supported only on Linux";
#endif
}

bool Updater::replaceAppImage(const QString &target, const QString &source, const QByteArray &digest, QString *error) {
    *error = appImagePathError(target);
    if (!error->isEmpty()) return false;
#ifdef Q_OS_LINUX
    struct stat original {}, directory {};
    const auto targetBytes = QFile::encodeName(target);
    const auto directoryBytes = QFile::encodeName(QFileInfo(target).absolutePath());
    if (::lstat(targetBytes.constData(), &original) != 0 || ::stat(directoryBytes.constData(), &directory) != 0) {
        *error = "Could not inspect AppImage before replacement"; return false;
    }
    QFile input(source);
    if (digest.size() != 64 || QFileInfo(source).isSymLink() || !QFileInfo(source).isFile()
        || !input.open(QIODevice::ReadOnly) || input.size() <= 0 || input.size() > downloadLimit) {
        *error = "Invalid verified AppImage staging file"; return false;
    }
    const qint64 expectedSize = input.size();
    // QSaveFile stages in the target directory and renames over the old inode.
    // Never permit its non-atomic truncation fallback for a running image.
    QSaveFile output(target);
    output.setDirectWriteFallback(false);
    if (!output.open(QIODevice::WriteOnly) || !output.setPermissions(QFileInfo(target).permissions())) {
        *error = output.errorString(); return false;
    }
    QCryptographicHash hash(QCryptographicHash::Sha256);
    qint64 copied = 0;
    while (!input.atEnd()) {
        const QByteArray chunk = input.read(65536);
        if (chunk.isEmpty() || copied + chunk.size() > expectedSize || output.write(chunk) != chunk.size()) {
            *error = "Could not stage complete AppImage replacement"; output.cancelWriting(); return false;
        }
        hash.addData(chunk); copied += chunk.size();
    }
    if (copied != expectedSize || hash.result().toHex() != digest.toLower()) {
        *error = "Staged AppImage SHA-256 verification failed"; output.cancelWriting(); return false;
    }
    struct stat current {}, currentDirectory {};
    *error = appImagePathError(target);
    if (!error->isEmpty() || ::lstat(targetBytes.constData(), &current) != 0
        || ::stat(directoryBytes.constData(), &currentDirectory) != 0
        || original.st_dev != current.st_dev || original.st_ino != current.st_ino
        || original.st_mode != current.st_mode || original.st_size != current.st_size
        || original.st_mtim.tv_sec != current.st_mtim.tv_sec || original.st_mtim.tv_nsec != current.st_mtim.tv_nsec
        || directory.st_dev != currentDirectory.st_dev || directory.st_ino != currentDirectory.st_ino) {
        *error = "AppImage path changed during staging; replacement cancelled";
        output.cancelWriting(); return false;
    }
    if (!output.commit()) { *error = output.errorString(); return false; }
    return true;
#else
    Q_UNUSED(source); Q_UNUSED(digest);
    return false;
#endif
}

QProcessEnvironment Updater::restartEnvironment(QProcessEnvironment environment) {
    // Native packages use the same AppRun layout, but do not set APPDIR.
    const QString appDir = QDir::cleanPath(environment.value("APPDIR", "/opt/ddetector"));
    if (QDir::isAbsolutePath(appDir) && appDir != "/") {
        for (const auto &key : {"PATH", "LD_LIBRARY_PATH", "QT_PLUGIN_PATH", "QML_IMPORT_PATH",
             "QML2_IMPORT_PATH", "QT_QPA_PLATFORM_PLUGIN_PATH", "XDG_DATA_DIRS"}) {
            if (!environment.contains(key)) continue;
            QStringList retained;
            for (const auto &entry : environment.value(key).split(':', Qt::SkipEmptyParts)) {
                const QString clean = QDir::cleanPath(entry);
                if (clean != appDir && !clean.startsWith(appDir + '/')) retained.append(entry);
            }
            if (retained.isEmpty()) environment.remove(key);
            else environment.insert(key, retained.join(':'));
        }
    }
    for (const auto &key : {"APPIMAGE", "APPDIR", "OWD", "ARGV0", "DDETECTOR_QT_EXECUTABLE"})
        environment.remove(key);
    return environment;
}

bool Updater::compareVersions(const QString &a, const QString &b, int *result) {
    Semver x, y;
    if (!parseVersion(a, x) || !parseVersion(b, y)) return false;
    int c = 0;
    for (int i = 0; i < 3 && !c; ++i) c = numberCompare(x.core[i], y.core[i]);
    if (!c && x.pre.isEmpty() != y.pre.isEmpty()) c = x.pre.isEmpty() ? 1 : -1;
    if (!c) {
        for (int i = 0; i < qMin(x.pre.size(), y.pre.size()) && !c; ++i) {
            const bool xn = numeric(x.pre[i]), yn = numeric(y.pre[i]);
            c = xn && yn ? numberCompare(x.pre[i], y.pre[i]) : xn != yn ? (xn ? -1 : 1)
                : QString::compare(x.pre[i], y.pre[i], Qt::CaseSensitive);
        }
        if (!c && x.pre.size() != y.pre.size()) c = x.pre.size() < y.pre.size() ? -1 : 1;
    }
    *result = c < 0 ? -1 : c > 0 ? 1 : 0;
    return true;
}
QString Updater::artifactName(Platform p, const QString &version, const QString &architecture) {
    Semver v;
    if (architecture != "x86_64" || !parseVersion(version, v) || !v.pre.isEmpty()) return {};
    QString s = version;
    if (s.startsWith('v')) s.remove(0, 1);
    switch (p) {
    case Platform::Windows: return "DDetector-" + s + "-Setup.exe";
    case Platform::Debian: return "ddetector_" + s + "_amd64.deb";
    case Platform::Arch: return "ddetector-" + s.replace('-', '_') + "-1-x86_64.pkg.tar.zst";
    case Platform::AppImage: return "DDetector-" + s + "-x86_64.AppImage";
    default: return {};
    }
}
void Updater::fail(const QString &message) {
    m_busy = false; m_error = message; m_status = "Update failed"; m_progress = -1;
    emit changed();
}
void Updater::check() { checkRelease(false); }
void Updater::checkRelease(bool force) {
    if (m_busy) return;
    m_forced = false; m_available = false; m_version.clear(); m_release.clear(); m_error.clear();
    m_busy = true; m_progress = -1; m_status = "Checking for updates"; emit changed();
    auto reply = m_network->get(request(QUrl("https://api.github.com/repos/" + repository + "/releases/latest")));
    reply->setReadBufferSize(65536);
    auto body = std::make_shared<QByteArray>();
    auto overflow = std::make_shared<bool>(false);
    connect(reply, &QNetworkReply::readyRead, this, [reply, body, overflow] {
        const auto bytes = reply->readAll();
        if (body->size() + bytes.size() > metadataLimit) { *overflow = true; reply->abort(); }
        else body->append(bytes);
    });
    QTimer::singleShot(60000, reply, [reply] { if (!reply->isFinished()) reply->abort(); });
    connect(reply, &QNetworkReply::finished, this, [this, reply, body, overflow, force] {
        reply->deleteLater();
        if (*overflow) { fail("Release metadata exceeds size limit"); return; }
        if (reply->error() != QNetworkReply::NoError) { fail(reply->errorString()); return; }
        const auto rest = reply->readAll();
        if (body->size() + rest.size() > metadataLimit) { fail("Release metadata exceeds size limit"); return; }
        body->append(rest); acceptRelease(*body, force);
    });
}
void Updater::acceptRelease(const QByteArray &json, bool force) {
    m_forced = false; m_available = false; m_error.clear();
    QJsonParseError parseError;
    const auto doc = QJsonDocument::fromJson(json, &parseError);
    const auto o = doc.object();
    const auto tag = o.value("tag_name").toString();
    Semver parsed;
    const QUrl page(o.value("html_url").toString());
    if (parseError.error != QJsonParseError::NoError || !doc.isObject()
        || !o.value("draft").isBool() || !o.value("prerelease").isBool()
        || o.value("draft").toBool() || o.value("prerelease").toBool()
        || !parseVersion(tag, parsed) || !parsed.pre.isEmpty() || !releaseUrl(page, false)
        || !o.value("assets").isArray()) { fail("Invalid stable release metadata"); return; }
    int comparison = 0;
    const bool known = compareVersions(tag, QStringLiteral(DDETECTOR_GIT_TAG), &comparison);
    m_busy = false; m_version = tag; m_page = page; m_release = json;
    m_forced = force;
    m_available = force || (known && comparison > 0);
    m_status = force ? "Update available" : !known ? "Current build has no semantic release version; automatic installation disabled"
        : m_available ? "Update available" : "Up to date";
    emit changed();
}
#ifdef DDETECTOR_DEV_TOOLS
void Updater::forceAvailable() { checkRelease(true); }
#endif
void Updater::install() {
    if (m_busy || !m_available) return;

    m_busy = true; m_error.clear(); m_status = "Checking installation ownership"; emit changed();
    detectInstallation();
}
void Updater::fallback(const QString &reason) {
    m_busy = false; m_status = reason + "; opening release page for manual installation";
    if (!QDesktopServices::openUrl(m_page)) m_error = "Could not open " + m_page.toString();
    emit changed();
}
void Updater::detectInstallation() {
    if (QSysInfo::currentCpuArchitecture() != "x86_64") { fallback("No published artifact for this architecture"); return; }
#ifdef Q_OS_WIN
    download(Platform::Windows);
#elif defined(Q_OS_LINUX)
    if (!qEnvironmentVariableIsEmpty("APPIMAGE")) {
        m_appImagePath = qEnvironmentVariable("APPIMAGE");
        const QString pathError = appImagePathError(m_appImagePath);
        if (!pathError.isEmpty()) { fallback(pathError); return; }
        download(Platform::AppImage); return;
    }
    const QString executable = QFileInfo(QCoreApplication::applicationFilePath()).canonicalFilePath();
    if (executable != "/opt/ddetector/usr/lib/ddetector/ddetector-qt") {
        // Explicit developer installs may install the native package without owning the checkout.
        // AppImage safety and architecture validation above still take precedence.
        if (m_forced) {
            if (QFileInfo::exists("/etc/arch-release") && QFileInfo::exists("/usr/bin/pacman")) {
                download(Platform::Arch); return;
            }
            if (QFileInfo::exists("/etc/debian_version") && QFileInfo::exists("/usr/bin/dpkg")) {
                download(Platform::Debian); return;
            }
        }
        fallback("This executable is not an owned system package installation"); return;
    }
    auto probe = new QProcess(this);
    // Arch can also have dpkg installed; prefer its native package database.
    const bool arch = QFileInfo::exists("/etc/arch-release") && QFileInfo::exists("/usr/bin/pacman");
    const bool deb = !arch && QFileInfo::exists("/usr/bin/dpkg-query");
    if (!deb && !QFileInfo::exists("/usr/bin/pacman")) { probe->deleteLater(); fallback("Unsupported package manager"); return; }
    auto timedOut = std::make_shared<bool>(false);
    auto timer = new QTimer(probe); timer->setSingleShot(true);
    connect(timer, &QTimer::timeout, probe, [probe, timedOut] { *timedOut = true; probe->kill(); });
    connect(probe, &QProcess::errorOccurred, this, [this, probe](QProcess::ProcessError e) {
        if (e == QProcess::FailedToStart) { probe->deleteLater(); fallback("Could not verify package ownership"); }
    });
    connect(probe, qOverload<int, QProcess::ExitStatus>(&QProcess::finished), this,
        [this, probe, timer, timedOut, deb](int code, QProcess::ExitStatus exit) {
        timer->stop(); const auto output = probe->readAllStandardOutput().trimmed(); probe->deleteLater();
        const bool owner = deb ? output == "ddetector: /opt/ddetector/usr/lib/ddetector/ddetector-qt"
            : output == "ddetector";
        if (*timedOut || code != 0 || exit != QProcess::NormalExit || !owner) {
            fallback("Could not verify DDetector package ownership"); return;
        }
        download(deb ? Platform::Debian : Platform::Arch);
    });
    probe->start(deb ? "/usr/bin/dpkg-query" : "/usr/bin/pacman",
        deb ? QStringList{"--search", executable} : QStringList{"-Qqo", "--", executable});
    timer->start(10000);
#else
    fallback("Native installation is unsupported on this platform");
#endif
}
void Updater::download(Platform platform) {
    const auto name = artifactName(platform, m_version, QSysInfo::currentCpuArchitecture());
    QJsonObject asset; int matches = 0;
    for (const auto &v : QJsonDocument::fromJson(m_release).object().value("assets").toArray()) {
        if (v.toObject().value("name").toString() == name && !name.isEmpty()) { asset = v.toObject(); ++matches; }
    }
    if (matches != 1) { fallback("No unique matching release artifact"); return; }
    const auto digest = asset.value("digest").toString();
    static const QRegularExpression sha("^sha256:[0-9a-fA-F]{64}$");
    if (!sha.match(digest).hasMatch()) { fallback("GitHub SHA-256 digest is missing or invalid; executable installation refused"); return; }
    const QUrl url(asset.value("browser_download_url").toString());
    const double sizeValue = asset.value("size").toDouble(-1);
    if (!releaseUrl(url, true) || sizeValue <= 0 || sizeValue > downloadLimit || sizeValue != qint64(sizeValue)) {
        fail("Invalid artifact URL or size"); return;
    }
    m_downloadDigest = digest.mid(7).toLatin1().toLower();
    const qint64 expectedSize = qint64(sizeValue);
    m_staging = std::make_unique<QTemporaryDir>();
    if (!m_staging->isValid()) { fail("Could not create private update staging directory"); return; }
    const QString path = m_staging->filePath(name);
    auto file = std::make_shared<QFile>(path);
    if (!file->open(QIODevice::WriteOnly | QIODevice::NewOnly)) { fail(file->errorString()); return; }
    file->setPermissions(QFileDevice::ReadOwner | QFileDevice::WriteOwner);
    auto hash = std::make_shared<QCryptographicHash>(QCryptographicHash::Sha256);
    auto bytes = std::make_shared<qint64>(0);
    auto failure = std::make_shared<QString>();
    auto reply = m_network->get(request(url)); reply->setReadBufferSize(65536);
    m_status = "Downloading update"; m_progress = 0; emit changed();
    auto drain = [this, reply, file, hash, bytes, failure, expectedSize] {
        while (reply->bytesAvailable() > 0) {
            const auto chunk = reply->read(65536);
            if (*bytes + chunk.size() > expectedSize || *bytes + chunk.size() > downloadLimit) {
                *failure = "Download exceeds declared size"; reply->abort(); return;
            }
            if (file->write(chunk) != chunk.size()) { *failure = file->errorString(); reply->abort(); return; }
            hash->addData(chunk); *bytes += chunk.size();
        }
        m_progress = double(*bytes) / expectedSize; emit changed();
    };
    connect(reply, &QNetworkReply::readyRead, this, drain);
    QTimer::singleShot(15 * 60 * 1000, reply, [reply] { if (!reply->isFinished()) reply->abort(); });
    connect(reply, &QNetworkReply::finished, this,
        [this, reply, file, hash, bytes, failure, expectedSize, digest, path, platform, drain] {
        drain(); reply->deleteLater();
        const bool flushed = file->flush(); file->close();
        if (!failure->isEmpty()) { fail(*failure); return; }
        if (reply->error() != QNetworkReply::NoError) { fail(reply->errorString()); return; }
        if (!flushed || *bytes != expectedSize || hash->result().toHex() != digest.mid(7).toLatin1().toLower()) {
            fail("Update size or SHA-256 verification failed"); return;
        }
        launchInstaller(platform, path);
    });
}
void Updater::configureLauncher(QProcess &process, const QString &path, bool installer) {
    process.setProgram(path);
    process.setArguments(installer ? QStringList{"/RESTARTAPP"} : QStringList{});
    process.setWorkingDirectory(QFileInfo(path).absolutePath());
    process.setProcessEnvironment(restartEnvironment(QProcessEnvironment::systemEnvironment()));
}

void Updater::restartLauncher(const QString &path) {
    QProcess restart;
    configureLauncher(restart, path);
    // Launch AppRun -> Go -> Qt with fresh bridge credentials, never the old Qt argv.
    if (!m_startDetached(restart)) {
        fail("Update installed, but restart failed; reopen DDetector through its launcher manually");
        return;
    }
    m_busy = false; m_status = "Update installed; restarting"; emit changed();
    m_quit();
}

void Updater::packageFinished(int code, bool normalExit) {
    if (code != 0 || !normalExit) {
        fail("Package installation failed or authorization was cancelled (exit " + QString::number(code) + ')');
        return;
    }
    m_available = false; m_staging.reset();
    restartLauncher("/usr/bin/ddetector");
}

void Updater::launchInstaller(Platform platform, const QString &path) {
    if (platform == Platform::AppImage) {
        QString replacementError;
        if (!replaceAppImage(m_appImagePath, path, m_downloadDigest, &replacementError)) {
            fail(replacementError); return;
        }
        m_available = false; m_staging.reset();
        restartLauncher(m_appImagePath); return;
    }
    if (platform == Platform::Windows) {
        QProcess installer;
        configureLauncher(installer, path, true);
        if (!m_startDetached(installer)) { fail("Could not start Windows installer"); return; }
        // The detached installer may still be reading its file after Qt exits.
        m_staging->setAutoRemove(false);
        m_busy = false; m_status = "Installer started; closing DDetector"; emit changed();
        m_quit(); return;
    }
    if (platform != Platform::Debian && platform != Platform::Arch) { fallback("Unsupported installer"); return; }
    if (!QFileInfo::exists("/usr/bin/pkexec")) { fallback("pkexec is unavailable"); return; }
    auto process = new QProcess(this);
    process->setProcessChannelMode(QProcess::ForwardedChannels);
    connect(process, &QProcess::errorOccurred, this, [this, process](QProcess::ProcessError e) {
        if (e == QProcess::FailedToStart) { process->deleteLater(); fail("Could not start pkexec installer"); }
    });
    connect(process, qOverload<int, QProcess::ExitStatus>(&QProcess::finished), this,
        [this, process](int code, QProcess::ExitStatus exit) {
        process->deleteLater();
        packageFinished(code, exit == QProcess::NormalExit);
    });
    m_status = "Waiting for authorized package installation"; emit changed();
    process->start("/usr/bin/pkexec", platform == Platform::Debian
        ? QStringList{"/usr/bin/dpkg", "--install", path}
        : QStringList{"/usr/bin/pacman", "-U", "--noconfirm", "--", path});
}

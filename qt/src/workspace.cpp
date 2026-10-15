#include "workspace.h"

#include <QDir>
#include <QDirIterator>
#include <QFile>
#include <QFileInfo>
#include <QSaveFile>
#include <QStandardPaths>
#include <QTemporaryDir>
#include <QProcessEnvironment>
#include <QProcess>
#include <QTimer>
#include <QStringEncoder>
#include <QLockFile>
#include <memory>
#include <algorithm>
#ifdef Q_OS_UNIX
#include <fcntl.h>
#include <sys/stat.h>
#include <unistd.h>
#endif
#include <git2.h>
#include <QtConcurrent>
#include <QFutureWatcher>

namespace {
// Qt's UTF-16 converter can preserve unpaired surrogates without reporting errors.
bool validUnicode(const QString &text) {
    for (qsizetype i = 0; i < text.size(); ++i) {
        if (text.at(i).isHighSurrogate()) {
            if (++i == text.size() || !text.at(i).isLowSurrogate()) return false;
        } else if (text.at(i).isLowSurrogate()) return false;
    }
    return true;
}
QByteArray untrackedPreview(const QString &root, const QString &path, QString &error) {
    QFile file;
#ifdef Q_OS_UNIX
    // Open every component relative to an already-open directory, so a concurrent
    // symlink replacement cannot redirect the preview outside the working tree.
    int fd = ::open(QFile::encodeName(root).constData(), O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC);
    const auto parts = path.split('/');
    for (qsizetype i = 0; fd >= 0 && i < parts.size(); ++i) {
        const bool last = i + 1 == parts.size();
        const int next = ::openat(fd, QFile::encodeName(parts[i]).constData(),
            O_RDONLY | O_NOFOLLOW | O_CLOEXEC | (last ? O_NONBLOCK : O_DIRECTORY));
        ::close(fd); fd = next;
    }
    struct stat info {};
    if (fd < 0 || ::fstat(fd, &info) != 0 || !S_ISREG(info.st_mode)) {
        if (fd >= 0) ::close(fd);
        error = "Cannot safely preview this untracked file"; return {};
    }
    if (!file.open(fd, QIODevice::ReadOnly, QFileDevice::AutoCloseHandle)) {
        ::close(fd); error = "Cannot open untracked file"; return {};
    }
#else
    Q_UNUSED(root);
    Q_UNUSED(path);
    error = "Safe untracked previews are unavailable on this platform"; return {};
#endif
    constexpr qint64 maximumPreviewBytes = GitWorkspace::MaximumTextBytes;
    if (file.size() > maximumPreviewBytes) {
        error = "Untracked file is too large to preview (maximum 2 MiB)"; return {};
    }
    const auto bytes = file.read(maximumPreviewBytes + 1);
    if (file.error() != QFileDevice::NoError || bytes.size() > maximumPreviewBytes) {
        error = "Cannot read untracked file within the preview limit"; return {};
    }
    QStringDecoder decoder(QStringConverter::Utf8);
    const QString text = decoder.decode(bytes);
    bool binary = decoder.hasError();
    for (const auto ch : text)
        if (ch.unicode() < 32 && ch != '\n' && ch != '\r' && ch != '\t') binary = true;
    if (binary) { error = "Binary or non-UTF-8 untracked file cannot be previewed"; return {}; }
    return QByteArray("Untracked file: ") + path.toUtf8() + "\n\n" + bytes;
}

QString editorText(QString text) {
    text.replace("\r\n", "\n");
    text.replace('\r', '\n');
    return text;
}
}

GitWorkspace::GitWorkspace(QObject *parent) : QObject(parent) {
    m_refreshDebounce.setSingleShot(true);
    m_refreshDebounce.setInterval(60);
    connect(&m_refreshDebounce, &QTimer::timeout, this, &GitWorkspace::refreshTree);
    const auto schedule = [this](const QString &) { m_refreshDebounce.start(); };
    connect(&m_watcher, &QFileSystemWatcher::directoryChanged, this, schedule);
    connect(&m_watcher, &QFileSystemWatcher::fileChanged, this, schedule);
    // File-watch limits and ignore/config changes must not silently leave stale status.
    m_reconcile.setInterval(2000);
    connect(&m_reconcile, &QTimer::timeout, this, &GitWorkspace::refreshTree);
    connect(&m_terminal, &TerminalSession::output, this, &GitWorkspace::appendOutput);
    connect(&m_terminal, &TerminalSession::changed, this, [this] { emit terminalChanged(); });
    connect(&m_terminal, &TerminalSession::finished, this, [this] {
        appendOutput("\n[shell exited]\n");
    });
}

GitWorkspace::~GitWorkspace() {
    cancelGit();
    // Normal operation never waits; teardown reaps already-killed children before
    // QObject destroys them (including processes retired by repository switches).
    for (auto *process : findChildren<QProcess *>(QString(), Qt::FindDirectChildrenOnly)) {
        if (process->state() != QProcess::NotRunning) { process->kill(); process->waitForFinished(1000); }
    }
}
void GitWorkspace::activate() {
    refreshGit();
    if (m_activationRequested) return;
    m_activationRequested = true;
    emit activationRequested();
}

QString GitWorkspace::error() const {
    QStringList errors;
    for (const auto &error : {m_error, m_gitError, m_watchError})
        if (!error.isEmpty()) errors.append(error);
    return errors.join('\n');
}
void GitWorkspace::cancelGit() {
    ++m_gitGeneration;
    if (m_gitOperation) {
        auto *process = m_gitOperation; m_gitOperation = nullptr;
        process->disconnect();
        connect(process, &QProcess::finished, process, &QObject::deleteLater);
        process->kill();
        if (process->state() == QProcess::NotRunning) process->deleteLater();
    }
    m_gitBusy = m_gitActionActive = m_gitRefreshAgain = false;
    m_commits.clear(); m_branch.clear(); m_aheadCount = 0; m_gitMessage.clear();
    if (!m_git) return;
    auto *process = m_git; m_git = nullptr;
    process->disconnect();
    connect(process, &QProcess::finished, process, &QObject::deleteLater);
    process->kill();
    if (process->state() == QProcess::NotRunning) process->deleteLater();
}

void GitWorkspace::fail(const QString &message) {
    m_error = message;
    emit changed();
}

void GitWorkspace::setRepositoryPath(const QString &path) {
    if (path == m_repository && (!m_root.isEmpty() || m_resolving || path.trimmed().isEmpty())) return;
    if (m_repositoryValidator) {
        const auto rejection = m_repositoryValidator(path);
        if (!rejection.isEmpty()) { fail(rejection); return; }
    }
    if (dirty() || m_pending) {
        fail("Save or discard the current document before changing the repository");
        return;
    }
    if (m_gitActionActive) {
        fail("Wait for the Git action before changing the repository");
        return;
    }
    if (terminalRunning()) {
        fail("Stop the shell before changing the repository");
        return;
    }
    cancelGit();
    m_resolving = false; m_refreshActive = false; m_refreshAgain = false;
    m_gitError.clear(); m_watchError.clear(); m_reconcile.stop();
    m_loadingDocument = true;
    emit documentAboutToChange();
    ++m_documentRevision;
    m_refreshDebounce.stop();
    if (!m_watcher.files().isEmpty()) m_watcher.removePaths(m_watcher.files());
    if (!m_watcher.directories().isEmpty()) m_watcher.removePaths(m_watcher.directories());
    m_gitStatus.clear(); m_gitDirectories.clear(); m_trackedFiles.clear();
    m_repository = path;
    m_root.clear(); m_file.clear(); m_text.clear(); m_saved.clear();
    m_largeFileMode = false;
    m_diskBytes.clear(); m_tree.clear(); m_expanded.clear(); m_error.clear();
    if (!m_changedFiles.isEmpty()) { m_changedFiles.clear(); emit changesChanged(); }
    if (!m_stashes.isEmpty()) { m_stashes.clear(); emit stashesChanged(); }
    if (!m_diffText.isEmpty()) { m_diffText.clear(); emit diffChanged(); }
    emit gitChanged();
    emit gitStateChanged();
    emit treeChanged();
    emit documentChanged();
    m_loadingDocument = false;
    if (path.trimmed().isEmpty()) { emit changed(); return; }
    QFileInfo input(path);
    if (!input.isDir()) { fail("Repository path must be an existing directory"); return; }
    m_resolving = true;
    struct Discovery { QString root; QStringList directories; };
    const auto generation = m_gitGeneration;
    const auto directory = input.canonicalFilePath();
    auto *watcher = new QFutureWatcher<Discovery>(this);
    connect(watcher, &QFutureWatcher<Discovery>::finished, this, [this, watcher, generation, directory] {
        const auto result = watcher->result(); watcher->deleteLater();
        if (generation != m_gitGeneration) return;
        if (result.root.isEmpty()) {
            m_resolving = false; m_gitError = "Git failed: no working tree found"; emit changed(); return;
        }
        m_root = result.root; m_gitDirectories = result.directories;
        m_resolving = false;
        emit changed();
        if (generation != m_gitGeneration) return;
        m_reconcile.start(); rebuildTree(); refreshTree();
    });
    watcher->setFuture(QtConcurrent::run([directory] {
        Discovery result;
        git_libgit2_init();
        git_repository *repository = nullptr;
        const auto encoded = QFile::encodeName(directory);
        // Match the CLI's sanitized GIT_* environment, including worktrees.
        if (git_repository_open_ext(&repository, encoded.constData(), 0, nullptr) == 0
            && !git_repository_is_bare(repository)) {
            result.root = QFileInfo(QFile::decodeName(git_repository_workdir(repository))).canonicalFilePath();
            for (const char *path : {git_repository_path(repository), git_repository_commondir(repository)}) {
                const auto canonical = QFileInfo(QFile::decodeName(path)).canonicalFilePath();
                if (!canonical.isEmpty() && !result.directories.contains(canonical)) result.directories.append(canonical);
            }
        }
        git_repository_free(repository); git_libgit2_shutdown();
        return result;
    }));
    emit changed();

}

QString GitWorkspace::confined(const QString &relativePath, bool directory) const {
    if (m_root.isEmpty() || relativePath.isEmpty() || QDir::isAbsolutePath(relativePath)
        || relativePath.contains('\\')) return {};
    const auto parts = relativePath.split('/');
    QString path = m_root;
    for (const QString &part : parts) {
        if (part.isEmpty() || part == "." || part == ".."
            || part.compare(".git", Qt::CaseInsensitive) == 0 || part.contains(':')) return {};
        path = QDir(path).filePath(part);
        QFileInfo info(path);
        if (info.isSymLink() || !info.exists()) return {};
        const QString canonical = info.canonicalFilePath();
        const QString prefix = m_root.endsWith('/') ? m_root : m_root + '/';
        if (!canonical.startsWith(prefix)) return {};
    }
    QFileInfo info(path);
    if (directory ? !info.isDir() : !info.isFile()) return {};
    return path;
}

void GitWorkspace::appendTree(const QString &relativePath, int depth) {
    const QString directory = relativePath.isEmpty() ? m_root : confined(relativePath, true);
    if (directory.isEmpty()) return;
    const auto entries = QDir(directory).entryInfoList(
        QDir::AllEntries | QDir::NoDotAndDotDot | QDir::Hidden | QDir::System,
        QDir::DirsFirst | QDir::Name | QDir::IgnoreCase);
    for (const auto &entry : entries) {
        if (entry.isSymLink() || entry.fileName().compare(".git", Qt::CaseInsensitive) == 0
            || (!entry.isDir() && !entry.isFile())) continue;
        const QString rel = relativePath.isEmpty() ? entry.fileName() : relativePath + '/' + entry.fileName();
        if (confined(rel, entry.isDir()).isEmpty()) continue;
        bool expanded = entry.isDir() && m_expanded.contains(rel);
        const QString status = m_gitStatus.value(rel);
        m_tree.append(QVariantMap{{"name", entry.fileName()}, {"path", rel},
                                {"directory", entry.isDir()}, {"depth", depth}, {"expanded", expanded},
                                {"status", status},
                                {"statusColor", status == "new" ? "#00a682" : status == "modified" ? "#ab910d" : ""}});
        if (expanded) appendTree(rel, depth + 1);
    }
}

void GitWorkspace::runGit(const QString &directory, const QStringList &arguments, GitCallback callback, bool operations) {
    Q_ASSERT(!(operations ? m_gitOperation : m_git));
    auto *process = new QProcess(this);
    (operations ? m_gitOperation : m_git) = process;
    const auto generation = m_gitGeneration;
    auto env = QProcessEnvironment::systemEnvironment();
    for (const QString &key : env.keys()) if (key.startsWith("GIT_")) env.remove(key);
    env.insert("GIT_OPTIONAL_LOCKS", "0");
    env.insert("GIT_TERMINAL_PROMPT", "0");
    if (operations) {
        env.insert("GIT_ASKPASS", "/bin/false");
        env.insert("SSH_ASKPASS", "/bin/false");
        env.insert("GIT_SSH_COMMAND", "ssh -oBatchMode=yes -oConnectTimeout=15 -oStrictHostKeyChecking=yes");
        env.insert("GIT_EDITOR", "/bin/false");
        env.insert("GIT_SEQUENCE_EDITOR", "/bin/false");
        env.insert("GCM_INTERACTIVE", "Never");
    }
    process->setProcessEnvironment(env);
    process->setWorkingDirectory(directory);
    struct Output { QByteArray out, err; QString failure; bool completed = false; };
    const auto output = std::make_shared<Output>();
    auto *deadline = new QTimer(process);
    deadline->setSingleShot(true);
    const auto drain = [process, output] {
        constexpr qsizetype maximumOutput = 16 * 1024 * 1024;
        output->out += process->readAllStandardOutput();
        output->err += process->readAllStandardError();
        if (output->out.size() > maximumOutput || output->err.size() > 64 * 1024) {
            output->failure = "Git output exceeds the safety limit; previous status retained";
            output->out.truncate(maximumOutput); output->err.truncate(64 * 1024); process->kill();
        }
    };
    const auto complete = [this, process, deadline, output, generation, operations, callback = std::move(callback), drain] {
        if (output->completed) return;
        output->completed = true;
        deadline->stop(); drain();
        if (output->failure.isEmpty() && (process->exitStatus() != QProcess::NormalExit || process->exitCode() != 0))
            output->failure = "Git failed: " + QString::fromUtf8(output->err).trimmed().left(1024);
        auto &slot = operations ? m_gitOperation : m_git;
        if (slot == process) slot = nullptr;
        process->deleteLater();
        if (generation == m_gitGeneration) callback(output->out, output->failure);
    };
    connect(process, &QProcess::readyReadStandardOutput, this, drain);
    connect(process, &QProcess::readyReadStandardError, this, drain);
    connect(process, &QProcess::finished, this, complete);
    connect(process, &QProcess::errorOccurred, this, [output, complete](QProcess::ProcessError error) {
        if (error == QProcess::FailedToStart) { output->failure = "Cannot start Git"; complete(); }
    });
    connect(deadline, &QTimer::timeout, this, [process, output, operations] {
        output->failure = operations
            ? "Git timed out; inspect repository and remote state before retrying"
            : "Git timed out; previous status retained";
        process->kill();
    });
    deadline->start(operations ? 60000 : m_gitDeadlineMs);
    QStringList options{"--no-pager", "-c", "core.fsmonitor=false"};
    if (operations) options += QStringList{"-c", "credential.interactive=false"};
    process->setStandardInputFile(QProcess::nullDevice());
    process->start("git", options + arguments);
}
void GitWorkspace::refreshGit() {
    if (m_root.isEmpty() || m_resolving) return;
    if (m_gitBusy) { m_gitRefreshAgain = true; return; }
    m_gitBusy = true;
    m_gitRefreshAgain = false;
    const auto generation = m_gitGeneration;
    if (generation != m_gitGeneration) return;
    runGit(m_root, {"stash", "list", "--format=%gd%x00%H%x00%gs", "-z"},
        [this](const QByteArray &out, const QString &error) {
        if (error.isEmpty()) {
            QVariantList stashes;
            const auto fields = out.split('\0');
            for (qsizetype i = 0; i + 2 < fields.size(); i += 3)
                stashes.append(QVariantMap{{"ref", QString::fromUtf8(fields[i])},
                    {"hash", QString::fromUtf8(fields[i + 1])}, {"subject", QString::fromUtf8(fields[i + 2])}});
            if (m_stashes != stashes) { m_stashes = stashes; emit stashesChanged(); }
        }
    runGit(m_root, {"branch", "--show-current"}, [this](const QByteArray &out, const QString &error) {
        if (!error.isEmpty()) {
            m_gitBusy = false;
            if (m_gitMessage != error) { m_gitMessage = error; emit gitStateChanged(); }
            return;
        }
        const QString branch = QString::fromUtf8(out).trimmed();
        // Missing upstream (or an unborn/detached HEAD) is normal: keep Fetch in that case.
        runGit(m_root, {"rev-list", "--count", "@{upstream}..HEAD"},
            [this, branch](const QByteArray &out, const QString &error) {
        const int ahead = error.isEmpty() ? out.trimmed().toInt() : 0;
        // --all also handles an unborn HEAD without treating a new repository as an error.
        runGit(m_root, {"log", "--all", "-100", "--no-show-signature", "--decorate=short",
                       "--format=%H%x00%h%x00%s%x00%an%x00%aI%x00%D", "-z"},
            [this, branch, ahead, generation = m_gitGeneration](const QByteArray &out, const QString &error) {
                const auto previousMessage = m_gitMessage;
                bool historyChanged = false;
                if (error.isEmpty()) {
                    const auto fields = out.split('\0');
                    QVariantList commits;
                    for (qsizetype i = 0; i + 5 < fields.size(); i += 6) {
                        commits.append(QVariantMap{{"hash", QString::fromUtf8(fields[i])},
                            {"shortHash", QString::fromUtf8(fields[i + 1])},
                            {"subject", QString::fromUtf8(fields[i + 2])},
                            {"author", QString::fromUtf8(fields[i + 3])},
                            {"date", QString::fromUtf8(fields[i + 4])},
                            {"refs", QString::fromUtf8(fields[i + 5])}});
                    }
                    historyChanged = m_commits != commits || m_branch != branch || m_aheadCount != ahead;
                    m_commits = commits; m_branch = branch; m_aheadCount = ahead;
                } else m_gitMessage = error;
                m_gitBusy = false;
                const bool again = m_gitRefreshAgain;
                m_gitRefreshAgain = false;
                if (historyChanged) emit gitChanged();
                if (m_gitMessage != previousMessage) emit gitStateChanged();
                if (again && generation == m_gitGeneration) refreshGit();
            }, true);
        }, true);
    }, true);
    }, true);
}

void GitWorkspace::refreshStashes() { refreshGit(); }

void GitWorkspace::viewDiff(QString path) { gitAction("viewDiff", path); }

bool GitWorkspace::validGitPath(const QString &path) const {
    if (path.isEmpty() || QDir::isAbsolutePath(path) || path.contains(QChar::Null)
        || path.contains('\\')) return false;
    for (const auto &part : path.split('/'))
        if (part.isEmpty() || part == "." || part == ".." || part.contains(':')
            || part.compare(".git", Qt::CaseInsensitive) == 0) return false;
    // Deleted paths are allowed, but never traverse an existing symlink.
    QString current = m_root;
    for (const auto &part : path.split('/')) {
        current = QDir(current).filePath(part);
        if (QFileInfo(current).isSymLink()) return false;
    }
    return true;
}

void GitWorkspace::stageFile(const QString &path, bool staged) {
    gitAction(staged ? "stageFile" : "unstageFile", path);
}
void GitWorkspace::stageAll(bool staged) { gitAction(staged ? "stageAll" : "unstageAll"); }

void GitWorkspace::refreshChanges(const QStringList &tracked, quint64 revision) {
    const auto finish = [this, revision](const QString &error) {
        if (revision != m_actionRevision || m_gitActionActive) m_refreshAgain = true;
        else if (m_gitError != error) { m_gitError = error; emit changed(); }
        finishRefresh();
    };
    runGit(m_root, {"status", "--porcelain=v1", "-z", "--untracked-files=all"},
        [this, tracked, revision, finish](const QByteArray &status, const QString &error) {
        if (!error.isEmpty()) { finish(error); return; }
        runGit(m_root, {"diff", "--numstat", "-z", "--no-renames", "--no-ext-diff", "--no-textconv"},
            [this, tracked, revision, finish, status](const QByteArray &work, const QString &error) {
            if (!error.isEmpty()) { finish(error); return; }
            runGit(m_root, {"diff", "--cached", "--numstat", "-z", "--no-renames", "--no-ext-diff", "--no-textconv"},
                [this, tracked, revision, finish, status, work](const QByteArray &index, const QString &error) {
                if (!error.isEmpty()) { finish(error); return; }
                if (revision != m_actionRevision || m_gitActionActive) { finish({}); return; }
                QHash<QString, QPair<qint64, qint64>> counts;
                QSet<QString> binaries;
                for (const auto &data : {work, index}) for (const auto &record : data.split('\0')) {
                    const auto first = record.indexOf('\t');
                    const auto second = record.indexOf('\t', first + 1);
                    if (first < 0 || second < 0) continue;
                    const auto path = QFile::decodeName(record.mid(second + 1));
                    if (record.left(first) == "-" || record.mid(first + 1, second - first - 1) == "-")
                        binaries.insert(path);
                    else {
                        counts[path].first += record.left(first).toLongLong();
                        counts[path].second += record.mid(first + 1, second - first - 1).toLongLong();
                    }
                }
                QVariantList changes;
                const auto records = status.split('\0');
                for (qsizetype i = 0; i < records.size(); ++i) {
                    const auto &record = records[i];
                    if (record.size() < 4 || record[2] != ' ') continue;
                    const auto xy = record.left(2);
                    const auto path = QFile::decodeName(record.mid(3));
                    const bool untracked = xy == "??";
                    changes.append(QVariantMap{{"path", path}, {"status", QString::fromLatin1(xy)},
                        {"untracked", untracked}, {"staged", !untracked && xy[0] != ' '},
                        {"additions", binaries.contains(path) ? QVariant() : QVariant(counts.value(path).first)},
                        {"deletions", binaries.contains(path) ? QVariant() : QVariant(counts.value(path).second)}});
                    if (xy.contains('R') || xy.contains('C')) ++i;
                }
                std::stable_sort(changes.begin(), changes.end(), [](const QVariant &a, const QVariant &b) {
                    return !a.toMap().value("untracked").toBool() && b.toMap().value("untracked").toBool();
                });
                updateGitStatus(status); m_trackedFiles = tracked;
                if (m_changedFiles != changes) { m_changedFiles = changes; emit changesChanged(); }
                finish({});
            });
        });
    });
}

void GitWorkspace::gitAction(QString action, QString message) {
    gitActionWithOptions(action, message, false, false, false);
}

void GitWorkspace::commitWithOptions(QString message, bool amend, bool signoff, bool skipHooks, bool stagedOnly) {
    gitActionWithOptions(stagedOnly ? "commitStaged" : "commit", message, amend, signoff, skipHooks);
}

void GitWorkspace::gitActionWithOptions(QString action, QString message, bool amend, bool signoff, bool skipHooks) {
    const auto reject = [this](const QString &error) {
            if (m_gitMessage == error) return;
            m_gitMessage = error; emit gitStateChanged();
        };
    if (m_gitActionActive) { reject("Wait for the current Git operation"); return; }
    if (m_root.isEmpty() || m_resolving) { reject("No repository is ready"); return; }
    if (action == "viewStash") { refreshStashes(); return; }
    const QStringList supported{"fetch", "pull", "push", "commit", "commitStaged",
        "stashAll", "stashTracked", "stashPop", "discardTracked", "trashUntracked",
        "stageFile", "unstageFile", "stageAll", "unstageAll", "stageTracked", "unstageTracked", "viewDiff", "undoCommit"};
    if (!supported.contains(action)) {
        reject("Unknown Git action: " + action); return;
    }
    if ((action == "stageFile" || action == "unstageFile"
         || (action == "viewDiff" && !message.isEmpty())) && !validGitPath(message)) {
        reject("Invalid repository-relative file path"); return;
    }
    if ((action == "commit" || action == "commitStaged") && (message.trimmed().isEmpty() || message.contains(QChar::Null))) {
        reject("Enter a non-empty commit message without NUL characters"); return;
    }
    if (action != "viewDiff" && m_writeValidator) {
        const auto error = m_writeValidator();
        if (!error.isEmpty()) { reject(error); return; }
    }
    QStringList commitArguments;
    std::shared_ptr<QTemporaryDir> hooksDirectory;
    if (action == "commit" || action == "commitStaged") {
        if (skipHooks) {
            hooksDirectory = std::make_shared<QTemporaryDir>();
            if (!hooksDirectory->isValid()) { reject("Cannot create an empty hooks directory"); return; }
            commitArguments << "-c" << ("core.hooksPath=" + hooksDirectory->path());
        }
        commitArguments << "commit";
        if (amend) commitArguments << "--amend";
        if (signoff) commitArguments << "--signoff";
        commitArguments << "-m" << message;
    }
    // Actions take priority over disposable background history reads. Disconnect
    // first so the cancelled read cannot launch its next stage or publish results.
    if (m_gitOperation) {
        auto *process = m_gitOperation; m_gitOperation = nullptr;
        process->disconnect();
        connect(process, &QProcess::finished, process, &QObject::deleteLater);
        process->kill();
        if (process->state() == QProcess::NotRunning) process->deleteLater();
    }
    m_gitRefreshAgain = false;
    ++m_actionRevision;
    m_gitBusy = m_gitActionActive = true;
    m_gitMessage.clear(); emit gitStateChanged();
    const auto complete = [this, action, hooksDirectory, generation = m_gitGeneration](const QByteArray &out, const QString &error) {
        m_gitBusy = m_gitActionActive = false;
        const bool quiet = action == "stageFile" || action == "unstageFile"
            || action == "stageAll" || action == "unstageAll" || action == "viewDiff"
                        || action == "stageTracked" || action == "unstageTracked";
        m_gitMessage = error.isEmpty() ? (quiet ? QString() : action + " succeeded") : error;
        if (action == "viewDiff") {
            m_diffText = error.isEmpty()
                ? (out.isEmpty() ? QString("No tracked differences from HEAD. Untracked files are not included.")
                                 : QString::fromUtf8(out))
                : error;
            emit diffChanged();
        } else if (!quiet && error.isEmpty() && !out.trimmed().isEmpty())
            m_gitMessage += ": " + QString::fromUtf8(out).trimmed().left(1024);
        emit gitStateChanged();
        if (generation != m_gitGeneration) return;
        if (error.isEmpty() && (action == "commit" || action == "commitStaged")) emit commitSucceeded();
        if (generation != m_gitGeneration) return;
        refreshTree();
        if (generation == m_gitGeneration) refreshGit();
    };
    if (action == "commit") {
        runGit(m_root, {"add", "-u"}, [this, commitArguments, complete](const QByteArray &out, const QString &error) {
            if (!error.isEmpty()) { complete(out, error); return; }
            if (m_writeValidator) {
                const auto rejection = m_writeValidator();
                if (!rejection.isEmpty()) { complete({}, rejection); return; }
            }
            runGit(m_root, commitArguments, complete, true);
        }, true);
    } else {
        QStringList arguments{action};
        if (action == "viewDiff") {
            arguments = {"--literal-pathspecs", "diff", "--no-ext-diff", "--no-textconv",
                         "--no-color", "HEAD", "--"};
            if (!message.isEmpty()) {
                arguments.append(message);
                runGit(m_root, {"--literal-pathspecs", "status", "--porcelain=v1", "-z",
                               "--untracked-files=all", "--", message},
                    [this, message, arguments, complete](const QByteArray &status, const QString &error) {
                    if (!error.isEmpty()) { complete({}, error); return; }
                    const auto expected = QByteArray("?? ") + QFile::encodeName(message);
                    if (status.split('\0').contains(expected)) {
                        if (confined(message).isEmpty()) { complete({}, "Unsafe untracked file path"); return; }
                        QString previewError;
                        const auto preview = untrackedPreview(m_root, message, previewError);
                        complete(preview, previewError);
                    } else runGit(m_root, arguments, complete, true);
                }, true);
                return;
            }
        } else if (action == "undoCommit") arguments = {"reset", "--soft", "HEAD~1"};
        else if (action == "commitStaged") arguments = commitArguments;
        else if (action == "stashAll") arguments = {"stash", "push", "--include-untracked"};
        else if (action == "stashTracked") arguments = {"stash", "push"};
        else if (action == "stashPop") arguments = {"stash", "pop"};
        else if (action == "discardTracked") arguments = {"restore", "--source=HEAD", "--staged", "--worktree", "--", "."};
        else if (action == "stageFile") arguments = {"--literal-pathspecs", "add", "--", message};
        else if (action == "stageAll") arguments = {"add", "--all", "--", "."};
                else if (action == "stageTracked" || action == "unstageTracked") {
                    QStringList paths;
                    for (const auto &entry : m_changedFiles) {
                        const auto file = entry.toMap();
                        if (!file.value("untracked").toBool())
                            paths.append(file.value("path").toString());
                    }
                    if (paths.isEmpty()) { complete({}, {}); return; }
                    arguments = {"--literal-pathspecs", action == "stageTracked" ? "add" : "reset", "--"};
                    arguments.append(paths);
                }
        else if (action == "unstageFile" || action == "unstageAll") {
            // reset also supports an unborn branch, unlike restore --staged.
            arguments = {"--literal-pathspecs", "reset", "--", action == "unstageAll" ? "." : message};
        } else if (action == "trashUntracked") {
            runGit(m_root, {"ls-files", "--others", "--exclude-standard", "-z"},
                [this, complete](const QByteArray &out, const QString &error) {
                    if (!error.isEmpty()) { complete({}, error); return; }
                    QStringList paths;
                    for (const auto &name : out.split('\0')) {
                        if (name.isEmpty()) continue;
                        const auto path = confined(QFile::decodeName(name));
                        if (path.isEmpty()) { complete({}, "Unsafe untracked path; nothing was trashed"); return; }
                        paths.append(path);
                    }
                    for (const auto &path : paths) {
                        if (m_writeValidator) {
                            const auto rejection = m_writeValidator();
                            if (!rejection.isEmpty()) { complete({}, rejection); return; }
                        }
                        const auto relative = QDir(m_root).relativeFilePath(path);
                        if (confined(relative) != path || !QFile::moveToTrash(path)) {
                            complete({}, "Cannot safely trash " + relative + "; earlier files may have been trashed"); return;
                        }
                    }
                    complete({}, {});
                }, true);
            return;
        }
        if (action == "pull") arguments += QStringList{"--ff-only", "--no-rebase", "--no-autostash"};
        runGit(m_root, arguments, complete, true);
    }
}

void GitWorkspace::openSystemTerminal() {
    const auto reject = [this](const QString &message) {
        m_gitMessage = message;
        emit gitStateChanged();
    };
    if (m_root.isEmpty() || m_resolving) { reject("No repository is ready"); return; }
    if (m_writeValidator) {
        const auto error = m_writeValidator();
        if (!error.isEmpty()) { reject(error); return; }
    }
    const auto launch = [this](const QString &name, const QStringList &arguments) {
        const auto executable = QStandardPaths::findExecutable(name);
        return !executable.isEmpty() && QProcess::startDetached(executable, arguments, m_root);
    };
#ifdef Q_OS_WIN
    if (launch("wt.exe", {"-d", m_root}) || launch("cmd.exe", {"/K"})) return;
#elif defined(Q_OS_LINUX)
    if (launch("x-terminal-emulator", {})
        || launch("konsole", {"--workdir", m_root})
        || launch("gnome-terminal", {"--working-directory", m_root})
        || launch("kitty", {"--directory", m_root})
        || launch("alacritty", {"--working-directory", m_root})
        || launch("xfce4-terminal", {"--working-directory", m_root})
        || launch("foot", {"--working-directory", m_root})) return;
#else
    Q_UNUSED(launch);
#endif
    reject("Cannot launch a supported system terminal in " + m_root);
}

void GitWorkspace::updateGitStatus(const QByteArray &status) {
    m_gitStatus.clear();
    const auto records = status.split('\0');
    const auto mark = [this](QString path, const QString &status) {
        // Modified wins for directories containing a mixture of new and changed files.
        while (!path.isEmpty()) {
            if (m_gitStatus.value(path) != "modified") m_gitStatus.insert(path, status);
            const int slash = path.lastIndexOf('/');
            if (slash < 0) break;
            path.truncate(slash);
        }
    };
    for (qsizetype i = 0; i < records.size(); ++i) {
        const QByteArray &record = records.at(i);
        if (record.size() < 4 || record.at(2) != ' ') continue;
        const QByteArray xy = record.left(2);
        const QString status = xy == "??" || xy == "A " || xy == "AM" ? "new" : "modified";
        mark(QFile::decodeName(record.mid(3)), status);
        // In -z mode rename/copy records contain destination first, then source.
        if (xy.contains('R') || xy.contains('C')) {
            if (++i < records.size() && xy.contains('R'))
                mark(QFile::decodeName(records.at(i)), "modified");
        }
    }
}

void GitWorkspace::updateWatches() {
    QSet<QString> desired;
    bool limited = false;
    const auto add = [&](const QString &path) {
        if (path.isEmpty() || desired.contains(path)) return;
        if (desired.size() >= m_maximumWatches) { limited = true; return; }
        if (QFileInfo::exists(path)) desired.insert(path);
    };
    // Metadata and directory replacement notifications have priority over files.
    add(m_root);
    const QString gitLink = QDir(m_root).filePath(".git");
    if (QFileInfo(gitLink).isFile()) add(gitLink);
    for (const QString &directory : m_gitDirectories) {
        add(directory);
        for (const QString &name : {"HEAD", "index", "packed-refs", "config", "commondir", "info", "info/exclude"})
            add(QDir(directory).filePath(name));
        const QString refs = QDir(directory).filePath("refs");
        add(refs);
        QDirIterator it(refs, QDir::Dirs | QDir::NoDotAndDotDot | QDir::Hidden, QDirIterator::Subdirectories);
        while (it.hasNext() && desired.size() < m_maximumWatches) {
            it.next();
            if (!it.fileInfo().isSymLink()) add(it.filePath());
        }
        if (it.hasNext()) limited = true;
    }
    QSet<QString> parents;
    for (const QString &path : m_trackedFiles) {
        QString parent = path;
        while (parent.contains('/')) {
            parent.truncate(parent.lastIndexOf('/'));
            if (parents.contains(parent)) break;
            parents.insert(parent);
            add(confined(parent, true));
            if (desired.size() >= m_maximumWatches) { limited = true; break; }
        }
        if (limited) break;
    }
    for (const QVariant &row : m_tree) {
        const auto item = row.toMap();
        if (item.value("directory").toBool() && item.value("expanded").toBool())
            add(QDir(m_root).filePath(item.value("path").toString()));
    }
    for (const QString &path : m_trackedFiles) {
        if (desired.size() >= m_maximumWatches) { limited = true; break; }
        add(confined(path));
    }
    for (const QVariant &row : m_tree) {
        if (desired.size() >= m_maximumWatches) { limited = true; break; }
        const auto item = row.toMap();
        if (!item.value("directory").toBool()) add(confined(item.value("path").toString()));
    }
    QSet<QString> current;
    for (const QString &path : m_watcher.files() + m_watcher.directories()) current.insert(path);
    const auto removed = current - desired;
    const auto added = desired - current;
    if (!removed.isEmpty()) m_watcher.removePaths(removed.values());
    const auto failed = added.isEmpty() ? QStringList() : m_watcher.addPaths(added.values());
    m_watchError = limited || !failed.isEmpty()
        ? QString("File watch coverage limited (%1 failed); status reconciles every 2 seconds").arg(failed.size())
        : QString();
}

void GitWorkspace::rebuildTree() {
    const auto previousTree = m_tree;
    const auto previousError = error();
    m_tree.clear();
    if (!m_root.isEmpty()) { appendTree({}, 0); updateWatches(); }
    if (m_tree != previousTree) emit treeChanged();
    if (error() != previousError) emit changed();
}
void GitWorkspace::finishRefresh() {
    m_refreshActive = false;
    rebuildTree();
    // Periodic reconciliation must not perpetually queue another slow history read.
    // Actions already refresh history when they finish.
    if (!m_gitBusy) refreshGit();
    if (m_refreshAgain) { m_refreshAgain = false; m_refreshDebounce.start(); }
}
void GitWorkspace::refreshTree() {
    m_refreshDebounce.stop();
    if (m_resolving || m_refreshActive || m_gitActionActive) { m_refreshAgain = true; return; }
    if (m_root.isEmpty()) {
        if (!m_repository.trimmed().isEmpty()) setRepositoryPath(m_repository);
        else rebuildTree();
        return;
    }
    struct Snapshot { QHash<QString, QString> status; QStringList tracked; QString error; bool filters = false; };
    m_refreshActive = true;
    const auto generation = m_gitGeneration;
    auto *watcher = new QFutureWatcher<Snapshot>(this);
    connect(watcher, &QFutureWatcher<Snapshot>::finished, this, [this, watcher, generation, revision = m_actionRevision] {
        const auto snapshot = watcher->result();
        watcher->deleteLater();
        if (generation != m_gitGeneration) return;
        const auto previousGitError = m_gitError;
        if (!snapshot.error.isEmpty()) {
            m_gitError = snapshot.error; finishRefresh();
        } else {
            refreshChanges(snapshot.tracked, revision);
        }
        if (m_gitError != previousGitError) emit changed();
    });
    const auto root = QFile::encodeName(m_root);
    watcher->setFuture(QtConcurrent::run([root] {
        Snapshot result;
        git_libgit2_init();
        git_repository *raw = nullptr;
        if (git_repository_open(&raw, root.constData()) == 0) {
            {
                std::unique_ptr<git_repository, decltype(&git_repository_free)> repo(raw, git_repository_free);
                git_config *config = nullptr;
                bool filters = false;
                if (git_repository_config_snapshot(&config, raw) == 0) {
                    git_config_foreach(config, [](const git_config_entry *entry, void *payload) {
                        if (QByteArray(entry->name).startsWith("filter.")) *static_cast<bool *>(payload) = true;
                        return 0;
                    }, &filters);
                    git_config_free(config);
                } else result.error = "Git failed: cannot read repository configuration";
                result.filters = filters;
                git_status_options options = GIT_STATUS_OPTIONS_INIT;
                options.flags = GIT_STATUS_OPT_INCLUDE_UNTRACKED | GIT_STATUS_OPT_RECURSE_UNTRACKED_DIRS
                    | GIT_STATUS_OPT_RENAMES_HEAD_TO_INDEX | GIT_STATUS_OPT_RENAMES_INDEX_TO_WORKDIR;
                git_status_list *list = nullptr;
                git_index *index = nullptr;
                if (result.error.isEmpty() && git_repository_index(&index, raw) == 0
                    && (filters || git_status_list_new(&list, raw, &options) == 0)) {
                    qsizetype bytes = 0;
                    const auto mark = [&](const char *name, const QString &status) {
                        QString path = QFile::decodeName(name);
                        bytes += path.size();
                        while (!path.isEmpty()) {
                            if (result.status.value(path) != "modified") result.status.insert(path, status);
                            const int slash = path.lastIndexOf('/');
                            if (slash < 0) break;
                            path.truncate(slash);
                        }
                    };
                    for (size_t i = 0; !filters && i < git_status_list_entrycount(list); ++i) {
                        const auto *entry = git_status_byindex(list, i);
                        const auto flags = entry->status;
                        if (flags == GIT_STATUS_CURRENT || flags == GIT_STATUS_IGNORED) continue;
                        const auto *delta = entry->index_to_workdir ? entry->index_to_workdir : entry->head_to_index;
                        if (!delta || !delta->new_file.path) { result.error = "Git failed: invalid status path"; break; }
                        const bool added = flags == GIT_STATUS_WT_NEW || flags == GIT_STATUS_INDEX_NEW
                            || flags == (GIT_STATUS_INDEX_NEW | GIT_STATUS_WT_MODIFIED);
                        mark(delta->new_file.path, added ? "new" : "modified");
                        for (const auto *rename : {entry->head_to_index, entry->index_to_workdir})
                            if (rename && rename->status == GIT_DELTA_RENAMED && rename->old_file.path)
                                mark(rename->old_file.path, "modified");
                        if (bytes > 16 * 1024 * 1024) { result.error = "Git output exceeds the safety limit; previous status retained"; break; }
                    }
                    for (size_t i = 0; result.error.isEmpty() && i < git_index_entrycount(index); ++i) {
                        const auto *path = git_index_get_byindex(index, i)->path;
                        bytes += qstrlen(path);
                        result.tracked.append(QFile::decodeName(path));
                        if (bytes > 16 * 1024 * 1024) result.error = "Git output exceeds the safety limit; previous status retained";
                    }
                }
                if (result.error.isEmpty() && (!index || (!filters && !list))) {
                    const auto *error = git_error_last();
                    result.error = "Git failed: " + QString::fromUtf8(error ? error->message : "cannot read status/index");
                }
                git_index_free(index); git_status_list_free(list);
            }
        }
        if (!raw) result.error = "Git failed: cannot open repository";
        git_libgit2_shutdown();
        return result;
    }));

}

void GitWorkspace::toggleDirectory(const QString &path) {
    if (confined(path, true).isEmpty()) { fail("Directory is unavailable or outside the workspace"); return; }
    if (m_expanded.contains(path)) m_expanded.remove(path); else m_expanded.insert(path);
    rebuildTree();
}

void GitWorkspace::updateLargeFileMode() {
    // QQuickTextEdit is not virtualized. A very large document makes full syntax
    // highlighting expensive, while one huge unwrapped line can create scene-graph
    // geometry millions of pixels wide and crash the renderer.
    constexpr qsizetype largeDocumentCharacters = 256 * 1024;
    constexpr qsizetype longLineCharacters = 16 * 1024;
    m_largeFileMode = m_text.size() > largeDocumentCharacters;
    if (m_largeFileMode) return;
    qsizetype lineLength = 0;
    for (const QChar character : m_text) {
        if (character == '\n') lineLength = 0;
        else if (++lineLength > longLineCharacters) {
            m_largeFileMode = true;
            return;
        }
    }
}

void GitWorkspace::setText(const QString &text) {
    const QString normalized = editorText(text);
    if (m_loadingDocument || m_file.isEmpty() || m_text == normalized) return;
    m_text = normalized;
    updateLargeFileMode();
    emit documentChanged();
    emit changed();
}

bool GitWorkspace::editDocument(int revision, const QString &text) {
    // A previous editor buffer must never be applied to a newly selected file,
    // even during synchronous QML binding/highlighter notifications.
    if (m_loadingDocument || m_file.isEmpty() || revision != m_documentRevision) return false;
    setText(text);
    return true;
}

bool GitWorkspace::loadFile(const QString &relativePath) {
    const QString path = confined(relativePath);
    if (path.isEmpty()) { fail("File is unavailable, a symlink, or outside the workspace"); return false; }
    QFile file(path);
    if (!file.open(QIODevice::ReadOnly)) { fail(file.errorString()); return false; }
    if (file.size() > MaximumTextBytes) { fail("Text files are limited to 2 MiB"); return false; }
    const QByteArray bytes = file.read(MaximumTextBytes + 1);
    if (file.error() != QFileDevice::NoError || bytes.size() > MaximumTextBytes) {
        fail("Could not read file within the 2 MiB limit"); return false;
    }
    auto reject = [&](const QString &encoding, const QString &reason) {
        fail(QString("Cannot edit '%1' (%2): %3").arg(relativePath, encoding, reason));
        return false;
    };
    // Check UTF-32 first: its LE BOM starts with the UTF-16 LE BOM.
    if (bytes.startsWith(QByteArray::fromHex("fffe0000"))
        || bytes.startsWith(QByteArray::fromHex("0000feff")))
        return reject("UTF-32 BOM", "unsupported encoding; use UTF-8 or BOM-marked UTF-16");
    auto encoding = QStringConverter::Utf8;
    QString encodingName = "UTF-8 (no BOM)";
    qsizetype bomSize = 0;
    if (bytes.startsWith(QByteArray::fromHex("efbbbf"))) {
        bomSize = 3; encodingName = "UTF-8 BOM";
    } else if (bytes.startsWith(QByteArray::fromHex("fffe"))) {
        bomSize = 2; encoding = QStringConverter::Utf16LE; encodingName = "UTF-16 LE BOM";
    } else if (bytes.startsWith(QByteArray::fromHex("feff"))) {
        bomSize = 2; encoding = QStringConverter::Utf16BE; encodingName = "UTF-16 BE BOM";
    }
    const QByteArray payload = bytes.mid(bomSize);
    if (encoding != QStringConverter::Utf8 && payload.size() % 2 != 0)
        return reject(encodingName, "truncated UTF-16 code unit (odd payload byte count)");
    if (encoding == QStringConverter::Utf8 && payload.contains('\0'))
        return reject(encodingName, "NUL byte found; binary or BOM-less UTF-16 is not supported");
    // Strip exactly one transport BOM, retaining any subsequent U+FEFF as content.
    QStringDecoder decoder(encoding, QStringConverter::Flag::ConvertInitialBom
                                     | QStringConverter::Flag::Stateless);
    const QString text = decoder.decode(payload);
    QStringEncoder encoder(encoding, QStringConverter::Flag::Stateless);
    const QByteArray roundTrip = encoder.encode(text);
    if (!validUnicode(text) || decoder.hasError() || encoder.hasError() || roundTrip != payload)
        return reject(encodingName, "malformed or truncated Unicode sequence; lossless decoding failed");
    if (text.contains(QChar(0)))
        return reject(encodingName, "decoded NUL character found; binary content cannot be edited");
    QSet<QString> endings;
    for (qsizetype i = 0; i < text.size(); ++i) {
        if (text.at(i) == '\r') {
            if (i + 1 < text.size() && text.at(i + 1) == '\n') {
                endings.insert("\r\n"); ++i;
            } else endings.insert("\r");
        } else if (text.at(i) == '\n') endings.insert("\n");
    }
    m_loadingDocument = true;
    emit documentAboutToChange();
    ++m_documentRevision;
    m_hasBom = bomSize != 0;
    m_encoding = encoding;
    m_mixedEndings = endings.size() > 1;
    m_lineEnding = endings.size() == 1 ? *endings.cbegin() : QString("\n");
    m_file = relativePath; m_text = editorText(text); m_saved = m_text; m_diskBytes = bytes;
    updateLargeFileMode();
    m_error.clear();
    emit documentChanged();
    emit changed();
    m_loadingDocument = false;
    return true;
}

void GitWorkspace::openFile(const QString &path) {
    if (m_pending || path == m_file) return;
    if (dirty()) {
        m_pending = true; m_pendingClose = false; m_pendingFile = path;
        emit unsavedPrompt();
    } else loadFile(path);
}

bool GitWorkspace::save() {
    if (m_writeValidator) { const auto error = m_writeValidator(); if (!error.isEmpty()) { fail(error); return false; } }
    QLockFile uploadLock(m_root + "/.ddetector-upload.lock");
    if (m_root.isEmpty() || !uploadLock.tryLock(0)) {
        fail("Cannot save while an uploader is writing this repository"); return false;
    }
    const QString path = confined(m_file);
    if (path.isEmpty()) { fail("Cannot save: file is no longer a confined regular file"); return false; }
    if (dirty() && m_mixedEndings) {
        fail("Save refused: mixed line endings cannot be safely preserved. Discard changes or normalize the file externally");
        return false;
    }
    QString diskText = m_text;
    diskText.replace("\n", m_lineEnding);
    const QString encodingName = QString::fromLatin1(QStringConverter::nameForEncoding(m_encoding));
    auto reject = [&](const QString &reason) {
        fail(QString("Cannot save '%1' (%2): %3").arg(m_file, encodingName, reason));
        return false;
    };
    if (!validUnicode(diskText))
        return reject("malformed Unicode text; unpaired surrogate found");
    if (diskText.contains(QChar(0)))
        return reject("decoded NUL character found; binary content cannot be saved");
    QStringEncoder encoder(m_encoding, QStringConverter::Flag::Stateless);
    const QByteArray payload = encoder.encode(diskText);
    QStringDecoder decoder(m_encoding, QStringConverter::Flag::ConvertInitialBom
                                      | QStringConverter::Flag::Stateless);
    const QString roundTrip = decoder.decode(payload);
    if (encoder.hasError() || decoder.hasError() || roundTrip != diskText)
        return reject("malformed Unicode text; lossless encoding failed");
    QByteArray bom;
    if (m_hasBom) {
        bom = QByteArray::fromHex(m_encoding == QStringConverter::Utf16LE ? "fffe"
                                 : m_encoding == QStringConverter::Utf16BE ? "feff" : "efbbbf");
    }
    const QByteArray bytes = bom + payload;
    if (bytes.size() > MaximumTextBytes)
        return reject("encoded text exceeds the 2 MiB limit");
    QFile current(path);
    if (!current.open(QIODevice::ReadOnly) || current.size() > MaximumTextBytes
        || current.read(MaximumTextBytes + 1) != m_diskBytes
        || current.error() != QFileDevice::NoError) {
        fail("File changed on disk or cannot be read; save refused to avoid overwriting external edits");
        return false;
    }
    current.close();
    if (!dirty()) { m_error.clear(); emit changed(); return true; }
    // Recheck after reading; QSaveFile must never fall back to truncating the original.
    if (confined(m_file) != path) { fail("File path changed during save"); return false; }
    QSaveFile target(path);
    target.setDirectWriteFallback(false);
    if (!target.open(QIODevice::WriteOnly) || target.write(bytes) != bytes.size() || !target.commit()) {
        fail("Atomic save failed: " + target.errorString()); return false;
    }
    m_saved = m_text; m_diskBytes = bytes; m_error.clear(); emit changed(); return true;
}

void GitWorkspace::requestClose() {
    if (m_pending) return;
    if (!dirty()) { emit closeApproved(); return; }
    m_pending = true; m_pendingClose = true; m_pendingFile.clear(); emit unsavedPrompt();
}

void GitWorkspace::resolveUnsaved(const QString &decision) {
    if (!m_pending) return;
    if (decision == "save") { if (!save()) return; }
    else if (decision != "discard" && decision != "cancel") return;
    const bool close = m_pendingClose;
    const QString next = m_pendingFile;
    m_pending = false; m_pendingClose = false; m_pendingFile.clear();
    if (decision == "cancel") return;
    if (decision == "discard") {
        m_text = m_saved;
        emit documentChanged();
        emit changed();
    }
    if (close) emit closeApproved(); else loadFile(next);
}

void GitWorkspace::appendOutput(const QString &text) {
    m_output += text;
    constexpr int limit = 256 * 1024;
    if (m_output.size() > limit) m_output.remove(0, m_output.size() - limit);
    emit terminalChanged();
}

void GitWorkspace::startTerminal(bool automatic) {
    if (terminalRunning()) return;
    if (m_writeValidator) {
        const auto error = m_writeValidator();
        if (!error.isEmpty()) {
            // Opening a tab is not an explicit request to modify the repository.
            if (!automatic) fail(error);
            return;
        }
    }
    if (m_root.isEmpty() || !QFileInfo(m_root).isDir()) { fail("Select a valid repository first"); return; }
    if (!m_terminal.start(m_root)) fail(m_terminal.error());
}

bool GitWorkspace::sendTerminalInput(const QString &text) {
    if (!m_terminal.sendText(text)) { fail(m_terminal.error()); return false; }
    return true;
}

void GitWorkspace::stopTerminal() {
    m_terminal.stop();
}

void GitWorkspace::closeTerminalInput() {
    m_terminal.sendEof();
}

void GitWorkspace::clearTerminal() {
    m_terminal.clear(); m_output.clear(); emit terminalChanged();
}

#ifdef GIT_WORKSPACE_TEST
#include <QCoreApplication>
#include <QTemporaryDir>
#include <QDateTime>
#include <QStandardPaths>
#include <QScopeGuard>
#include <QtTest>
#ifdef GIT_WORKSPACE_QML_TEST
#include "terminal.h"
#include "syntaxhighlighter.h"
#include <qqml.h>
#include <QQmlComponent>
#include <QQmlEngine>
#include <QQmlExpression>
#include <QQmlContext>
#include <QQuickItem>
#include <QQuickStyle>
#include <QQuickWindow>
#endif

// Standalone test target: AUTOMOC ON, sources workspace.cpp/workspace.h and
// terminalsession.cpp/.h, define GIT_WORKSPACE_TEST, link Qt6::Widgets,
// Qt6::Test and qtermwidget6. QApplication is required by the emulator.
// Optionally define GIT_WORKSPACE_QML_TEST, add terminal.cpp/.h and link
// Qt6::Quick/QuickControls2 to exercise GitWorkspace.qml and its terminal Loader
// with and without an injected backend (offscreen).
class WorkspaceTest : public QObject {
    Q_OBJECT
    static QByteArray utf16Bytes(const QString &text, bool littleEndian) {
        QByteArray bytes = QByteArray::fromHex(littleEndian ? "fffe" : "feff");
        for (QChar ch : text) {
            const char low = char(ch.unicode() & 0xff), high = char(ch.unicode() >> 8);
            bytes.append(littleEndian ? low : high);
            bytes.append(littleEndian ? high : low);
        }
        return bytes;
    }
private slots:
    void initTestCase() {
#ifdef GIT_WORKSPACE_QML_TEST
        QQuickStyle::setStyle("Basic");
        qmlRegisterType<SyntaxHighlighter>("DDetector.Syntax", 1, 0, "SyntaxHighlighter");
        qmlRegisterType<EmbeddedTerminal>("DDetector.Terminal", 1, 0, "EmbeddedTerminal");
        qmlRegisterUncreatableType<TerminalSession>(
            "DDetector.Terminal", 1, 0, "TerminalSession", "Owned by GitWorkspace");
#endif
    }
    void outgoingCommitCount() {
        QTemporaryDir temp, remote;
        QVERIFY(temp.isValid());
        QVERIFY(remote.isValid());
        const auto git = [&](const QStringList &args) {
            QProcess process;
            process.setWorkingDirectory(temp.path());
            process.start("git", args);
            return process.waitForFinished(5000) && process.exitCode() == 0;
        };
        QVERIFY(git({"init", "--bare", "-q", remote.path()}));
        QVERIFY(git({"init", "-q"}));
        QVERIFY(git({"config", "user.name", "Test"}));
        QVERIFY(git({"config", "user.email", "test@example.invalid"}));
        QVERIFY(git({"commit", "--allow-empty", "-qm", "initial"}));
        GitWorkspace workspace;
        workspace.setRepositoryPath(temp.path());
        QTRY_COMPARE(workspace.commits().size(), 1);
        QCOMPARE(workspace.aheadCount(), 0);
        QVERIFY(git({"remote", "add", "origin", remote.path()}));
        QVERIFY(git({"push", "-qu", "origin", "HEAD"}));
        QVERIFY(git({"commit", "--allow-empty", "-qm", "outgoing"}));
        workspace.refreshGit();
        QTRY_COMPARE(workspace.aheadCount(), 1);
        workspace.gitAction("push");
        QTRY_COMPARE(workspace.aheadCount(), 0);
        QTRY_VERIFY(!workspace.gitBusy());
        QVERIFY(git({"branch", "--unset-upstream"}));
        QVERIFY(git({"commit", "--allow-empty", "-qm", "no upstream"}));
        workspace.refreshGit();
        QTRY_COMPARE(workspace.commits().size(), 3);
        QCOMPARE(workspace.aheadCount(), 0);
    }

    void repositoryDiscoveryNotifiesReadyState() {
        QTemporaryDir temp;
        QVERIFY(temp.isValid());
        QProcess git;
        git.start("git", {"init", "-q", temp.path()});
        QVERIFY(git.waitForFinished(5000));
        QCOMPARE(git.exitCode(), 0);
        GitWorkspace workspace;
        bool readyNotified = false;
        connect(&workspace, &GitWorkspace::changed, &workspace, [&] {
            if (!workspace.resolving() && workspace.rootPath() == temp.path())
                readyNotified = true;
        });
        workspace.setRepositoryPath(temp.path());
        QTRY_VERIFY(!workspace.resolving());
        QCOMPARE(workspace.rootPath(), temp.path());
        QVERIFY(readyNotified);
    }
    void expandedGitChanges() {
        QTemporaryDir temp;
        QVERIFY(temp.isValid());
        const auto git = [&](const QStringList &args) {
            QProcess process;
            process.setWorkingDirectory(temp.path());
            process.start("git", args);
            return process.waitForFinished(5000) && process.exitCode() == 0;
        };
        QVERIFY(git({"init", "-q"}));
        QVERIFY(git({"config", "user.name", "Test"}));
        QVERIFY(git({"config", "user.email", "test@example.invalid"}));
        QFile file(temp.filePath("literal[1].txt"));
        QVERIFY(file.open(QIODevice::WriteOnly));
        file.write("one\n"); file.close();
        GitWorkspace workspace;
        workspace.setRepositoryPath(temp.path());
        const auto change = [&](const QString &path) {
            for (const auto &row : workspace.changedFiles()) {
                const auto item = row.toMap();
                if (item.value("path").toString() == path) return item;
            }
            return QVariantMap{};
        };
        QTRY_COMPARE(workspace.changedFiles().size(), 1);
        QVERIFY(change("literal[1].txt").value("untracked").toBool());
        workspace.viewDiff("literal[1].txt");
        QTRY_VERIFY(!workspace.gitBusy());
        QVERIFY(workspace.diffText().contains("one\n"));
        workspace.stageFile("literal[1].txt", true);
        QTRY_VERIFY(!workspace.gitBusy() && !workspace.m_refreshActive);
        QTRY_VERIFY(change("literal[1].txt").value("staged").toBool());
        QCOMPARE(change("literal[1].txt").value("additions").toInt(), 1);
        workspace.gitAction("commitStaged", "initial");
        QTRY_VERIFY(!workspace.gitBusy() && !workspace.m_refreshActive);
        QTRY_VERIFY(workspace.changedFiles().isEmpty());
        QVERIFY(file.open(QIODevice::Append));
        file.write("two\n"); file.close();
        workspace.refreshTree();
        QTRY_COMPARE(workspace.changedFiles().size(), 1);
        QTRY_COMPARE(change("literal[1].txt").value("additions").toInt(), 1);
        QSignalSpy diffs(&workspace, &GitWorkspace::diffChanged);
        // A diff takes priority over history reads without sharing their process slot.
        workspace.refreshGit();
        workspace.viewDiff("literal[1].txt");
        QTRY_VERIFY(!workspace.gitBusy());
        QCOMPARE(diffs.size(), 1);
        QVERIFY(workspace.diffText().contains("+two"));
        QVERIFY(git({"add", "--", "literal[1].txt"}));
        QVERIFY(git({"commit", "-qm", "second"}));
        workspace.gitAction("undoCommit");
        QTRY_VERIFY(!workspace.gitBusy() && !workspace.m_refreshActive);
        QVERIFY2(workspace.gitMessage().startsWith("undoCommit succeeded"), qPrintable(workspace.gitMessage()));
        QTRY_COMPARE(workspace.changedFiles().size(), 1);
        // A stale refresh can finish before its debounced replacement starts.
        // Wait for the requested file state, not just an idle process lane.
        QTRY_VERIFY(change("literal[1].txt").value("staged").toBool());
        workspace.stageAll(false);
        QTRY_VERIFY(!workspace.gitBusy() && !workspace.m_refreshActive);
        QTRY_COMPARE(change("literal[1].txt").value("status").toString(), QString(" M"));
        workspace.setWriteValidator([] { return QString("blocked"); });
        workspace.stageAll(true);
        QCOMPARE(workspace.gitMessage(), QString("blocked"));
        QVERIFY(!change("literal[1].txt").value("staged").toBool());
        QVERIFY(!workspace.validGitPath("../outside"));
        QVERIFY(!workspace.validGitPath(".git/config"));
        const auto writeUntracked = [&](const QString &name, const QByteArray &bytes) {
            QFile extra(temp.filePath(name));
            return extra.open(QIODevice::WriteOnly) && extra.write(bytes) == bytes.size();
        };
        QVERIFY(writeUntracked("aaa.txt", "plain text\n"));
        QVERIFY(writeUntracked("binary.bin", QByteArray("a\0b", 3)));
        QVERIFY(writeUntracked("huge.txt", QByteArray(GitWorkspace::MaximumTextBytes + 1, 'x')));
        workspace.refreshTree();
        QTRY_COMPARE(workspace.changedFiles().size(), 4);
        bool sawUntracked = false;
        for (const auto &row : workspace.changedFiles()) {
            const bool untracked = row.toMap().value("untracked").toBool();
            QVERIFY(!sawUntracked || untracked);
            sawUntracked |= untracked;
        }
        workspace.viewDiff("aaa.txt");
        QTRY_VERIFY(!workspace.gitBusy());
        QVERIFY(workspace.diffText().contains("plain text"));
        workspace.viewDiff("binary.bin");
        QTRY_VERIFY(!workspace.gitBusy());
        QVERIFY(workspace.diffText().contains("Binary"));
        workspace.viewDiff("huge.txt");
        QTRY_VERIFY(!workspace.gitBusy());
        QVERIFY(workspace.diffText().contains("too large"));
#ifdef Q_OS_UNIX
        QVERIFY(QFile::link(temp.filePath("aaa.txt"), temp.filePath("link.txt")));
        workspace.viewDiff("link.txt");
        QVERIFY(workspace.gitMessage().contains("Invalid"));
#endif
    }
    void unchangedPollingDoesNotResetModels() {
        QTemporaryDir temp;
        QVERIFY(temp.isValid());
        const auto git = [&](const QStringList &arguments) {
            QProcess process;
            process.setWorkingDirectory(temp.path());
            process.start("git", arguments);
            return process.waitForFinished(5000) && process.exitCode() == 0;
        };
        QVERIFY(git({"init", "-q"}));
        QFile file(temp.filePath("tracked.txt"));
        QVERIFY(file.open(QIODevice::WriteOnly));
        file.write("original\n"); file.close();
        QVERIFY(git({"add", "."}));
        QVERIFY(git({"-c", "user.name=Test", "-c", "user.email=test@example.invalid",
                     "commit", "-qm", "initial"}));
        GitWorkspace workspace;
        workspace.setRepositoryPath(temp.path());
        QTRY_VERIFY(!workspace.resolving() && !workspace.m_refreshActive && !workspace.m_gitBusy);
        QCOMPARE(workspace.commits().size(), 1);
        const auto tree = workspace.tree();
        const auto commits = workspace.commits();
        QSignalSpy treeChanges(&workspace, &GitWorkspace::treeChanged);
        QSignalSpy historyChanges(&workspace, &GitWorkspace::gitChanged);
        QSignalSpy stateChanges(&workspace, &GitWorkspace::gitStateChanged);
        QSignalSpy changes(&workspace, &GitWorkspace::changed);
        // Exercise the actual reconciliation timer, not just a direct refresh.
        workspace.m_refreshDebounce.stop();
        workspace.m_reconcile.start(100);
        QTest::qWait(450);
        workspace.m_reconcile.stop();
        QTRY_VERIFY(!workspace.m_refreshActive && !workspace.m_gitBusy);
                QVERIFY(!workspace.gitBusy());
                QCOMPARE(stateChanges.size(), 0);
        QCOMPARE(workspace.tree(), tree);
        QCOMPARE(workspace.commits(), commits);
        QCOMPARE(treeChanges.size(), 0);
        QCOMPARE(historyChanges.size(), 0);
        QCOMPARE(changes.size(), 0);

        // Real external changes must still reach both models.
        QVERIFY(file.open(QIODevice::WriteOnly | QIODevice::Append));
        file.write("external\n"); file.close();
        workspace.refreshTree();
        QTRY_VERIFY(!treeChanges.isEmpty());
        QVERIFY(git({"-c", "user.name=Test", "-c", "user.email=test@example.invalid",
                     "commit", "-qam", "external"}));
        workspace.refreshTree();
        QTRY_COMPARE(workspace.commits().size(), 2);
        QVERIFY(!historyChanges.isEmpty());

        QTRY_VERIFY(!workspace.m_gitBusy);
        workspace.refreshGit();
        QVERIFY(workspace.m_gitBusy);
        QVERIFY(!workspace.gitBusy());
        // A click during a background read starts an action rather than rejecting it.
        workspace.gitAction("commit", "action during polling");
        QVERIFY(workspace.gitBusy());
        QTRY_VERIFY(!workspace.m_gitActionActive);
        QVERIFY(workspace.gitMessage() != "Wait for the current Git operation");
        QTRY_VERIFY(!workspace.m_gitBusy);
    }
    void statusAndAutomaticRefresh() {
        QTemporaryDir temp;
        QVERIFY(temp.isValid());
        const auto git = [&](const QStringList &arguments) {
            QProcess process;
            process.setWorkingDirectory(temp.path());
            process.start("git", arguments);
            return process.waitForFinished(5000) && process.exitCode() == 0;
        };
        const auto write = [&](const QString &name, const QByteArray &bytes) {
            QFile file(QDir(temp.path()).filePath(name));
            return file.open(QIODevice::WriteOnly) && file.write(bytes) == bytes.size();
        };
        QVERIFY(git({"init", "-q"}));
        QVERIFY(QDir(temp.path()).mkdir("nested"));
        QVERIFY(write("tracked.txt", "original\n"));
        QVERIFY(write("nested/deleted.txt", "delete\n"));
        QVERIFY(write("rename.txt", "rename\n"));
        QVERIFY(git({"add", "."}));
        QVERIFY(git({"-c", "user.name=Test", "-c", "user.email=test@example.invalid",
                     "commit", "-qm", "initial"}));
        GitWorkspace workspace;
        workspace.setRepositoryPath(temp.path());
        QTRY_VERIFY(!workspace.resolving() && !workspace.m_refreshActive);
        const auto metadata = [&](const QString &path, const QString &key) {
            for (const QVariant &row : workspace.tree()) {
                const auto item = row.toMap();
                if (item.value("path").toString() == path) return item.value(key).toString();
            }
            return QString("missing");
        };
        QCOMPARE(metadata("tracked.txt", "status"), QString());
        workspace.openFile("tracked.txt");
        const int revision = workspace.documentRevision();
        workspace.setText("unsaved editor\n");
        QSignalSpy documentChanges(&workspace, &GitWorkspace::documentChanged);
        const QString unusual = "nested/space \"quote\"\nfile.txt";
        workspace.toggleDirectory("nested");
        QVERIFY(write(unusual, "new\n"));
        QTRY_COMPARE_WITH_TIMEOUT(metadata(unusual, "status"), QString("new"), 3000);
        QCOMPARE(metadata(unusual, "statusColor"), QString("#00a682"));
        QCOMPARE(metadata("nested", "status"), QString("new"));
        QVERIFY(git({"add", "--", unusual}));
        QTest::qWait(150);
        QCOMPARE(metadata(unusual, "status"), QString("new"));
        QVERIFY(write("tracked.txt", "external\n"));
        QTRY_COMPARE_WITH_TIMEOUT(metadata("tracked.txt", "status"), QString("modified"), 3000);
        QCOMPARE(metadata("tracked.txt", "statusColor"), QString("#ab910d"));
        QVERIFY(QFile::remove(QDir(temp.path()).filePath("nested/deleted.txt")));
        QTRY_COMPARE_WITH_TIMEOUT(metadata("nested", "status"), QString("modified"), 3000);
        QVERIFY(git({"mv", "--", "rename.txt", "renamed \"file\".txt"}));
        QTRY_COMPARE_WITH_TIMEOUT(metadata("renamed \"file\".txt", "status"), QString("modified"), 3000);
        QCOMPARE(workspace.filePath(), QString("tracked.txt"));
        QCOMPARE(workspace.text(), QString("unsaved editor\n"));
        QCOMPARE(workspace.documentRevision(), revision);
        QCOMPARE(documentChanges.count(), 0);
        QVERIFY(workspace.dirty());
        QVERIFY(!workspace.save());
        // Index-only changes (no working-tree edit) must also update the tree.
        QVERIFY(git({"-c", "user.name=Test", "-c", "user.email=test@example.invalid",
                     "commit", "-qm", "staged changes"}));
        QTRY_COMPARE_WITH_TIMEOUT(metadata(unusual, "status"), QString(), 3000);
        QCOMPARE(metadata(unusual, "statusColor"), QString());
        QVERIFY(write("created externally.txt", "new\n"));
        QTRY_COMPARE_WITH_TIMEOUT(metadata("created externally.txt", "status"), QString("new"), 3000);
        QVERIFY(QFile::remove(QDir(temp.path()).filePath("created externally.txt")));
        QTRY_COMPARE_WITH_TIMEOUT(metadata("created externally.txt", "status"), QString("missing"), 3000);
        // A replacement drops a file watch; the containing directory re-arms it.
        QSaveFile replacement(QDir(temp.path()).filePath("tracked.txt"));
        QVERIFY(replacement.open(QIODevice::WriteOnly));
        QVERIFY(replacement.write("original\n") > 0);
        QVERIFY(replacement.commit());
        QTRY_COMPARE_WITH_TIMEOUT(metadata("tracked.txt", "status"), QString(), 3000);
        QVERIFY(write("tracked.txt", "changed again\n"));
        QTRY_COMPARE_WITH_TIMEOUT(metadata("tracked.txt", "status"), QString("modified"), 3000);
    }
    void asyncGitFailureRetainsStatusAndReconcilesLimitedWatches() {
        QTemporaryDir temp; QVERIFY(temp.isValid());
        QProcess git; git.start("git", {"init", "-q", temp.path()});
        QVERIFY(git.waitForFinished()); QCOMPARE(git.exitCode(), 0);
        QFile file(temp.filePath("file")); QVERIFY(file.open(QIODevice::WriteOnly)); file.write("new\n"); file.close();
        GitWorkspace workspace;
        workspace.m_maximumWatches = 1;
        workspace.setRepositoryPath(temp.path());
        QVERIFY(workspace.resolving()); // No blocking validation in the setter.
        QTRY_VERIFY(!workspace.resolving() && !workspace.m_refreshActive);
        QVERIFY(workspace.error().contains("watch coverage limited"));
        const auto status = [&] {
            for (const auto &row : workspace.tree())
                if (row.toMap().value("path").toString() == "file") return row.toMap().value("status").toString();
            return QString("missing");
        };
        QCOMPARE(status(), QString("new"));
        QFile index(temp.filePath(".git/index")); QVERIFY(index.open(QIODevice::WriteOnly)); index.write("corrupt"); index.close();
        workspace.refreshTree();
        QTRY_VERIFY(workspace.m_gitError.contains("Git failed"));
        QCOMPARE(status(), QString("new")); // Failed status is not a clean snapshot.
        QVERIFY(QFile::remove(index.fileName()));
        git.setWorkingDirectory(temp.path()); git.start("git", {"add", "file"});
        QVERIFY(git.waitForFinished()); QCOMPARE(git.exitCode(), 0);
        git.start("git", {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false", "commit", "-qm", "fixture"});
        QVERIFY(git.waitForFinished()); QCOMPARE(git.exitCode(), 0);
        QTRY_COMPARE_WITH_TIMEOUT(status(), QString(), 4000);
        QVERIFY(file.open(QIODevice::WriteOnly)); file.write("modified\n"); file.close();
        // Root directory watches don't observe in-place file content changes.
        QTRY_COMPARE_WITH_TIMEOUT(status(), QString("modified"), 4000);
        QVERIFY(workspace.m_gitError.isEmpty());
    }
    void gitDeadlineAndStaleRepositoryResult() {
#ifndef Q_OS_WIN
        QTemporaryDir a, b, tools; QVERIFY(a.isValid()); QVERIFY(b.isValid()); QVERIFY(tools.isValid());
        const QString actualGit = QStandardPaths::findExecutable("git"); QVERIFY(!actualGit.isEmpty());
        for (const auto &root : {a.path(), b.path()}) {
            QProcess git; git.start(actualGit, {"init", "-q", root});
            QVERIFY(git.waitForFinished()); QCOMPARE(git.exitCode(), 0);
        }
        QFile fake(tools.filePath("git")); QVERIFY(fake.open(QIODevice::WriteOnly));
        fake.write("#!/bin/sh\nexec /bin/sleep 2\n"); fake.close();
        QVERIFY(fake.setPermissions(QFileDevice::ReadOwner | QFileDevice::WriteOwner | QFileDevice::ExeOwner));
        const QByteArray oldPath = qgetenv("PATH");
        const auto restore = qScopeGuard([&] { qputenv("PATH", oldPath); });
        qputenv("PATH", tools.path().toUtf8() + ':' + oldPath);
        GitWorkspace workspace; workspace.m_gitDeadlineMs = 120;
        int heartbeats = 0; QTimer heartbeat; heartbeat.setInterval(10);
        connect(&heartbeat, &QTimer::timeout, this, [&] { ++heartbeats; }); heartbeat.start();
        workspace.setRepositoryPath(a.path());
        QTRY_VERIFY(!workspace.resolving() && !workspace.m_refreshActive);
        workspace.runGit(a.path(), {"status"}, [&workspace](const QByteArray &, const QString &error) { workspace.m_gitError = error; });
        QTRY_VERIFY_WITH_TIMEOUT(!workspace.m_git, 1500);
        QVERIFY(workspace.error().contains("timed out")); QVERIFY(heartbeats > 2);
        workspace.setRepositoryPath(b.path()); // Cancel this slow validation with a third request.
        qputenv("PATH", oldPath);
        workspace.m_gitDeadlineMs = 5000;
        workspace.setRepositoryPath(a.path());
        QTRY_VERIFY(!workspace.resolving()); QCOMPARE(workspace.rootPath(), a.path());
        QTest::qWait(200); QCOMPARE(workspace.rootPath(), a.path());
#endif
    }
    void gitFailedStartCanBeRetriedAndFsmonitorIsDisabled() {
#ifndef Q_OS_WIN
        QTemporaryDir temp, tools; QVERIFY(temp.isValid()); QVERIFY(tools.isValid());
        const QString gitExecutable = QStandardPaths::findExecutable("git"); QVERIFY(!gitExecutable.isEmpty());
        QProcess git; git.start(gitExecutable, {"init", "-q", temp.path()});
        QVERIFY(git.waitForFinished()); QCOMPARE(git.exitCode(), 0);
        const QByteArray oldPath = qgetenv("PATH");
        const auto restore = qScopeGuard([&] { qputenv("PATH", oldPath); });
        qputenv("PATH", tools.path().toUtf8());
        GitWorkspace workspace; workspace.setRepositoryPath(temp.path());
        QTRY_VERIFY(!workspace.resolving());
        QCOMPARE(workspace.rootPath(), temp.path());
        workspace.runGit(temp.path(), {"status"}, [&workspace](const QByteArray &, const QString &error) { workspace.m_gitError = error; });
        QTRY_VERIFY(workspace.error().contains("Cannot start Git"));
        qputenv("PATH", oldPath);
        QFile helper(temp.filePath("fsmonitor-helper")); QVERIFY(helper.open(QIODevice::WriteOnly));
        helper.write("#!/bin/sh\n: > fsmonitor-was-executed\n"); helper.close();
        QVERIFY(helper.setPermissions(QFileDevice::ReadOwner | QFileDevice::WriteOwner | QFileDevice::ExeOwner));
        git.setWorkingDirectory(temp.path()); git.start(gitExecutable, {"config", "core.fsmonitor", helper.fileName()});
        QVERIFY(git.waitForFinished()); QCOMPARE(git.exitCode(), 0);
        workspace.refreshTree();
        QTRY_VERIFY(!workspace.resolving() && !workspace.m_refreshActive);
        QCOMPARE(workspace.rootPath(), temp.path()); QVERIFY(workspace.m_gitError.isEmpty());
        QVERIFY(!QFile::exists(temp.filePath("fsmonitor-was-executed")));
#endif
    }
    void configuredCleanFilterStatus() {
#ifndef Q_OS_WIN
        QTemporaryDir temp; QVERIFY(temp.isValid());
        const auto git = [&](const QStringList &arguments) {
            QProcess process; process.setWorkingDirectory(temp.path()); process.start("git", arguments);
            return process.waitForFinished() && process.exitCode() == 0;
        };
        QVERIFY(git({"init", "-q"}));
        QVERIFY(git({"config", "filter.fixture.clean", "sed s/WORKTREE/CANONICAL/g"}));
        QVERIFY(git({"config", "filter.fixture.smudge", "sed s/CANONICAL/WORKTREE/g"}));
        QVERIFY(git({"config", "filter.fixture.required", "true"}));
        QFile attributes(temp.filePath(".gitattributes")); QVERIFY(attributes.open(QIODevice::WriteOnly));
        attributes.write("file filter=fixture\n"); attributes.close();
        QFile file(temp.filePath("file")); QVERIFY(file.open(QIODevice::WriteOnly));
        file.write("WORKTREE\n"); file.close();
        QVERIFY(git({"add", "."}));
        QVERIFY(git({"-c", "user.name=Test", "-c", "user.email=test@example.invalid",
            "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false", "commit", "-qm", "fixture"}));
        GitWorkspace workspace; workspace.setRepositoryPath(temp.path());
        QTRY_VERIFY(!workspace.resolving() && !workspace.m_refreshActive);
        QVERIFY2(workspace.error().isEmpty(), qPrintable(workspace.error()));
        QVERIFY(workspace.m_gitStatus.value("file").isEmpty());
        QVERIFY(workspace.m_trackedFiles.contains("file"));
        QVERIFY(file.open(QIODevice::WriteOnly)); file.write("changed\n"); file.close();
        workspace.refreshTree();
        QTRY_VERIFY(!workspace.m_refreshActive);
        QCOMPARE(workspace.m_gitStatus.value("file"), QString("modified"));
#endif
    }
    void saveRespectsUploadLock() {
        QTemporaryDir temp; QProcess git; git.start("git", {"init", "-q", temp.path()});
        QVERIFY(git.waitForFinished());
        QFile file(temp.filePath("README.md")); QVERIFY(file.open(QIODevice::WriteOnly)); file.write("original"); file.close();
        GitWorkspace workspace; workspace.setRepositoryPath(temp.path()); QTRY_VERIFY(!workspace.resolving());
        workspace.openFile("README.md"); workspace.setText("edited");
        QLockFile lock(temp.filePath(".ddetector-upload.lock")); QVERIFY(lock.tryLock(0));
        QVERIFY(!workspace.save()); QVERIFY(workspace.dirty());
        QVERIFY(file.open(QIODevice::ReadOnly)); QCOMPARE(file.readAll(), QByteArray("original")); file.close();
        lock.unlock(); QVERIFY(workspace.save());
    }
    void largeFileMode() {
        QTemporaryDir temp; QVERIFY(temp.isValid());
        QProcess git; git.start("git", {"init", temp.path()});
        QVERIFY(git.waitForFinished()); QCOMPARE(git.exitCode(), 0);
        const auto write = [&](const QString &name, const QByteArray &contents) {
            QFile file(temp.filePath(name));
            QVERIFY(file.open(QIODevice::WriteOnly));
            QCOMPARE(file.write(contents), qint64(contents.size()));
        };
        write("normal.txt", "one\ntwo\n");
        write("long-line.txt", QByteArray(16 * 1024 + 1, 'x'));
        write("large.txt", QByteArray(256 * 1024 + 1, 'x').replace(1024, 1, "\n"));

        GitWorkspace workspace; workspace.setRepositoryPath(temp.path());
        QTRY_VERIFY(!workspace.resolving());
        workspace.openFile("normal.txt"); QVERIFY(!workspace.largeFileMode());
        workspace.openFile("long-line.txt"); QVERIFY(workspace.largeFileMode());
        workspace.openFile("large.txt"); QVERIFY(workspace.largeFileMode());
        workspace.setText("small again\n"); QVERIFY(!workspace.largeFileMode());
    }

    void formatting_data() {
        QTest::addColumn<QByteArray>("original");
        QTest::addColumn<QString>("model");
        QTest::addColumn<QByteArray>("ending");
        QTest::addColumn<bool>("mixed");
        const QList<QByteArray> bodies{
            "# Markdown\n\n- [link](https://example.org)  \n```html\n<b>literal</b>\n```\n",
            "{\n    \"tracks\": {\n        \"1\": \"https://example.org\"\n    }\n}\n",
            "<html><body><b>literal &amp; text</b><br/></body></html>\n",
            "", "no final newline", "unicode: \xc3\xa9\n\n"};
        int row = 0;
        for (const auto &body : bodies) {
            for (const QByteArray &ending : {QByteArray("\n"), QByteArray("\r\n"), QByteArray("\r")}) {
                for (bool bom : {false, true}) {
                    QByteArray disk = body;
                    disk.replace("\n", ending);
                    if (bom) disk.prepend(QByteArray::fromHex("efbbbf"));
                    QTest::newRow(qPrintable(QString::number(row++)))
                        << disk << QString::fromUtf8(body) << ending << false;
                }
            }
        }
        for (bool littleEndian : {false, true}) {
            const QString prefix = littleEndian ? "utf16-le" : "utf16-be";
            for (const QString &body : {QString(), QString("no final newline"),
                                       QString::fromUtf8("{\n  \"text\": \"é 水 😀\"\n}\n"),
                                       QString(QChar(0xfeff)) + "content\n"}) {
                for (const QByteArray &ending : {QByteArray("\n"), QByteArray("\r\n"), QByteArray("\r")}) {
                    QString disk = body; disk.replace("\n", QString::fromLatin1(ending));
                    QTest::newRow(qPrintable(prefix + QString::number(row++)))
                        << utf16Bytes(disk, littleEndian) << body << ending << false;
                }
            }
            QTest::newRow(qPrintable(prefix + "-mixed"))
                << utf16Bytes("one\r\ntwo\n", littleEndian)
                << QString("one\ntwo\n") << QByteArray() << true;
        }
        QTest::newRow("mixed") << QByteArray("one\r\ntwo\nthree\rfour")
                              << QString("one\ntwo\nthree\nfour") << QByteArray() << true;
        QTest::newRow("bom-mixed") << (QByteArray::fromHex("efbbbf") + "one\r\ntwo\n")
                                  << QString("one\ntwo\n") << QByteArray() << true;
    }
    void formatting() {
        QFETCH(QByteArray, original);
        QFETCH(QString, model);
        QFETCH(QByteArray, ending);
        QFETCH(bool, mixed);
        QTemporaryDir temp; QVERIFY(temp.isValid());
        QProcess git; git.start("git", {"init", temp.path()});
        QVERIFY(git.waitForFinished()); QCOMPARE(git.exitCode(), 0);
        const QString path = temp.filePath("document");
        QFile file(path); QVERIFY(file.open(QIODevice::WriteOnly));
        QCOMPARE(file.write(original), original.size()); file.close();
        GitWorkspace backend; backend.setRepositoryPath(temp.path());
                QTRY_VERIFY(!backend.resolving()); backend.openFile("document");
        QCOMPARE(backend.filePath(), "document"); QCOMPARE(backend.text(), model); QVERIFY(!backend.dirty());
#ifdef GIT_WORKSPACE_QML_TEST
        QQmlEngine engine;
        QSignalSpy warnings(&engine, &QQmlEngine::warnings);
        QQmlComponent component(&engine);
        // Exercise the parent's plain-text/load-guard contract without modifying its QML.
        component.setData(R"(
            import QtQuick
            import QtQuick.Controls
            TextArea {
                property var backend
                property bool loading: false
                textFormat: TextEdit.PlainText
                function load() {
                    loading = true
                    text = backend.text
                    loading = false
                }
                onTextChanged: if (backend && !loading) backend.text = text
                Connections {
                    target: backend
                    function onChanged() {
                        if (text !== backend.text) load()
                    }
                }
                Component.onCompleted: load()
            }
        )", QUrl());
        QVERIFY2(component.isReady(), qPrintable(component.errorString()));
        QScopedPointer<QObject> editor(component.createWithInitialProperties(
            {{"backend", QVariant::fromValue<QObject *>(&backend)}}));
        QVERIFY2(editor, qPrintable(component.errorString()));
        QCOMPARE(editor->property("text").toString(), model);
        QCOMPARE(warnings.count(), 0); QVERIFY(!backend.dirty());
#endif
        // A held descriptor still sees the original inode after save (no atomic replacement).
        QVERIFY(file.open(QIODevice::ReadOnly));
        const QDateTime timestamp = QDateTime::fromSecsSinceEpoch(946684800);
        QVERIFY(file.setFileTime(timestamp, QFileDevice::FileModificationTime));
        QVERIFY(backend.save()); QCOMPARE(file.readAll(), original);
        QCOMPARE(QFileInfo(path).lastModified(), timestamp);
        QFile disk(path); QVERIFY(disk.open(QIODevice::ReadOnly)); QCOMPARE(disk.readAll(), original); disk.close();
        backend.setText(model + "temporary"); QVERIFY(backend.dirty());
        backend.setText(model); QVERIFY(!backend.dirty());
        QString crlfModel = model; crlfModel.replace("\n", "\r\n");
        backend.setText(crlfModel); QCOMPARE(backend.text(), model); QVERIFY(!backend.dirty());
#ifdef GIT_WORKSPACE_QML_TEST
        QVERIFY(editor->setProperty("text", model + "edited\n"));
        QCOMPARE(backend.text(), model + "edited\n");
#else
        backend.setText(model + "edited\n");
#endif
        QVERIFY(backend.dirty());
        if (mixed) {
            QVERIFY(!backend.save()); QVERIFY(backend.error().contains("mixed line endings"));
            QVERIFY(backend.dirty());
            QVERIFY(disk.open(QIODevice::ReadOnly)); QCOMPARE(disk.readAll(), original);
            return;
        }
        QVERIFY2(backend.save(), qPrintable(backend.error())); QVERIFY(!backend.dirty());
        QString expectedText = model + "edited\n";
        // Files without any original terminator default to LF.
        expectedText.replace("\n", model.contains('\n') ? QString::fromLatin1(ending) : QString("\n"));
        QByteArray expected;
        if (original.startsWith(QByteArray::fromHex("fffe"))) expected = utf16Bytes(expectedText, true);
        else if (original.startsWith(QByteArray::fromHex("feff"))) expected = utf16Bytes(expectedText, false);
        else {
            expected = expectedText.toUtf8();
            if (original.startsWith(QByteArray::fromHex("efbbbf"))) expected.prepend(QByteArray::fromHex("efbbbf"));
        }
        QVERIFY(disk.open(QIODevice::ReadOnly)); QCOMPARE(disk.readAll(), expected); disk.close();
        backend.setText("temporary"); backend.requestClose(); backend.resolveUnsaved("discard");
        QCOMPARE(backend.text(), model + "edited\n"); QVERIFY(!backend.dirty());
        QVERIFY(backend.save());
        QVERIFY(disk.open(QIODevice::ReadOnly)); QCOMPARE(disk.readAll(), expected);
    }
#ifdef GIT_WORKSPACE_QML_TEST
    void qmlDocumentSwitching() {
        QTemporaryDir temp; QVERIFY(temp.isValid());
        QProcess git; git.start("git", {"init", temp.path()});
        QVERIFY(git.waitForFinished()); QCOMPARE(git.exitCode(), 0);
        const QStringList names{"list_v2.schema.json", ".gitignore", "README.md",
                                "append_track.py", "list.json", "migrate", "list_v2"};
        QByteArray schema;
        for (int i = 0; i < 418; ++i) schema += "schema line " + QByteArray::number(i) + "\n";
        QList<QByteArray> expected{schema, "build/\r\n", "# Readme\n", "print('track')\n",
                                  "{\"tracks\": {}}\r\n", "#!/bin/sh\nexit 0", ""};
        for (int i = 0; i < names.size(); ++i) {
            QFile file(temp.filePath(names[i])); QVERIFY(file.open(QIODevice::WriteOnly));
            QCOMPARE(file.write(expected[i]), expected[i].size());
        }
        GitWorkspace backend;
        backend.setRepositoryPath(temp.path());
        QTRY_VERIFY(!backend.resolving());
        QObject *editor = nullptr;
        int staleRevision = -1;
        QString staleText;
        int rejectedDuringLoad = 0;
        // Connect before QML: exercise notifications before its synchronization slot,
        // rather than depending on a particular Qt binding evaluation order.
        connect(&backend, &GitWorkspace::documentAboutToChange, this, [&] {
            staleRevision = backend.documentRevision();
            staleText = backend.text();
            QVERIFY(!backend.editDocument(staleRevision, staleText + "stale"));
            ++rejectedDuringLoad;
        });
        connect(&backend, &GitWorkspace::documentChanged, this, [&] {
            if (editor && editor->property("loadedRevision").toInt() != backend.documentRevision()) {
                QVERIFY(!backend.editDocument(staleRevision, staleText));
                // A text notification caused by enabled/highlighter changes while
                // the old editor is still installed must not target the new file.
                editor->setProperty("text", staleText + "notification");
            }
        });
        QQmlEngine engine;
        QSignalSpy warnings(&engine, &QQmlEngine::warnings);
        QQmlComponent component(&engine, QUrl("qrc:/GitWorkspace.qml"));
        QVERIFY2(component.isReady(), qPrintable(component.errorString()));
        QScopedPointer<QObject> object(component.createWithInitialProperties(
            {{"workspaceBackend", QVariant::fromValue<QObject *>(&backend)}}));
        QVERIFY2(object, qPrintable(component.errorString()));
        editor = object->findChild<QObject *>("workspaceEditor"); QVERIFY(editor);
        auto *item = qobject_cast<QQuickItem *>(object.data()); QVERIFY(item);
        QQuickWindow window;
        window.resize(900, 700);
        item->setParentItem(window.contentItem()); item->setSize(QSizeF(900, 700));
        window.show();
        QSignalSpy prompts(&backend, &GitWorkspace::unsavedPrompt);
        // Switch in a burst without delivering queued highlighter/document events.
        for (int pass = 0; pass < 3; ++pass) {
            for (int i = 0; i < names.size(); ++i) {
                backend.openFile(names[i]);
                QCOMPARE(backend.filePath(), names[i]);
                QCOMPARE(editor->property("text").toString(), editorText(QString::fromUtf8(expected[i])));
                QVERIFY(!backend.dirty());
                QVERIFY(backend.save());
            }
        }
        QCoreApplication::processEvents();
        QVERIFY(!backend.dirty());
        for (int pass = 0; pass < 3; ++pass) {
            for (int i = 0; i < names.size(); ++i) {
                const int previousRevision = backend.documentRevision();
                const QString previousText = backend.text();
                backend.openFile(names[i]);
                QCOMPARE(backend.filePath(), names[i]);
                QCOMPARE(backend.text(), editorText(QString::fromUtf8(expected[i])));
                QCOMPARE(editor->property("text").toString(), backend.text());
                QVERIFY(!backend.dirty());
                QVERIFY(!backend.editDocument(previousRevision, previousText));
                backend.refreshTree();
                backend.openFile("missing-file"); // Broad error notification, not a document change.
                QCoreApplication::processEvents();
                QCOMPARE(editor->property("text").toString(), backend.text());
                QVERIFY(!backend.dirty());
                QVERIFY2(backend.save(), qPrintable(backend.error()));
                QFile file(temp.filePath(names[i])); QVERIFY(file.open(QIODevice::ReadOnly));
                QCOMPARE(file.readAll(), expected[i]);
                if (pass == 1) {
                    // Insert through the actual TextArea API, including into an empty document.
                    const int position = editor->property("text").toString().size();
                    QVERIFY(QMetaObject::invokeMethod(editor, "insert", Q_ARG(int, position),
                                                      Q_ARG(QString, QString("X"))));
                    QCOMPARE(backend.text(), editorText(QString::fromUtf8(expected[i])) + "X");
                    QVERIFY(backend.dirty());
                    QVERIFY2(backend.save(), qPrintable(backend.error()));
                    expected[i] += "X";
                    QVERIFY(!backend.dirty());
                }
                // Check every file after every switch/save, not only the selected file.
                for (int j = 0; j < names.size(); ++j) {
                    QFile disk(temp.filePath(names[j])); QVERIFY(disk.open(QIODevice::ReadOnly));
                    QCOMPARE(disk.readAll(), expected[j]);
                }
            }
        }
        QCOMPARE(prompts.count(), 0);
        QVERIFY(rejectedDuringLoad >= names.size() * 3);
        QCOMPARE(warnings.count(), 0);
        object->setProperty("workspaceBackend", QVariant::fromValue<QObject *>(nullptr));
        QCOMPARE(editor->property("text").toString(), QString());
    }

    void qmlExpandedGeometry() {
        QTemporaryDir temp; QVERIFY(temp.isValid());
        QProcess git; git.start("git", {"init", temp.path()});
        QVERIFY(git.waitForFinished()); QCOMPARE(git.exitCode(), 0);
        GitWorkspace backend;
        backend.setRepositoryPath(temp.path());
        QTRY_VERIFY(!backend.resolving());
        QVERIFY(!backend.rootPath().isEmpty());
        QQmlEngine engine;
        QSignalSpy warnings(&engine, &QQmlEngine::warnings);
        QQmlComponent component(&engine, QUrl("qrc:/GitWorkspace.qml"));
        QVERIFY2(component.isReady(), qPrintable(component.errorString()));
        QQuickWindow window;
        window.resize(1200, 800);
        QScopedPointer<QObject> object(component.createWithInitialProperties({
            {"workspaceBackend", QVariant::fromValue<QObject *>(&backend)},
            {"expanded", true}
        }));
        QVERIFY2(object, qPrintable(component.errorString()));
        auto *root = qobject_cast<QQuickItem *>(object.data()); QVERIFY(root);
        root->setParentItem(window.contentItem());
        root->setSize(QSizeF(1200, 800));
        auto *mainSplit = root->findChild<QQuickItem *>("gitMainSplit"); QVERIFY(mainSplit);
        auto *editorSplit = root->findChild<QQuickItem *>("gitEditorSplit"); QVERIFY(editorSplit);
        auto *editorPane = root->findChild<QQuickItem *>("gitEditorPane"); QVERIFY(editorPane);
        auto *actions = root->findChild<QQuickItem *>("gitActionsPanel"); QVERIFY(actions);
        auto *tabs = root->findChild<QQuickItem *>("gitTabs"); QVERIFY(tabs);
        auto *editorHeader = root->findChild<QQuickItem *>("gitEditorHeader"); QVERIFY(editorHeader);
        window.show();
        QTest::qWait(100);
        for (auto *pane : {editorSplit, editorPane, actions}) {
            QQmlExpression attached(QQmlEngine::contextForObject(pane), pane,
                "JSON.stringify({fillWidth: SplitView.fillWidth, fillHeight: SplitView.fillHeight, "
                "minimumWidth: SplitView.minimumWidth, preferredWidth: SplitView.preferredWidth, "
                "maximumWidth: SplitView.maximumWidth, view: String(SplitView.view)})");
            qInfo() << pane->objectName() << "position" << pane->mapToItem(root, QPointF())
                    << "size" << pane->size() << "attached" << attached.evaluate()
                    << attached.error();
        }
        for (int cycle = 0; cycle < 4; ++cycle) {
            QVERIFY(root->setProperty("expanded", true));
            QTest::qWait(100);
            qInfo() << "cycle" << cycle << "root" << root->size()
                    << "split" << editorSplit->size() << "editor" << editorPane->size()
                    << "actions" << actions->size() << actions->mapToItem(root, QPointF());
            QTRY_VERIFY(mainSplit->isVisible());
            QTRY_VERIFY(qAbs(tabs->height() - 30) <= 1);
            QTRY_VERIFY(qAbs(tabs->mapToItem(actions, QPointF()).y()) <= 1);
            QVERIFY(root->setProperty("showingDiff", true));
            QTRY_VERIFY(!editorHeader->isVisible());
            QTRY_VERIFY(qAbs(mainSplit->width() - root->width()) <= 1);
            // Allow the divider, but no unallocated space to the right of either pane.
            QTRY_VERIFY(qAbs(editorSplit->width() + actions->width() - root->width()) <= 2);
            QTRY_VERIFY(qAbs(actions->mapToItem(root, QPointF(actions->width(), 0)).x()
                                - root->width()) <= 1);
            QTRY_VERIFY(qAbs(editorSplit->mapToItem(root, QPointF()).x()) <= 1);
            QTRY_VERIFY(editorPane->width() >= root->width() / 3);
            QTRY_VERIFY(editorPane->width() < editorSplit->width());
            QVERIFY(root->setProperty("expanded", false));
            QTRY_VERIFY(qAbs(editorSplit->width() - root->width()) <= 1);
            QTRY_VERIFY(qAbs(actions->width() - root->width()) <= 1);
            QTRY_VERIFY(actions->mapToItem(root, QPointF()).y()
                        >= editorSplit->mapToItem(root, QPointF(0, editorSplit->height())).y());
        }
        QCOMPARE(warnings.count(), 0);
    }

    void qmlComponent() {
        QQmlEngine engine;
        QSignalSpy warnings(&engine, &QQmlEngine::warnings);
        QQmlComponent component(&engine, QUrl("qrc:/GitWorkspace.qml"));
        QVERIFY2(component.isReady(), qPrintable(component.errorString()));
        QScopedPointer<QObject> object(component.create());
        QVERIFY2(object, qPrintable(component.errorString()));
        auto *item = qobject_cast<QQuickItem *>(object.data()); QVERIFY(item);
        QQuickWindow window;
        window.resize(900, 700);
        item->setParentItem(window.contentItem()); item->setSize(QSizeF(900, 700));
        window.show(); QTest::qWait(50);
        QCOMPARE(warnings.count(), 0);
        QTemporaryDir temp;
        QProcess git; git.start("git", {"init", temp.path()});
        QVERIFY(git.waitForFinished()); QCOMPARE(git.exitCode(), 0);
        QFile file(temp.filePath("a.txt")); QVERIFY(file.open(QIODevice::WriteOnly));
        file.write("hello"); file.close();
        GitWorkspace backend;
        QVERIFY(object->setProperty("workspaceBackend", QVariant::fromValue<QObject *>(&backend)));
        backend.setRepositoryPath(temp.path()); QTRY_VERIFY(!backend.resolving()); backend.openFile("a.txt");
        QVERIFY(object->setProperty("expanded", true));
        item->setSize(QSizeF(1200, 800));
        window.resize(1200, 800);
        QTRY_COMPARE(backend.changedFiles().size(), 1);
        QTest::qWait(50);
        QCOMPARE(item->size(), QSizeF(1200, 800));
        QCOMPARE(object->property("expanded").toBool(), true);
        QCOMPARE(warnings.count(), 0);
        backend.setText("edited"); backend.requestClose();
        QTest::qWait(50); QCOMPARE(warnings.count(), 0);
        backend.resolveUnsaved("cancel");
        // Remove the injected pointer before the stack-owned backend is destroyed.
        object->setProperty("workspaceBackend", QVariant::fromValue<QObject *>(nullptr));
    }
#endif
    void encodingAndExternalChanges() {
        QTemporaryDir temp; QVERIFY(temp.isValid());
        QProcess git; git.start("git", {"init", temp.path()});
        QVERIFY(git.waitForFinished()); QCOMPARE(git.exitCode(), 0);
        auto write = [&](const QString &name, const QByteArray &bytes) {
            QFile file(temp.filePath(name)); QVERIFY(file.open(QIODevice::WriteOnly));
            QCOMPARE(file.write(bytes), bytes.size());
        };
        const QByteArray bom = QByteArray::fromHex("efbbbf");
        // A second initial U+FEFF is document content, not another transport BOM.
        write("valid", bom + bom + "hello\r\n");
        write("invalid", bom + QByteArray::fromHex("c328"));
        write("nul", bom + QByteArray("a\0b", 3));
        write("utf16", QByteArray::fromHex("fffe00d8"));
        GitWorkspace backend; backend.setRepositoryPath(temp.path());
                QTRY_VERIFY(!backend.resolving()); backend.openFile("valid");
        QCOMPARE(backend.text(), QString(QChar(0xfeff)) + "hello\n");
        for (const QString &name : {QString("invalid"), QString("nul"), QString("utf16")}) {
            backend.openFile(name); QCOMPARE(backend.filePath(), "valid"); QVERIFY(!backend.error().isEmpty());
        }
        write("valid", "external\r\n");
        QVERIFY(!backend.save()); QVERIFY(!backend.dirty());
        QFile file(temp.filePath("valid")); QVERIFY(file.open(QIODevice::ReadOnly));
        QCOMPARE(file.readAll(), "external\r\n");
    }
    void rejectedEncoding_data() {
        QTest::addColumn<QByteArray>("bytes");
        QTest::addColumn<QString>("encoding");
        QTest::addColumn<QString>("reason");
        QTest::newRow("utf8-invalid") << QByteArray::fromHex("efbbbfc328") << QString("UTF-8 BOM") << QString("malformed");
        QTest::newRow("utf8-truncated") << QByteArray::fromHex("e282") << QString("UTF-8 (no BOM)") << QString("malformed");
        QTest::newRow("binary") << QByteArray("a\0b", 3) << QString("UTF-8 (no BOM)") << QString("NUL byte");
        QTest::newRow("bomless-utf16") << QByteArray::fromHex("61006200") << QString("UTF-8 (no BOM)") << QString("NUL byte");
        for (bool le : {false, true}) {
            const QByteArray bom = QByteArray::fromHex(le ? "fffe" : "feff");
            const QString encoding = le ? "UTF-16 LE BOM" : "UTF-16 BE BOM";
            QTest::newRow(qPrintable(encoding + "-odd")) << (bom + "a") << encoding << QString("odd payload");
            QTest::newRow(qPrintable(encoding + "-high-surrogate"))
                << (bom + QByteArray::fromHex(le ? "00d8" : "d800")) << encoding << QString("malformed");
            QTest::newRow(qPrintable(encoding + "-low-surrogate"))
                << (bom + QByteArray::fromHex(le ? "00dc" : "dc00")) << encoding << QString("malformed");
            QTest::newRow(qPrintable(encoding + "-nul"))
                << utf16Bytes(QString("a") + QChar(0) + "b", le) << encoding << QString("decoded NUL");
        }
        QTest::newRow("utf32-le") << QByteArray::fromHex("fffe000061000000") << QString("UTF-32 BOM") << QString("unsupported");
        QTest::newRow("utf32-be") << QByteArray::fromHex("0000feff00000061") << QString("UTF-32 BOM") << QString("unsupported");
    }
    void rejectedEncoding() {
        QFETCH(QByteArray, bytes); QFETCH(QString, encoding); QFETCH(QString, reason);
        QTemporaryDir temp; QVERIFY(temp.isValid());
        QProcess git; git.start("git", {"init", temp.path()});
        QVERIFY(git.waitForFinished()); QCOMPARE(git.exitCode(), 0);
        QFile file(temp.filePath("bad.json")); QVERIFY(file.open(QIODevice::WriteOnly));
        QCOMPARE(file.write(bytes), bytes.size()); file.close();
        GitWorkspace backend; backend.setRepositoryPath(temp.path());
                QTRY_VERIFY(!backend.resolving()); backend.openFile("bad.json");
        QVERIFY(backend.filePath().isEmpty());
        QVERIFY2(backend.error().contains("bad.json"), qPrintable(backend.error()));
        QVERIFY2(backend.error().contains(encoding), qPrintable(backend.error()));
        QVERIFY2(backend.error().contains(reason), qPrintable(backend.error()));
        QVERIFY(file.open(QIODevice::ReadOnly)); QCOMPARE(file.readAll(), bytes);
    }
    void invalidSave() {
        QTemporaryDir temp; QVERIFY(temp.isValid());
        QProcess git; git.start("git", {"init", temp.path()});
        QVERIFY(git.waitForFinished()); QCOMPARE(git.exitCode(), 0);
        for (bool le : {false, true}) {
            const QString name = le ? "le.json" : "be.json";
            const QByteArray original = utf16Bytes("{}\r\n", le);
            QFile file(temp.filePath(name)); QVERIFY(file.open(QIODevice::WriteOnly));
            QCOMPARE(file.write(original), original.size()); file.close();
            GitWorkspace backend; backend.setRepositoryPath(temp.path());
                        QTRY_VERIFY(!backend.resolving()); backend.openFile(name);
            for (const QString &invalid : {QString("a") + QChar(0) + "b", QString(QChar(0xd800)), QString(QChar(0xdc00))}) {
                backend.setText(invalid); QVERIFY(!backend.save()); QVERIFY(backend.dirty());
                QVERIFY(backend.error().contains(name)); QVERIFY(backend.error().contains("UTF-16"));
                QVERIFY(file.open(QIODevice::ReadOnly)); QCOMPARE(file.readAll(), original); file.close();
            }
            backend.setText(QString(1024 * 1024 + 1, 'x')); QVERIFY(!backend.save());
            QVERIFY(backend.error().contains("2 MiB"));
            QVERIFY(file.open(QIODevice::ReadOnly)); QCOMPARE(file.readAll(), original);
        }
    }
    void workflow() {
        QTemporaryDir temp;
        QVERIFY(temp.isValid());
        QProcess git;
        git.start("git", {"init", temp.path()});
        QVERIFY(git.waitForFinished()); QCOMPARE(git.exitCode(), 0);
        QDir dir(temp.path()); QVERIFY(dir.mkdir("sub"));
        auto write = [&](const QString &name, const QByteArray &data) {
            QFile f(dir.filePath(name)); QVERIFY(f.open(QIODevice::WriteOnly)); QCOMPARE(f.write(data), data.size());
        };
        write("sub/a.txt", "hello\r\n"); write("b.txt", "second");
        write("binary", QByteArray("a\0b", 3));
        write("large", QByteArray(GitWorkspace::MaximumTextBytes + 1, 'x'));
        GitWorkspace workspace;
        workspace.setRepositoryPath(dir.filePath("sub"));
        QTRY_VERIFY(!workspace.resolving());
        QCOMPARE(workspace.rootPath(), dir.canonicalPath());
        for (const auto &row : workspace.tree()) QVERIFY(row.toMap().value("name") != ".git");
        workspace.toggleDirectory("sub");
        workspace.openFile("sub/a.txt"); QCOMPARE(workspace.text(), "hello\n");
        workspace.setText("changed"); QVERIFY(workspace.dirty());
        QSignalSpy prompt(&workspace, &GitWorkspace::unsavedPrompt);
        QSignalSpy closed(&workspace, &GitWorkspace::closeApproved);
        workspace.openFile("b.txt"); QCOMPARE(prompt.count(), 1);
        workspace.resolveUnsaved("cancel"); QCOMPARE(workspace.filePath(), "sub/a.txt");
        workspace.openFile("b.txt"); workspace.resolveUnsaved("save");
        QCOMPARE(workspace.filePath(), "b.txt");
        QFile saved(dir.filePath("sub/a.txt")); QVERIFY(saved.open(QIODevice::ReadOnly)); QCOMPARE(saved.readAll(), "changed");
        workspace.openFile("../outside"); QCOMPARE(workspace.filePath(), "b.txt");
        workspace.openFile(".git/config"); QCOMPARE(workspace.filePath(), "b.txt");
#ifndef Q_OS_WIN
        QVERIFY(QFile::link(dir.filePath("b.txt"), dir.filePath("link")));
        workspace.openFile("link"); QCOMPARE(workspace.filePath(), "b.txt");
#endif
        workspace.openFile("binary"); QCOMPARE(workspace.filePath(), "b.txt");
        workspace.openFile("large"); QCOMPARE(workspace.filePath(), "b.txt");
        workspace.setText("local"); write("b.txt", "external"); QVERIFY(!workspace.save());
        workspace.requestClose(); workspace.resolveUnsaved("save"); QCOMPARE(closed.count(), 0);
        workspace.resolveUnsaved("discard"); QCOMPARE(closed.count(), 1); QVERIFY(!workspace.dirty());
        workspace.startTerminal(); QTRY_VERIFY(workspace.terminalRunning());
        QTRY_VERIFY(workspace.sendTerminalInput("echo workspace-shell-test\n"));
        QTRY_VERIFY(workspace.terminalOutput().contains("workspace-shell-test"));
        QVERIFY(workspace.sendTerminalInput("exit\n")); QTRY_VERIFY(!workspace.terminalRunning());
    }
};
QTEST_MAIN(WorkspaceTest)
#include "workspace.moc"
#endif

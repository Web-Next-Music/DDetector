#include "workspace.h"

#include <QDir>
#include <QDirIterator>
#include <QFile>
#include <QFileInfo>
#include <QSaveFile>
#include <QProcessEnvironment>
#include <QProcess>
#include <QTimer>
#include <QStringEncoder>

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
    connect(&m_terminal, &TerminalSession::output, this, &GitWorkspace::appendOutput);
    connect(&m_terminal, &TerminalSession::changed, this, [this] { emit terminalChanged(); });
    connect(&m_terminal, &TerminalSession::finished, this, [this] {
        appendOutput("\n[shell exited]\n");
    });
}

GitWorkspace::~GitWorkspace() = default;

void GitWorkspace::fail(const QString &message) {
    m_error = message;
    emit changed();
}

void GitWorkspace::setRepositoryPath(const QString &path) {
    if (path == m_repository) return;
    if (dirty() || m_pending) {
        fail("Save or discard the current document before changing the repository.");
        return;
    }
    if (terminalRunning()) {
        fail("Stop the shell before changing the repository.");
        return;
    }
    m_loadingDocument = true;
    emit documentAboutToChange();
    ++m_documentRevision;
    m_refreshDebounce.stop();
    if (!m_watcher.files().isEmpty()) m_watcher.removePaths(m_watcher.files());
    if (!m_watcher.directories().isEmpty()) m_watcher.removePaths(m_watcher.directories());
    m_gitStatus.clear(); m_gitDirectories.clear(); m_trackedFiles.clear();
    m_repository = path;
    m_root.clear(); m_file.clear(); m_text.clear(); m_saved.clear();
    m_diskBytes.clear(); m_tree.clear(); m_expanded.clear(); m_error.clear();
    emit documentChanged();
    m_loadingDocument = false;
    if (path.trimmed().isEmpty()) { emit changed(); return; }
    QFileInfo input(path);
    if (!input.isDir()) { fail("Repository path must be an existing directory."); return; }
    QProcess git;
    git.setWorkingDirectory(input.canonicalFilePath());
    // Ignore inherited Git overrides so validation applies to the requested directory.
    auto env = QProcessEnvironment::systemEnvironment();
    for (const QString &key : env.keys())
        if (key.startsWith("GIT_")) env.remove(key);
    git.setProcessEnvironment(env);
    git.start("git", {"rev-parse", "--show-toplevel"});
    if (!git.waitForStarted(3000) || !git.waitForFinished(5000)) {
        git.kill(); git.waitForFinished(1000);
        fail("Git validation timed out or git could not be started."); return;
    }
    if (git.exitStatus() != QProcess::NormalExit || git.exitCode() != 0) {
        fail("Not a Git working tree: " + QString::fromUtf8(git.readAllStandardError()).trimmed());
        return;
    }
    QString root = QString::fromUtf8(git.readAllStandardOutput());
    if (root.endsWith('\n')) root.chop(1);
    if (root.endsWith('\r')) root.chop(1);
    m_root = QFileInfo(root).canonicalFilePath();
    if (m_root.isEmpty() || !QFileInfo(m_root).isDir()) {
        m_root.clear(); fail("Git returned an invalid working tree root."); return;
    }
    for (const QString &option : {QString("--absolute-git-dir"), QString("--git-common-dir")}) {
        QByteArray output = gitOutput({"rev-parse", option});
        if (output.endsWith('\n')) output.chop(1);
        const QString directory = QFileInfo(QDir(m_root).absoluteFilePath(QFile::decodeName(output))).canonicalFilePath();
        if (!directory.isEmpty() && !m_gitDirectories.contains(directory)) m_gitDirectories.append(directory);
    }
    refreshTree();
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
                                {"statusColor", status == "new" ? "#008a4f" : status == "modified" ? "#ab910d" : ""}});
        if (expanded) appendTree(rel, depth + 1);
    }
}

QByteArray GitWorkspace::gitOutput(const QStringList &arguments) const {
    QProcess git;
    git.setWorkingDirectory(m_root);
    auto env = QProcessEnvironment::systemEnvironment();
    for (const QString &key : env.keys())
        if (key.startsWith("GIT_")) env.remove(key);
    env.insert("GIT_OPTIONAL_LOCKS", "0"); // Status must not rewrite the watched index.
    git.setProcessEnvironment(env);
    git.start("git", arguments);
    if (!git.waitForStarted(3000) || !git.waitForFinished(5000)) {
        git.kill(); git.waitForFinished(1000);
        return {};
    }
    if (git.exitStatus() != QProcess::NormalExit || git.exitCode() != 0) return {};
    return git.readAllStandardOutput();
}

void GitWorkspace::updateGitStatus() {
    m_gitStatus.clear();
    const auto records = gitOutput({"status", "--porcelain=v1", "-z", "--untracked-files=all"}).split('\0');
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
    m_trackedFiles.clear();
    for (const QByteArray &path : gitOutput({"ls-files", "-z"}).split('\0'))
        if (!path.isEmpty()) m_trackedFiles.append(QFile::decodeName(path));
}

void GitWorkspace::updateWatches() {
    QSet<QString> desired;
    const auto add = [&desired](const QString &path) {
        if (!path.isEmpty() && QFileInfo::exists(path)) desired.insert(path);
    };
    add(m_root);
    for (const QVariant &row : m_tree) {
        const auto item = row.toMap();
        if (!item.value("directory").toBool() || item.value("expanded").toBool())
            add(QDir(m_root).filePath(item.value("path").toString()));
    }
    for (const QString &path : m_trackedFiles) {
        add(confined(path));
        // Also observe replacements/deletions in collapsed tracked directories.
        QString parent = path;
        while (parent.contains('/')) {
            parent.truncate(parent.lastIndexOf('/'));
            add(confined(parent, true));
        }
    }
    const QString gitLink = QDir(m_root).filePath(".git");
    if (QFileInfo(gitLink).isFile()) add(gitLink);
    for (const QString &directory : m_gitDirectories) {
        add(directory);
        for (const QString &name : {"HEAD", "index", "packed-refs", "config", "commondir"})
            add(QDir(directory).filePath(name));
        add(QDir(directory).filePath("info"));
        add(QDir(directory).filePath("info/exclude"));
        for (const QString &name : QDir(directory).entryList({"sharedindex.*"}, QDir::Files))
            add(QDir(directory).filePath(name));
        const QString refs = QDir(directory).filePath("refs");
        add(refs);
        QDirIterator it(refs, QDir::AllEntries | QDir::NoDotAndDotDot | QDir::Hidden,
                        QDirIterator::Subdirectories);
        while (it.hasNext()) {
            it.next();
            if (!it.fileInfo().isSymLink()) add(it.filePath());
        }
    }
    QSet<QString> current;
    for (const QString &path : m_watcher.files() + m_watcher.directories()) current.insert(path);
    const auto removed = current - desired;
    const auto added = desired - current;
    if (!removed.isEmpty()) m_watcher.removePaths(removed.values());
    if (!added.isEmpty()) m_watcher.addPaths(added.values());
}

void GitWorkspace::refreshTree() {
    m_refreshDebounce.stop();
    m_tree.clear();
    if (!m_root.isEmpty()) {
        updateGitStatus();
        appendTree({}, 0);
        updateWatches();
    }
    // Never reload editor content here: external edits must still fail save's disk check.
    emit changed();
}

void GitWorkspace::toggleDirectory(const QString &path) {
    if (confined(path, true).isEmpty()) { fail("Directory is unavailable or outside the workspace."); return; }
    if (m_expanded.contains(path)) m_expanded.remove(path); else m_expanded.insert(path);
    refreshTree();
}

void GitWorkspace::setText(const QString &text) {
    const QString normalized = editorText(text);
    if (m_loadingDocument || m_file.isEmpty() || m_text == normalized) return;
    m_text = normalized;
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
    if (path.isEmpty()) { fail("File is unavailable, a symlink, or outside the workspace."); return false; }
    QFile file(path);
    if (!file.open(QIODevice::ReadOnly)) { fail(file.errorString()); return false; }
    if (file.size() > MaximumTextBytes) { fail("Text files are limited to 2 MiB."); return false; }
    const QByteArray bytes = file.read(MaximumTextBytes + 1);
    if (file.error() != QFileDevice::NoError || bytes.size() > MaximumTextBytes) {
        fail("Could not read file within the 2 MiB limit."); return false;
    }
    auto reject = [&](const QString &encoding, const QString &reason) {
        fail(QString("Cannot edit '%1' (%2): %3").arg(relativePath, encoding, reason));
        return false;
    };
    // Check UTF-32 first: its LE BOM starts with the UTF-16 LE BOM.
    if (bytes.startsWith(QByteArray::fromHex("fffe0000"))
        || bytes.startsWith(QByteArray::fromHex("0000feff")))
        return reject("UTF-32 BOM", "unsupported encoding; use UTF-8 or BOM-marked UTF-16.");
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
        return reject(encodingName, "truncated UTF-16 code unit (odd payload byte count).");
    if (encoding == QStringConverter::Utf8 && payload.contains('\0'))
        return reject(encodingName, "NUL byte found; binary or BOM-less UTF-16 is not supported.");
    // Strip exactly one transport BOM, retaining any subsequent U+FEFF as content.
    QStringDecoder decoder(encoding, QStringConverter::Flag::ConvertInitialBom
                                     | QStringConverter::Flag::Stateless);
    const QString text = decoder.decode(payload);
    QStringEncoder encoder(encoding, QStringConverter::Flag::Stateless);
    const QByteArray roundTrip = encoder.encode(text);
    if (!validUnicode(text) || decoder.hasError() || encoder.hasError() || roundTrip != payload)
        return reject(encodingName, "malformed or truncated Unicode sequence; lossless decoding failed.");
    if (text.contains(QChar(0)))
        return reject(encodingName, "decoded NUL character found; binary content cannot be edited.");
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
    const QString path = confined(m_file);
    if (path.isEmpty()) { fail("Cannot save: file is no longer a confined regular file."); return false; }
    if (dirty() && m_mixedEndings) {
        fail("Save refused: mixed line endings cannot be safely preserved. Discard changes or normalize the file externally.");
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
        return reject("malformed Unicode text; unpaired surrogate found.");
    if (diskText.contains(QChar(0)))
        return reject("decoded NUL character found; binary content cannot be saved.");
    QStringEncoder encoder(m_encoding, QStringConverter::Flag::Stateless);
    const QByteArray payload = encoder.encode(diskText);
    QStringDecoder decoder(m_encoding, QStringConverter::Flag::ConvertInitialBom
                                      | QStringConverter::Flag::Stateless);
    const QString roundTrip = decoder.decode(payload);
    if (encoder.hasError() || decoder.hasError() || roundTrip != diskText)
        return reject("malformed Unicode text; lossless encoding failed.");
    QByteArray bom;
    if (m_hasBom) {
        bom = QByteArray::fromHex(m_encoding == QStringConverter::Utf16LE ? "fffe"
                                 : m_encoding == QStringConverter::Utf16BE ? "feff" : "efbbbf");
    }
    const QByteArray bytes = bom + payload;
    if (bytes.size() > MaximumTextBytes)
        return reject("encoded text exceeds the 2 MiB limit.");
    QFile current(path);
    if (!current.open(QIODevice::ReadOnly) || current.size() > MaximumTextBytes
        || current.read(MaximumTextBytes + 1) != m_diskBytes
        || current.error() != QFileDevice::NoError) {
        fail("File changed on disk or cannot be read; save refused to avoid overwriting external edits.");
        return false;
    }
    current.close();
    if (!dirty()) { m_error.clear(); emit changed(); return true; }
    // Recheck after reading; QSaveFile must never fall back to truncating the original.
    if (confined(m_file) != path) { fail("File path changed during save."); return false; }
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

void GitWorkspace::startTerminal() {
    if (terminalRunning()) return;
    if (m_root.isEmpty() || !QFileInfo(m_root).isDir()) { fail("Select a valid repository first."); return; }
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
#include <QtTest>
#ifdef GIT_WORKSPACE_QML_TEST
#include "terminal.h"
#include "syntaxhighlighter.h"
#include <qqml.h>
#include <QQmlComponent>
#include <QQmlEngine>
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
        QCOMPARE(metadata(unusual, "statusColor"), QString("#008a4f"));
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
        GitWorkspace backend; backend.setRepositoryPath(temp.path()); backend.openFile("document");
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
        QQmlComponent component(&engine, QUrl::fromLocalFile(
            QFileInfo(QString::fromUtf8(__FILE__)).absoluteDir().filePath("GitWorkspace.qml")));
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

    void qmlComponent() {
        QQmlEngine engine;
        QSignalSpy warnings(&engine, &QQmlEngine::warnings);
        QQmlComponent component(&engine, QUrl::fromLocalFile(
            QFileInfo(QString::fromUtf8(__FILE__)).absoluteDir().filePath("GitWorkspace.qml")));
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
        backend.setRepositoryPath(temp.path()); backend.openFile("a.txt");
        QTest::qWait(50); QCOMPARE(warnings.count(), 0);
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
        GitWorkspace backend; backend.setRepositoryPath(temp.path()); backend.openFile("valid");
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
        GitWorkspace backend; backend.setRepositoryPath(temp.path()); backend.openFile("bad.json");
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
            GitWorkspace backend; backend.setRepositoryPath(temp.path()); backend.openFile(name);
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

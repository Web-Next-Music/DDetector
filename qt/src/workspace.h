#pragma once

#include <QObject>
#include "terminalsession.h"
#include <QStringDecoder>
#include <QVariantList>
#include <QSet>
#include <QHash>
#include <QFileSystemWatcher>
#include <QTimer>
#include <QProcess>
#include <functional>

// Standalone QObject: no context properties, configuration, or QML registration required.
// Shell commands are trusted user input, NOT sandboxed by the editor's path checks.
class GitWorkspace : public QObject {
    Q_OBJECT
    Q_PROPERTY(QString repositoryPath READ repositoryPath WRITE setRepositoryPath NOTIFY changed)
    Q_PROPERTY(QString rootPath READ rootPath NOTIFY changed)
    Q_PROPERTY(QString filePath READ filePath NOTIFY documentChanged)
    Q_PROPERTY(QString text READ text WRITE setText NOTIFY documentChanged)
    Q_PROPERTY(int documentRevision READ documentRevision NOTIFY documentChanged)
    Q_PROPERTY(bool largeFileMode READ largeFileMode NOTIFY documentChanged)
    Q_PROPERTY(bool fileLoading READ fileLoading NOTIFY changed)
    Q_PROPERTY(QString loadingFilePath READ loadingFilePath NOTIFY changed)
    Q_PROPERTY(bool dirty READ dirty NOTIFY changed)
    Q_PROPERTY(QString error READ error NOTIFY changed)
    Q_PROPERTY(bool resolving READ resolving NOTIFY changed)
    Q_PROPERTY(QVariantList tree READ tree NOTIFY treeChanged)
    Q_PROPERTY(QVariantList commits READ commits NOTIFY gitChanged)
    Q_PROPERTY(QVariantList changedFiles READ changedFiles NOTIFY changesChanged)
    Q_PROPERTY(QVariantList stashes READ stashes NOTIFY stashesChanged)
    Q_PROPERTY(QString branch READ branch NOTIFY gitChanged)
        Q_PROPERTY(int aheadCount READ aheadCount NOTIFY gitChanged)
    Q_PROPERTY(QString diffText READ diffText NOTIFY diffChanged)
    Q_PROPERTY(bool gitBusy READ gitBusy NOTIFY gitStateChanged)
    Q_PROPERTY(QString gitMessage READ gitMessage NOTIFY gitStateChanged)
    Q_PROPERTY(QString terminalOutput READ terminalOutput NOTIFY terminalChanged)
    Q_PROPERTY(bool terminalRunning READ terminalRunning NOTIFY terminalChanged)
    Q_PROPERTY(TerminalSession *terminalSession READ terminalSession CONSTANT)
public:
    explicit GitWorkspace(QObject *parent = nullptr);
    ~GitWorkspace() override;
    QString repositoryPath() const { return m_repository; }
    QString rootPath() const { return m_root; }
    QString filePath() const { return m_file; }
    QString text() const { return m_text; }
    int documentRevision() const { return m_documentRevision; }
    bool largeFileMode() const { return m_largeFileMode; }
    bool fileLoading() const { return m_fileLoading; }
    QString loadingFilePath() const { return m_loadingFilePath; }
    bool dirty() const { return m_text != m_saved; }
    QString error() const;
    bool resolving() const { return m_resolving; }
    bool canChangeRepository() const { return !dirty() && !m_pending && !terminalRunning() && !m_resolving && !m_gitActionActive; }
    void setRepositoryValidator(std::function<QString(const QString &)> validator) { m_repositoryValidator = std::move(validator); }
    void setWriteValidator(std::function<QString()> validator) { m_writeValidator = std::move(validator); }
        QVariantList tree() const { return m_tree; }
    QString terminalOutput() const { return m_output; }
    bool terminalRunning() const { return m_terminal.running(); }
    TerminalSession *terminalSession() { return &m_terminal; }
    void setRepositoryPath(const QString &path);
    void setText(const QString &text);
    Q_INVOKABLE void activate();
    Q_INVOKABLE bool editDocument(int revision, const QString &text);
    QVariantList commits() const { return m_commits; }
    QString branch() const { return m_branch; }
        int aheadCount() const { return m_aheadCount; }
    bool gitBusy() const { return m_gitActionActive; }
    QString gitMessage() const { return m_gitMessage; }
    QVariantList changedFiles() const { return m_changedFiles; }
    QVariantList stashes() const { return m_stashes; }
    Q_INVOKABLE void stageFile(const QString &path, bool staged);
    Q_INVOKABLE void stageAll(bool staged);
    QString diffText() const { return m_diffText; }
    Q_INVOKABLE void viewDiff(QString path = QString());
    Q_INVOKABLE void refreshStashes();
    Q_INVOKABLE void refreshGit();
    Q_INVOKABLE void gitAction(QString action, QString message = QString());
    Q_INVOKABLE void commitWithOptions(QString message, bool amend, bool signoff, bool skipHooks, bool stagedOnly = false);
    Q_INVOKABLE void openSystemTerminal();
    Q_INVOKABLE void refreshTree();
    Q_INVOKABLE void toggleDirectory(const QString &relativePath);
    Q_INVOKABLE void openFile(const QString &relativePath);
    Q_INVOKABLE bool save();
    Q_INVOKABLE void requestClose();
    // decision: "save", "discard", "cancel". Failed saves keep the prompt pending.
    Q_INVOKABLE void resolveUnsaved(const QString &decision);
    Q_INVOKABLE void startTerminal(bool automatic = false);
    Q_INVOKABLE bool sendTerminalInput(const QString &text);
    Q_INVOKABLE void stopTerminal();
    // Sends terminal Ctrl+D; unlike a pipe this does not permanently close stdin.
    Q_INVOKABLE void closeTerminalInput();
    Q_INVOKABLE void clearTerminal();
    static constexpr qint64 MaximumTextBytes = 2 * 1024 * 1024;
signals:
    void activationRequested();
    void changed();
    void documentAboutToChange();
    void documentChanged();
    void terminalChanged();
    void gitChanged();
    void changesChanged();
    void stashesChanged();
    void diffChanged();
        void gitStateChanged();
        void treeChanged();
    void commitSucceeded();
    void unsavedPrompt();
    void closeApproved();
private:
    QString confined(const QString &relativePath, bool directory = false) const;
    void updateLargeFileMode();
    void loadFile(const QString &relativePath);
    void startFileLoad();
    void cancelFileLoad();
    void appendTree(const QString &relativePath, int depth);
    void updateGitStatus(const QByteArray &status);
    void rebuildTree();
    void updateWatches();
    using GitCallback = std::function<void(const QByteArray &, const QString &)>;
    void runGit(const QString &directory, const QStringList &arguments, GitCallback callback, bool operations = false);
    void cancelGit();
    void gitActionWithOptions(QString action, QString message, bool amend, bool signoff, bool skipHooks);
    void finishRefresh();
    void refreshChanges(const QStringList &tracked, quint64 revision);
    bool validGitPath(const QString &path) const;
    void fail(const QString &message);
    void appendOutput(const QString &text);
    QString m_repository, m_root, m_file, m_text, m_saved, m_error, m_output;
    QByteArray m_diskBytes;
    int m_documentRevision = 0;
    bool m_loadingDocument = false;
    bool m_fileLoading = false;
    // One worker at a time; m_loadingFilePath holds only the newest requested file.
    bool m_fileReadActive = false;
    QString m_loadingFilePath;
    quint64 m_fileGeneration = 0;
    bool m_watchesDirty = true;
    bool m_largeFileMode = false;
    // The editor uses LF without a transport BOM; disk formatting is separate.
    QString m_lineEnding = "\n";
    QStringConverter::Encoding m_encoding = QStringConverter::Utf8;
    bool m_hasBom = false, m_mixedEndings = false;
    QVariantList m_tree;
    QSet<QString> m_expanded;
    QHash<QString, QString> m_gitStatus;
    QStringList m_gitDirectories, m_trackedFiles;
    QFileSystemWatcher m_watcher;
    QTimer m_refreshDebounce, m_reconcile;
    QProcess *m_git = nullptr;
    QProcess *m_gitOperation = nullptr;
    QVariantList m_commits, m_changedFiles, m_stashes;
    quint64 m_actionRevision = 0;
    QString m_branch, m_gitMessage, m_diffText;
        int m_aheadCount = 0;
    bool m_gitBusy = false, m_gitActionActive = false, m_gitRefreshAgain = false;
    quint64 m_gitGeneration = 0;
    bool m_resolving = false, m_refreshActive = false, m_refreshAgain = false;
    bool m_activationRequested = false;
    QString m_gitError, m_watchError;
    int m_maximumWatches = 4096;
    int m_gitDeadlineMs = 5000;
    std::function<QString(const QString &)> m_repositoryValidator;
        std::function<QString()> m_writeValidator;
    friend class WorkspaceTest;
    QString m_pendingFile;
    bool m_pending = false, m_pendingClose = false;
    TerminalSession m_terminal;
};

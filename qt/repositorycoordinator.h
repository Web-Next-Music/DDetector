#pragma once

#include "config.h"
#include "workspace.h"
#include "uploader.h"
#include <QPointer>

// Header-only so the application and existing C++ test targets share the same
// orchestration without introducing a new link dependency or QML API.
class RepositoryCoordinator final : public QObject {
public:
    RepositoryCoordinator(Config &config, GitWorkspace &workspace, Uploader &uploader)
        : m_config(&config), m_workspace(&workspace), m_uploader(&uploader) {
        config.setRepositoryValidator([this](const QString &) {
            if (!m_workspace || !m_uploader) return QString("Repository backend is unavailable.");
            if (m_uploader->busy()) return QString("Finish the current Send before changing the repository.");
            if (!m_workspace->canChangeRepository())
                return QString("Save/discard the document, stop the shell and wait for repository validation first.");
            return QString();
        });
        workspace.setRepositoryValidator([this](const QString &path) {
            if (!m_config || path != m_config->repositoryPath())
                return QString("Change the repository through configuration.");
            if (m_uploader && m_uploader->busy()) return QString("Finish the current Send before changing the repository.");
            return QString();
        });
        uploader.setDirectoryValidator([this](const QString &path) {
            if (m_applying) return QString();
            const auto expected = m_workspace && !m_workspace->resolving() ? m_workspace->rootPath() : QString();
            return path == expected ? QString() : QString("Uploader directory must match the workspace.");
        });
        uploader.setSendValidator([this](const QString &id) {
            if (!m_workspace || !m_config || !m_uploader || m_workspace->resolving()
                || m_workspace->repositoryPath() != m_config->repositoryPath()
                || m_uploader->directory() != m_workspace->rootPath())
                return QString("Wait for the configured repository to be ready before sending.");
            const QString file = m_workspace->filePath();
            if (m_workspace->dirty() && (file == "list.json" || file == "README.md" || file == "tracks/" + id))
                return QString("Save or discard edits to the upload target before sending.");
            return QString();
        });
        connect(&config, &Config::repositoryPathChanged, this, [this] { applyRepository(); });
        connect(&workspace, &GitWorkspace::changed, this, [this] { syncUploader(); });
        connect(&uploader, &Uploader::stateChanged, this, [this] { syncUploader(); });
        connect(&uploader, &Uploader::completed, &workspace, [&workspace](const QString &) { workspace.refreshTree(); });
        applyRepository();
    }
    ~RepositoryCoordinator() override {
        if (m_config) m_config->setRepositoryValidator({});
        if (m_workspace) m_workspace->setRepositoryValidator({});
        if (m_uploader) { m_uploader->setDirectoryValidator({}); m_uploader->setSendValidator({}); }
    }
private:
    void applyRepository() {
        if (m_workspace && m_config) m_workspace->setRepositoryPath(m_config->repositoryPath());
        syncUploader();
    }
    void syncUploader() {
        if (m_applying || !m_workspace || !m_uploader || m_uploader->busy()) return;
        m_applying = true;
        m_uploader->setDirectory(m_workspace->resolving() ? QString() : m_workspace->rootPath());
        m_applying = false;
    }
    QPointer<Config> m_config;
    QPointer<GitWorkspace> m_workspace;
    QPointer<Uploader> m_uploader;
    bool m_applying = false;
};

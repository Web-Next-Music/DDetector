#pragma once

#include <QPointer>
#include <QQuickTextDocument>
#include <KSyntaxHighlighting/Definition>
#include <KSyntaxHighlighting/Repository>
#include <memory>

// Stable QML identity; timer-driven parsing is retired before buffer replacement.
class SyntaxHighlighter : public QObject {
    Q_OBJECT
    Q_PROPERTY(QQuickTextDocument *document READ quickDocument WRITE setQuickDocument NOTIFY documentChanged)
    Q_PROPERTY(QString filePath READ filePath WRITE setFilePath NOTIFY filePathChanged)
public:
    explicit SyntaxHighlighter(QObject *parent = nullptr);
    ~SyntaxHighlighter() override;
    QQuickTextDocument *quickDocument() const { return m_document; }
    void setQuickDocument(QQuickTextDocument *document);
    void setDocument(QTextDocument *document);
    QString filePath() const { return m_filePath; }
    void setFilePath(const QString &path);
    // Explicit synchronous pass; automatic attach, path changes and edits are batched.
    void rehighlight();
    // Call before any full-buffer mutation, even when the document identity is unchanged.
    Q_INVOKABLE void beginDocumentReplacement();
    Q_INVOKABLE void endDocumentReplacement();
signals:
    void documentChanged();
    void filePathChanged();
private:
    class Highlighter;
    void attachHighlighter();
    KSyntaxHighlighting::Repository m_repository;
    KSyntaxHighlighting::Definition m_definition;
    QPointer<QQuickTextDocument> m_document;
    QPointer<QTextDocument> m_textDocument;
    QMetaObject::Connection m_destroyed;
    QMetaObject::Connection m_textDestroyed;
    QString m_filePath;
    bool m_replacing = false;
    std::unique_ptr<Highlighter> m_highlighter;
};

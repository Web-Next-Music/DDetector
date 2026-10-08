#pragma once

#include <QPointer>
#include <QQuickTextDocument>
#include <QSyntaxHighlighter>

// Applies layout formats only; never replaces text or changes the editor model.
// Register with qmlRegisterType<SyntaxHighlighter>(uri, 1, 0, "SyntaxHighlighter").
class SyntaxHighlighter : public QSyntaxHighlighter {
    Q_OBJECT
    Q_PROPERTY(QQuickTextDocument *document READ quickDocument WRITE setQuickDocument NOTIFY documentChanged)
    Q_PROPERTY(QString filePath READ filePath WRITE setFilePath NOTIFY filePathChanged)
public:
    explicit SyntaxHighlighter(QObject *parent = nullptr);
    QQuickTextDocument *quickDocument() const { return m_document; }
    void setQuickDocument(QQuickTextDocument *document);
    QString filePath() const { return m_filePath; }
    void setFilePath(const QString &path);
signals:
    void documentChanged();
    void filePathChanged();
protected:
    void highlightBlock(const QString &text) override;
private:
    enum Language { Plain, Python, Json, Markdown, JavaScript, Yaml, Shell };
    QPointer<QQuickTextDocument> m_document;
    QMetaObject::Connection m_destroyed;
    QString m_filePath;
    Language m_language = Plain;
};

#include "syntaxhighlighter.h"

#include <QColor>
#include <QFileInfo>
#include <QUrl>
#include <KSyntaxHighlighting/Definition>
#include <KSyntaxHighlighting/Format>

SyntaxHighlighter::SyntaxHighlighter(QObject *parent) : KSyntaxHighlighting::SyntaxHighlighter(parent) {
    setTheme(m_repository.defaultTheme(KSyntaxHighlighting::Repository::DarkTheme));
}

void SyntaxHighlighter::setQuickDocument(QQuickTextDocument *document) {
    if (m_document == document) return;
    QObject::disconnect(m_destroyed);
    m_document = document;
    QSyntaxHighlighter::setDocument(document ? document->textDocument() : nullptr);
    if (document) {
        m_destroyed = connect(document, &QObject::destroyed, this, [this] {
            m_document = nullptr;
            QSyntaxHighlighter::setDocument(nullptr);
            emit documentChanged();
        });
    }
    emit documentChanged();
}

void SyntaxHighlighter::setFilePath(const QString &path) {
    if (m_filePath == path) return;
    m_filePath = path;
    const QUrl url(path);
    const QFileInfo info(url.isLocalFile() ? url.toLocalFile() : path);
    auto definition = m_repository.definitionForFileName(info.fileName());
    if (!definition.isValid()) definition = m_repository.definitionForFileName(info.fileName().toLower());
    if (!definition.isValid() && info.suffix().compare("mdown", Qt::CaseInsensitive) == 0)
        definition = m_repository.definitionForName("Markdown");
    setDefinition(definition);
    rehighlight();
    emit filePathChanged();
}

void SyntaxHighlighter::applyFormat(int offset, int length, const KSyntaxHighlighting::Format &format) {
    using T = KSyntaxHighlighting::Theme;
    QColor color;
    // Markdown expresses headings/code as Function/Information, and emphasis
    // as decorated Normal text. Keep the editor palette without parsing markup.
    if (definition().name() == "Markdown") {
        if (format.textStyle() == T::Function || format.isUnderline(theme())) color = QColor("#26a6b5");
        else if (format.textStyle() == T::Information) color = QColor("#a5d6a7");
        else if (format.textStyle() == T::Normal && format.isBold(theme())) color = QColor("#c792ea");
    }
    if (!color.isValid()) switch (format.textStyle()) {
    case T::Normal: return;
    case T::Comment: case T::Documentation: case T::CommentVar: color = QColor("#78909c"); break;
    case T::String: case T::VerbatimString: case T::SpecialString: case T::Char: case T::SpecialChar:
        color = QColor("#a5d6a7"); break;
    case T::DecVal: case T::BaseN: case T::Float: color = QColor("#80cbc4"); break;
    case T::Attribute: case T::Variable: color = QColor("#26a6b5"); break;
    default: color = QColor("#c792ea"); break;
    }
    setFormat(offset, length, color);
}

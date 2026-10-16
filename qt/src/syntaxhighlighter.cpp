#include "syntaxhighlighter.h"

#include <QColor>
#include <QFileInfo>
#include <QUrl>
#include <KSyntaxHighlighting/Definition>
#include <KSyntaxHighlighting/Format>
#include <KSyntaxHighlighting/SyntaxHighlighter>
#include <KSyntaxHighlighting/Theme>

class SyntaxHighlighter::Highlighter : public KSyntaxHighlighting::SyntaxHighlighter {
public:
    using KSyntaxHighlighting::SyntaxHighlighter::SyntaxHighlighter;
protected:
    void applyFormat(int offset, int length, const KSyntaxHighlighting::Format &format) override;
};

SyntaxHighlighter::SyntaxHighlighter(QObject *parent) : QObject(parent) {}

SyntaxHighlighter::~SyntaxHighlighter() = default;

void SyntaxHighlighter::beginDocumentReplacement() {
    m_replacing = true;
    // KF queues rehighlightBlock with QTextBlock values. Destroy the receiver
    // synchronously, before those blocks can be invalidated by setText/clear.
    m_highlighter.reset();
}

void SyntaxHighlighter::endDocumentReplacement() {
    m_replacing = false;
    attachHighlighter();
}

void SyntaxHighlighter::attachHighlighter() {
    if (m_replacing || m_highlighter || !m_textDocument) return;
    m_highlighter = std::make_unique<Highlighter>(this);
    m_highlighter->setTheme(m_repository.defaultTheme(KSyntaxHighlighting::Repository::DarkTheme));
    m_highlighter->setDefinition(m_definition);
    m_highlighter->setDocument(m_textDocument);
}

void SyntaxHighlighter::setDocument(QTextDocument *document) {
    if (m_textDocument == document) return;
    m_highlighter.reset();
    QObject::disconnect(m_textDestroyed);
    m_textDocument = document;
    if (document) {
        m_textDestroyed = connect(document, &QObject::destroyed, this, [this] {
            m_textDocument = nullptr;
            m_highlighter.reset();
        });
    }
    attachHighlighter();
}

void SyntaxHighlighter::rehighlight() {
    if (m_highlighter) m_highlighter->rehighlight();
}

void SyntaxHighlighter::setQuickDocument(QQuickTextDocument *document) {
    if (m_document == document) return;
    QObject::disconnect(m_destroyed);
    m_document = document;
    setDocument(document ? document->textDocument() : nullptr);
    if (document) {
        m_destroyed = connect(document, &QObject::destroyed, this, [this] {
            m_document = nullptr;
            setDocument(nullptr);
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
    if (m_definition != definition) {
        m_definition = definition;
        // KF's setDefinition already rehighlights when the definition changes.
        if (m_highlighter) m_highlighter->setDefinition(definition);
    }
    emit filePathChanged();
}

void SyntaxHighlighter::Highlighter::applyFormat(int offset, int length, const KSyntaxHighlighting::Format &format) {
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

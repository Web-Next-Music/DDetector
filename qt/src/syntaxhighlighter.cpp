#include "syntaxhighlighter.h"

#include <QColor>
#include <QFileInfo>
#include <QUrl>
#include <KSyntaxHighlighting/Definition>
#include <KSyntaxHighlighting/Format>
#include <KSyntaxHighlighting/AbstractHighlighter>
#include <KSyntaxHighlighting/State>
#include <QElapsedTimer>
#include <QTextBlock>
#include <QTextLayout>
#include <QTimer>
#include <vector>
#include <KSyntaxHighlighting/Theme>

class SyntaxHighlighter::Highlighter : public QObject, public KSyntaxHighlighting::AbstractHighlighter {
public:
    Highlighter(QObject *parent, QTextDocument *document) : QObject(parent), m_document(document) {
        // Ensure contentsChange is emitted even for standalone QTextDocuments.
        document->documentLayout();
        m_timer.setSingleShot(true);
        connect(&m_timer, &QTimer::timeout, this, [this] { process(false); });
        connect(document, &QTextDocument::contentsChange, this, [this](int position, int removed, int added) {
            if (!removed && !added) return;
            restart(m_document->findBlock(position).blockNumber());
        });
    }
    void restart(int block = 0) {
        // Only the prefix remains valid after edits, including block insertion/removal.
        m_states.resize(qMin(qMax(0, block), int(m_states.size())));
        m_timer.start(0);
    }
    void rehighlight() {
        m_timer.stop();
        m_states.clear();
        process(true);
    }
protected:
    void applyFormat(int offset, int length, const KSyntaxHighlighting::Format &format) override;
private:
    void process(bool synchronous) {
        QElapsedTimer elapsed;
        elapsed.start();
        auto block = m_document->findBlockByNumber(int(m_states.size()));
        const int start = block.isValid() ? block.position() : 0;
        int end = start;
        int count = 0;
        while (block.isValid()) {
            m_formats.clear();
            const auto previous = m_states.empty() ? KSyntaxHighlighting::State() : m_states.back();
            const QString text = block.text();
            m_states.push_back(highlightLine(QStringView(text), previous));
            block.layout()->setFormats(m_formats);
            end = block.position() + block.length();
            block = block.next();
            if (!synchronous && (++count >= 128 || elapsed.elapsed() >= 4)) break;
        }
        if (end > start) m_document->markContentsDirty(start, end - start);
        if (block.isValid()) m_timer.start(0);
    }
    QTextDocument *m_document;
    QTimer m_timer;
    std::vector<KSyntaxHighlighting::State> m_states;
    QList<QTextLayout::FormatRange> m_formats;
};

SyntaxHighlighter::SyntaxHighlighter(QObject *parent) : QObject(parent) {}

SyntaxHighlighter::~SyntaxHighlighter() = default;

void SyntaxHighlighter::beginDocumentReplacement() {
    m_replacing = true;
    // Destroy the timer and state cache before the buffer is invalidated.
    m_highlighter.reset();
}

void SyntaxHighlighter::endDocumentReplacement() {
    m_replacing = false;
    attachHighlighter();
}

void SyntaxHighlighter::attachHighlighter() {
    if (m_replacing || m_highlighter || !m_textDocument) return;
    m_highlighter = std::make_unique<Highlighter>(this, m_textDocument);
    m_highlighter->setTheme(m_repository.defaultTheme(KSyntaxHighlighting::Repository::DarkTheme));
    m_highlighter->setDefinition(m_definition);
    m_highlighter->restart();
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
        if (m_highlighter) {
            m_highlighter->setDefinition(definition);
            m_highlighter->restart();
        }
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
    if (length <= 0) return;
    if (!m_formats.isEmpty() && m_formats.back().start + m_formats.back().length == offset
        && m_formats.back().format.foreground().color() == color) {
        m_formats.back().length += length;
        return;
    }
    QTextLayout::FormatRange range;
    range.start = offset;
    range.length = length;
    range.format.setForeground(color);
    m_formats.push_back(range);
}

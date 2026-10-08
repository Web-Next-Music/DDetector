#include "syntaxhighlighter.h"

#include <QColor>
#include <QFileInfo>
#include <QRegularExpression>
#include <QUrl>

SyntaxHighlighter::SyntaxHighlighter(QObject *parent) : QSyntaxHighlighter(parent) {}

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
    const QString ext = info.suffix().toLower();
    const QString name = info.fileName().toLower();
    m_language = Plain;
    if (ext == "py" || ext == "pyw") m_language = Python;
    else if (ext == "json" || ext == "jsonc") m_language = Json;
    else if (ext == "md" || ext == "markdown" || ext == "mdown") m_language = Markdown;
    else if (QStringList{"js", "jsx", "mjs", "cjs", "ts", "tsx", "mts", "cts"}.contains(ext)) m_language = JavaScript;
    else if (ext == "yaml" || ext == "yml") m_language = Yaml;
    else if (QStringList{"sh", "bash", "zsh", "fish"}.contains(ext)
             || QStringList{".bashrc", ".zshrc", ".profile", ".bash_profile"}.contains(name)) m_language = Shell;
    rehighlight();
    emit filePathChanged();
}

void SyntaxHighlighter::highlightBlock(const QString &text) {
    setCurrentBlockState(0);
    if (m_language == Plain) return;
    const QColor keyword("#c792ea"), string("#a5d6a7"), comment("#78909c"), number("#80cbc4"), key("#26a6b5");
    auto rule = [this, &text](const QString &pattern, const QColor &color) {
        auto matches = QRegularExpression(pattern).globalMatch(text);
        while (matches.hasNext()) {
            const auto match = matches.next();
            setFormat(match.capturedStart(), match.capturedLength(), color);
        }
    };
    if (m_language == Markdown) {
        // Fenced blocks remain literal, including their contents across paragraphs.
        const QRegularExpression fence("^\\s*(`{3,}|~{3,})");
        const auto match = fence.match(text);
        const int previous = previousBlockState();
        if (previous == 6 || previous == 7) {
            setFormat(0, text.size(), string);
            const QChar marker = previous == 6 ? QChar('`') : QChar('~');

            if (!(match.hasMatch() && match.captured(1).front() == marker
                  && text.mid(match.capturedEnd()).trimmed().isEmpty())) setCurrentBlockState(previous);
            return;
        }
        if (match.hasMatch()) {
            setCurrentBlockState(match.captured(1).front() == '`' ? 6 : 7);
            setFormat(0, text.size(), string);
            return;
        }
        rule("^\\s{0,3}#{1,6}\\s.*$", key);
        rule("\\*\\*[^*]+\\*\\*|__[^_]+__", keyword);
        rule("\\[[^\\]]+\\]\\([^)]*\\)", key);
        rule("`[^`]+`", string);
        return;
    }
    rule("\\b(?:0[xX][0-9a-fA-F]+|\\d+(?:\\.\\d+)?(?:[eE][+-]?\\d+)?)\\b", number);
    QString words;
    switch (m_language) {
    case Python: words = "False None True and as assert async await break class continue def del elif else except finally for from global if import in is lambda nonlocal not or pass raise return try while with yield"; break;
    case JavaScript: words = "async await break case catch class const continue debugger default delete do else enum export extends false finally for from function if implements import in instanceof interface let new null of private protected public return static super switch this throw true try typeof undefined var void while with yield type"; break;
    case Json: words = "true false null"; break;
    case Yaml: words = "true false null yes no on off"; break;
    case Shell: words = "if then else elif fi for while do done case esac in function select until export local readonly return"; break;
    default: break;
    }
    rule("\\b(?:" + words.split(' ').join('|') + ")\\b", keyword);
    if (m_language == Yaml) rule("^\\s*(?:-\\s+)?[\\w.-]+(?=\\s*:)", key);
    if (m_language == Shell) rule("\\$[A-Za-z_][A-Za-z_0-9]*|\\$\\{[^}]*\\}", key);

    // Scan strings/comments after token rules so their contents take precedence.
    // States: 1 block comment, 2/3 Python triple strings, 4 JS template string.
    int state = previousBlockState();
    if (state < 1 || state > 4) state = 0;
    int i = 0;
    while (i < text.size()) {
        int start = i;
        QString end;
        QColor color = string;
        bool multiline = false;
        if (state) {
            end = state == 1 ? "*/" : state == 2 ? "'''" : state == 3 ? "\"\"\"" : "`";
            color = state == 1 ? comment : string;
            multiline = true;
        } else {
            const bool hashComment = m_language == Python || m_language == Yaml || m_language == Shell;
            const bool slashComment = m_language == JavaScript || (m_language == Json && QFileInfo(m_filePath).suffix().compare("jsonc", Qt::CaseInsensitive) == 0);
            if ((hashComment && text[i] == '#') || (slashComment && text.mid(i, 2) == "//")) {
                setFormat(i, text.size() - i, comment);
                break;
            }
            if (slashComment && text.mid(i, 2) == "/*") {
                state = 1; end = "*/"; color = comment; multiline = true; i += 2;
            } else if (m_language == Python && (text.mid(i, 3) == "'''" || text.mid(i, 3) == "\"\"\"")) {
                end = text.mid(i, 3); state = end == "'''" ? 2 : 3; multiline = true; i += 3;
            } else if (m_language == JavaScript && text[i] == '`') {
                state = 4; end = "`"; multiline = true; ++i;
            } else if (text[i] == '"' || (text[i] == '\'' && m_language != Json)) {
                end = text.mid(i, 1); ++i;
            } else { ++i; continue; }
        }
        bool closed = false;
        while (i < text.size()) {
            if (state != 1 && text[i] == '\\') { i += 2; continue; }
            if (text.mid(i, end.size()) == end) { i += end.size(); closed = true; break; }
            ++i;
        }
        i = qMin(i, int(text.size()));
        const QColor tokenColor = m_language == Json && closed && text.mid(i).trimmed().startsWith(':') ? key : color;
        setFormat(start, i - start, tokenColor);
        if (multiline && !closed) { setCurrentBlockState(state); break; }
        state = 0;
    }
    // Preserve continuation state across empty blocks too.
    if (text.isEmpty() && state) setCurrentBlockState(state);
}

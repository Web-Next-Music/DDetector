#ifndef Q_MOC_RUN
#include "syntaxhighlighter.h"
#endif

#include <QGuiApplication>
#include <QQmlComponent>
#include <QQmlEngine>
#include <QTextBlock>
#include <QTextCursor>
#include <QTextDocument>
#include <QTextLayout>
#include <QSyntaxHighlighter>
#include <QtTest>

// Standalone target: enable AUTOMOC, compile this file plus syntaxhighlighter.cpp/.h,
// and link Qt6::Quick, Qt6::Test and KF6::SyntaxHighlighting. Run with QT_QPA_PLATFORM=offscreen.
#ifdef Q_MOC_RUN
class SyntaxHighlighterTests : public QObject {
    Q_OBJECT
private slots:
    void preservesText_data();
    void preservesText();
    void multilinePrecedence();
    void quickDocumentBinding();
    void fenceLengths();
    void shellAndYamlQuoting();
    void semanticPalette();
    void queuedPropagation_data();
    void queuedPropagation();
};
#else
class SyntaxHighlighterTests : public QObject {
    Q_OBJECT
    static QColor colorAt(const QTextDocument &document, int blockNumber, int position) {
        for (const auto &range : document.findBlockByNumber(blockNumber).layout()->formats())
            if (range.start <= position && position < range.start + range.length)
                return range.format.foreground().color();
        return QColor();
    }
private slots:
    void preservesText_data() {
        QTest::addColumn<QString>("path");
        QTest::addColumn<QString>("text");
        QTest::newRow("kde-cpp") << "code.cpp" << "/* multi\nline */\nint main() { return 42; }\n";
        QTest::newRow("kde-go") << "code.go" << "package main\nfunc main() { println(42) }\n";
        QTest::newRow("kde-rust") << "code.rs" << "fn main() { let value = 42; }\n";
        QTest::newRow("python") << "code.py" << "def f():\n\tvalue = '''hello\n# still string\n'''\n    return True # café 😀\n";
        QTest::newRow("json") << "file:///tmp/data.json" << "{\n  \"key\": [true, null, 12.5, \"# string\"]\n}\n";
        QTest::newRow("markdown") << "readme.MD" << "# Heading\n**bold** [link](url)\n```js\nconst a = 1;\n```\n";
        QTest::newRow("javascript") << "code.js" << "/* comment\ncontinued */ const s = `hello\nworld`; // end\n";
        QTest::newRow("typescript") << "code.ts" << "interface Thing { value: string; }\nconst s = \"escaped \\\" quote\";\n";
        QTest::newRow("yaml") << "config.yaml" << "key: true\nlist:\n  - \"text # not comment\" # comment\n";
        QTest::newRow("shell") << ".bashrc" << "export PATH=\"/tmp/bin\"\nif true; then # comment\n echo ${PATH}\nfi\n";
        for (const auto &extension : {"jsx", "mjs", "cjs", "tsx", "mts", "cts"})
            QTest::newRow(extension) << QString("code.%1").arg(extension) << "const value = 42; // comment\n";
        for (const auto &extension : {"pyw", "jsonc", "yml", "bash", "zsh", "fish"})
            QTest::newRow(extension) << QString("code.%1").arg(extension) << "\"hello\"\n";
        QTest::newRow("markdown-extension") << "readme.markdown" << "# Heading\n";
        QTest::newRow("mdown") << "readme.mdown" << "# Heading\n";
        for (const auto &name : {".zshrc", ".profile", ".bash_profile"})
            QTest::newRow(name) << QString(name) << "echo \"hello\"\n";
    }
    void preservesText() {
        QFETCH(QString, path);
        QFETCH(QString, text);
        QTextDocument document;
        document.setPlainText(text);
        document.setModified(false);
        const QString before = document.toPlainText();
        const QByteArray bytes = before.toUtf8();
        SyntaxHighlighter highlighter;
        highlighter.setFilePath(path);
        highlighter.setDocument(&document);
        highlighter.rehighlight();
        QCOMPARE(document.toPlainText(), before);
        QCOMPARE(document.toPlainText().toUtf8(), bytes);
        QVERIFY(!document.isModified());
        bool formatted = false;
        for (auto block = document.begin(); block.isValid(); block = block.next())
            formatted |= !block.layout()->formats().isEmpty();
        QVERIFY(formatted);
        highlighter.setFilePath("unknown.txt");
        for (auto block = document.begin(); block.isValid(); block = block.next())
            QVERIFY(block.layout()->formats().isEmpty());
        QCOMPARE(document.toPlainText(), before);
    }
    void multilinePrecedence() {
        QTextDocument document("/* comment\nconst false 123\n*/ const value = true;");
        SyntaxHighlighter highlighter;
        highlighter.setFilePath("test.ts");
        highlighter.setDocument(&document);
        highlighter.rehighlight();
        const auto ranges = document.findBlockByNumber(1).layout()->formats();
        QCOMPARE(ranges.size(), 1);
        QCOMPARE(ranges.front().format.foreground().color(), QColor("#78909c"));
        QTextCursor cursor(document.begin());
        cursor.select(QTextCursor::BlockUnderCursor);
        cursor.insertText("const x = 1;");
        highlighter.rehighlight();
        // KDE owns block-state IDs; assert the visible contract, not its private encoding.
        bool comment = false;
        for (const auto &range : document.findBlockByNumber(1).layout()->formats())
            comment |= range.format.foreground().color() == QColor("#78909c");
        QVERIFY(!comment);

    }
    void fenceLengths() {
        QTextDocument document("````\n```\n# still literal\n````\n# heading\n~~~~\n~~~\n~~~~~\n# heading again\n");
        SyntaxHighlighter highlighter; highlighter.setFilePath("README.md");
        highlighter.setDocument(&document); highlighter.rehighlight();
        // Inspect output, not the library's opaque state encoding. Shorter
        // delimiters must not close a fence; longer closing delimiters may.
        QCOMPARE(colorAt(document, 2, 2), QColor("#a5d6a7"));
        QVERIFY(colorAt(document, 4, 2) != QColor("#a5d6a7"));
        QCOMPARE(colorAt(document, 6, 1), QColor("#a5d6a7"));
        QVERIFY(colorAt(document, 8, 2) != QColor("#a5d6a7"));
    }
    void shellAndYamlQuoting() {
        SyntaxHighlighter highlighter;
        QTextDocument document("echo 'first\n# still string\\\nlast' # comment\necho foo#bar\n");
        const auto original = document.toPlainText();
        highlighter.setFilePath("test.sh"); highlighter.setDocument(&document); highlighter.rehighlight();
        QCOMPARE(colorAt(document, 2, 9), QColor("#78909c"));
        QVERIFY(colorAt(document, 3, 8) != QColor("#78909c"));
        const auto ranges = document.findBlockByNumber(1).layout()->formats();
        QCOMPARE(ranges.size(), 1); QCOMPARE(ranges[0].format.foreground().color(), QColor("#a5d6a7"));
        QCOMPARE(document.toPlainText(), original);
        highlighter.setFilePath("test.yaml");
        highlighter.beginDocumentReplacement();
        document.setPlainText("key: foo#bar\nkey: 'a\\' # comment\nkey: 'a''#b' # comment\n"); highlighter.endDocumentReplacement(); highlighter.rehighlight();
        QVERIFY(colorAt(document, 0, 8) != QColor("#78909c"));
        QCOMPARE(colorAt(document, 1, 10), QColor("#78909c"));
        QCOMPARE(colorAt(document, 2, 9), QColor("#a5d6a7"));
        QCOMPARE(colorAt(document, 2, 13), QColor("#78909c"));
    }
    void semanticPalette() {
        SyntaxHighlighter highlighter;
        QTextDocument document("const value = 42; // comment\nconst text = \"hello\";");
        highlighter.setFilePath("test.js");
        highlighter.setDocument(&document);
        highlighter.rehighlight();
        QCOMPARE(colorAt(document, 0, 0), QColor("#c792ea"));
        QCOMPARE(colorAt(document, 0, 14), QColor("#80cbc4"));
        QCOMPARE(colorAt(document, 0, 20), QColor("#78909c"));
        QCOMPARE(colorAt(document, 1, 15), QColor("#a5d6a7"));
        highlighter.setFilePath("test.sh");
        highlighter.beginDocumentReplacement();
        document.setPlainText("echo ${PATH}");
        highlighter.endDocumentReplacement();
        highlighter.rehighlight();
        QCOMPARE(colorAt(document, 0, 8), QColor("#26a6b5"));
        highlighter.setFilePath("test.md");
        highlighter.beginDocumentReplacement();
        document.setPlainText("# Heading\n**bold** [link](url) `code`\n```js\nconst value = 42;\n```\n# Heading again");
        highlighter.endDocumentReplacement();
        highlighter.rehighlight();
        QCOMPARE(colorAt(document, 0, 2), QColor("#26a6b5"));
        QCOMPARE(colorAt(document, 1, 2), QColor("#c792ea"));
        QCOMPARE(colorAt(document, 1, 11), QColor("#26a6b5"));
        QCOMPARE(colorAt(document, 1, 22), QColor("#a5d6a7"));
        // Language-tagged fences now use the embedded language's categories.
        QCOMPARE(colorAt(document, 3, 0), QColor("#c792ea"));
        QCOMPARE(colorAt(document, 3, 14), QColor("#80cbc4"));
        QCOMPARE(colorAt(document, 5, 2), QColor("#26a6b5"));
    }
    void queuedPropagation_data() {
        QTest::addColumn<QString>("operation");
        for (const auto *operation : {"replace", "discard", "switch", "destroy-document", "destroy-wrapper"})
            QTest::newRow(operation) << QString(operation);
    }
    void queuedPropagation() {
        QFETCH(QString, operation);
        auto document = std::make_unique<QTextDocument>("/* comment\nstill comment\n*/\nint value = 42;\n");
        auto highlighter = std::make_unique<SyntaxHighlighter>();
        highlighter->setFilePath("test.cpp");
        highlighter->setDocument(document.get());
        highlighter->rehighlight();
        QCoreApplication::sendPostedEvents(nullptr, QEvent::MetaCall);
        QPointer<QSyntaxHighlighter> receiver = highlighter->findChild<QSyntaxHighlighter *>();
        QVERIFY(receiver);
        // Reproduce KF's queued propagation argument explicitly, without depending
        // on private state IDs or whether this library version propagates eagerly.
        QVERIFY(QMetaObject::invokeMethod(receiver, "rehighlightBlock", Qt::QueuedConnection,
                                          Q_ARG(QTextBlock, document->begin().next())));
        bool staleCallDelivered = false;
        QMetaObject::invokeMethod(receiver, [&] { staleCallDelivered = true; }, Qt::QueuedConnection);
        QTextDocument replacement("int fresh = 42;");
        if (operation == "destroy-document") {
            document.reset();
        } else if (operation == "destroy-wrapper") {
            highlighter.reset();
        } else if (operation == "switch") {
            highlighter->setDocument(&replacement);
            document.reset();
        } else {
            highlighter->beginDocumentReplacement();
            QVERIFY(!receiver);
            document->setPlainText(operation == "discard" ? "/* restored */" : "int fresh = 42;");
            highlighter->endDocumentReplacement();
        }
        QVERIFY(!receiver);
        QCoreApplication::sendPostedEvents(nullptr, QEvent::MetaCall);
        QVERIFY(!staleCallDelivered);
        if (highlighter) {
            if (!document && operation == "destroy-document") highlighter->setDocument(&replacement);
            highlighter->rehighlight();
            const auto &current = document ? *document : replacement;
            QVERIFY(!current.begin().layout()->formats().isEmpty());
            QCOMPARE(colorAt(current, 0, 0), QColor(operation == "discard" ? "#78909c" : "#c792ea"));
        }
    }
    void quickDocumentBinding() {
        qmlRegisterType<SyntaxHighlighter>("HighlighterTests", 1, 0, "SyntaxHighlighter");
        QQmlEngine engine;
        QQmlComponent component(&engine);
        component.setData(R"(
            import QtQuick
            import HighlighterTests 1.0
            TextEdit {
                id: editor
                textFormat: TextEdit.PlainText
                text: "const value = true; // comment"
                SyntaxHighlighter {
                    objectName: "highlighter"
                    document: editor.textDocument
                    filePath: "test.js"
                }
            }
        )", QUrl());
        QScopedPointer<QObject> editor(component.create());
        QVERIFY2(editor, qPrintable(component.errorString()));
        auto *highlighter = editor->findChild<SyntaxHighlighter *>("highlighter");
        QVERIFY(highlighter);
        QVERIFY(highlighter->quickDocument());
        auto *document = highlighter->quickDocument()->textDocument();
        const QString before = document->toPlainText();
        highlighter->rehighlight();
        QVERIFY(!document->begin().layout()->formats().isEmpty());
        QCOMPARE(document->toPlainText(), before);
        QCOMPARE(editor->property("text").toString(), before);
        highlighter->setQuickDocument(nullptr);
        QVERIFY(!highlighter->quickDocument());
        QCOMPARE(document->toPlainText(), before);
    }
};

#endif

QTEST_MAIN(SyntaxHighlighterTests)
#include "syntaxhighlighter_tests.moc"

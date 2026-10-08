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
#include <QtTest>

// Standalone target: enable AUTOMOC, compile this file plus syntaxhighlighter.cpp/.h,
// and link Qt6::Quick and Qt6::Test. Run with QT_QPA_PLATFORM=offscreen.
#ifdef Q_MOC_RUN
class SyntaxHighlighterTests : public QObject {
    Q_OBJECT
private slots:
    void preservesText_data();
    void preservesText();
    void multilinePrecedence();
    void quickDocumentBinding();
};
#else
class SyntaxHighlighterTests : public QObject {
    Q_OBJECT
private slots:
    void preservesText_data() {
        QTest::addColumn<QString>("path");
        QTest::addColumn<QString>("text");
        QTest::newRow("python") << "code.py" << "def f():\n\tvalue = '''hello\n# still string\n'''\n    return True # café 😀\n";
        QTest::newRow("json") << "file:///tmp/data.json" << "{\n  \"key\": [true, null, 12.5, \"# string\"]\n}\n";
        QTest::newRow("markdown") << "readme.MD" << "# Heading\n**bold** [link](url)\n```js\nconst a = 1;\n```\n";
        QTest::newRow("javascript") << "code.js" << "/* comment\ncontinued */ const s = `hello\nworld`; // end\n";
        QTest::newRow("typescript") << "code.ts" << "interface Thing { value: string; }\nconst s = \"escaped \\\" quote\";\n";
        QTest::newRow("yaml") << "config.yaml" << "key: true\nlist:\n  - \"text # not comment\" # comment\n";
        QTest::newRow("shell") << ".bashrc" << "export PATH=\"/tmp/bin\"\nif true; then # comment\n echo ${PATH}\nfi\n";
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
        highlighter.QSyntaxHighlighter::setDocument(&document);
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
        highlighter.QSyntaxHighlighter::setDocument(&document);
        highlighter.rehighlight();
        const auto ranges = document.findBlockByNumber(1).layout()->formats();
        QCOMPARE(ranges.size(), 1);
        QCOMPARE(ranges.front().format.foreground().color(), QColor("#78909c"));
        QTextCursor cursor(document.begin());
        cursor.select(QTextCursor::BlockUnderCursor);
        cursor.insertText("const x = 1;");
        highlighter.rehighlight();
        QCOMPARE(document.findBlockByNumber(1).userState(), 0);
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

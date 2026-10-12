#include "lyricslayout.h"

#include <QAbstractTextDocumentLayout>
#include <QTextDocument>
#include <memory>

QVariantList LyricsLayout::measure(QQuickTextDocument *reference,
                                  const QVariantList &lines, qreal width,
                                  const QColor &markedColor) const
{
    QVariantList offsets{0.0};
    if (!reference || !reference->textDocument()) return offsets;
    // QQuickTextEdit configures margin, font, wrap policy, and native font metrics.
    // Clone those settings rather than independently approximating its geometry.
    std::unique_ptr<QTextDocument> document(reference->textDocument()->clone());
    document->setUndoRedoEnabled(false);
    // QQuickTextEdit exposes the inner content box, excluding document margins.
    const qreal margins = 2 * document->documentMargin();
    document->setTextWidth(qMax<qreal>(0, width) + margins);
    offsets.reserve(lines.size() + 1);
    const QString prefix = QStringLiteral("<span style='color:%1;font-weight:600'>")
                               .arg(markedColor.name());
    qreal total = 0;
    for (const QVariant &line : lines) {
        QString html = QStringLiteral("<p style='margin:0;line-height:130%'>");
        const QVariantList parts = line.toMap().value(QStringLiteral("parts")).toList();
        for (const QVariant &part : parts) {
            const QVariantMap data = part.toMap();
            QString text = data.value(QStringLiteral("text")).toString().toHtmlEscaped();
            text.replace(QLatin1Char('\n'), QStringLiteral("<br>"));
            if (data.value(QStringLiteral("marked")).toBool())
                html += prefix + text + QStringLiteral("</span>");
            else
                html += text;
        }
        html += QStringLiteral("</p>");
        document->setHtml(html);
        total += qMax<qreal>(19, document->documentLayout()->documentSize().height() - margins) + 8;
        offsets.append(total);
    }
    return offsets;
}

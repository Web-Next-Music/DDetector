#pragma once

#include <QObject>
#include <QColor>
#include <QQuickTextDocument>
#include <QVariantList>

// Register as LyricsLayout in DDetector.Lyrics 1.0 in application/test hosts.
// Uses one native document cloned from the actual renderer's layout settings.
class LyricsLayout : public QObject {
    Q_OBJECT
public:
    explicit LyricsLayout(QObject *parent = nullptr) : QObject(parent) {}
    Q_INVOKABLE QVariantList measure(QQuickTextDocument *reference,
                                    const QVariantList &lines, qreal width,
                                    const QColor &markedColor) const;
};

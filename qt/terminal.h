#pragma once

#include "terminalsession.h"
#include <QQuickPaintedItem>
#include <QTimer>
#include <QImage>

// QWidget emulator rendered into the Qt Quick scene; no native child window.
// One view per session. Widgets are rasterized on the GUI thread; paint only
// consumes the cached image while Qt Quick synchronizes GUI/render threads.
class EmbeddedTerminal : public QQuickPaintedItem {
    Q_OBJECT
    Q_PROPERTY(TerminalSession *session READ session WRITE setSession NOTIFY sessionChanged)
public:
    explicit EmbeddedTerminal(QQuickItem *parent = nullptr);
    TerminalSession *session() const { return m_session; }
    void setSession(TerminalSession *session);
    void paint(QPainter *painter) override;
    Q_INVOKABLE void copy();
    Q_INVOKABLE void paste();
signals:
    void sessionChanged();
protected:
    void geometryChange(const QRectF &newGeometry, const QRectF &oldGeometry) override;
    void keyPressEvent(QKeyEvent *event) override;
    void keyReleaseEvent(QKeyEvent *event) override;
    void mousePressEvent(QMouseEvent *event) override;
    void mouseMoveEvent(QMouseEvent *event) override;
    void mouseReleaseEvent(QMouseEvent *event) override;
    void mouseDoubleClickEvent(QMouseEvent *event) override;
    void wheelEvent(QWheelEvent *event) override;
    void focusInEvent(QFocusEvent *event) override;
    void focusOutEvent(QFocusEvent *event) override;
    void inputMethodEvent(QInputMethodEvent *event) override;
    QVariant inputMethodQuery(Qt::InputMethodQuery query) const override;
private:
    void capture();
    void resizeTerminal();
    void forwardMouse(QMouseEvent *event);
    QPointer<TerminalSession> m_session;
    QPointer<QWidget> m_mouseTarget;
    QTimer m_repaint;
    QImage m_frame;
};

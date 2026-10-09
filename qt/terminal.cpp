#include "terminal.h"

#include <QApplication>

#include <QInputMethodEvent>
#include <QPainter>
#include <QWidget>

#include <QQuickWindow>
#ifndef Q_OS_WIN
#include <qtermwidget.h>
#endif

EmbeddedTerminal::EmbeddedTerminal(QQuickItem *parent) : QQuickPaintedItem(parent) {
    setAcceptedMouseButtons(Qt::AllButtons);
    setFlag(ItemIsFocusScope);
    setFlag(ItemAcceptsInputMethod);
    setClip(true);
    // Widget cursor blinking/selection updates do not emit emulator output.
    // Limit uploads to 30fps and stop them when hidden; QML retains the PTY.
    m_repaint.setInterval(33);
    connect(&m_repaint, &QTimer::timeout, this, &EmbeddedTerminal::capture);
    connect(this, &QQuickItem::visibleChanged, this, [this] {
        markDirty();
    });
}

void EmbeddedTerminal::setSession(TerminalSession *session) {
    if (m_session == session) return;
    if (m_session) disconnect(m_session, nullptr, this, nullptr);
    m_mouseTarget.clear();
    m_session = session;
    if (session) {
        connect(session, &TerminalSession::widgetChanged, this, [this] {
            m_mouseTarget.clear(); watchWidget(); resizeTerminal(); markDirty(); capture();
        });
        connect(session, &TerminalSession::output, this, [this](const QString &) { markDirty(); });
        connect(session, &QObject::destroyed, this, [this] {
            m_session = nullptr; m_mouseTarget.clear(); m_repaint.stop();
            m_frame = {}; emit sessionChanged(); update();
        });
    }
    watchWidget(); resizeTerminal(); markDirty();
    emit sessionChanged(); capture();
}

void EmbeddedTerminal::resizeTerminal() {
#ifndef Q_OS_WIN
    if (m_session && m_session->widget() && width() > 0 && height() > 0)
        m_session->widget()->resize(qMax(1, qRound(width())), qMax(1, qRound(height())));
#endif
}

void EmbeddedTerminal::geometryChange(const QRectF &now, const QRectF &before) {
    QQuickPaintedItem::geometryChange(now, before);
    resizeTerminal(); markDirty();
}

void EmbeddedTerminal::watchWidget() {
    if (m_watchedWidget) {
        m_watchedWidget->removeEventFilter(this);
        for (auto *child : m_watchedWidget->findChildren<QWidget *>()) child->removeEventFilter(this);
    }
    m_watchedWidget.clear();
#ifndef Q_OS_WIN
    if (m_session) m_watchedWidget = m_session->widget();
#endif
    if (m_watchedWidget) {
        m_watchedWidget->installEventFilter(this);
        for (auto *child : m_watchedWidget->findChildren<QWidget *>()) child->installEventFilter(this);
    }
}
void EmbeddedTerminal::markDirty() {
    m_dirty = true;
    if (isVisible() && m_watchedWidget) {
        if (!m_repaint.isActive()) m_repaint.start();
    } else m_repaint.stop();
}
bool EmbeddedTerminal::eventFilter(QObject *watched, QEvent *event) {
    if (event->type() == QEvent::ChildAdded) {
        QTimer::singleShot(0, this, [this] { watchWidget(); markDirty(); });
    }
    if (!m_capturing) {
        switch (event->type()) {
        case QEvent::Paint: case QEvent::UpdateRequest: case QEvent::Resize:
        case QEvent::PaletteChange: case QEvent::FontChange: case QEvent::StyleChange:
        case QEvent::FocusIn: case QEvent::FocusOut:
            markDirty(); break;
        default: break;
        }
    }
    return QQuickPaintedItem::eventFilter(watched, event);
}
void EmbeddedTerminal::capture() {
#ifndef Q_OS_WIN
    if (m_session && m_session->widget() && isVisible()) {
        QWidget *widget = m_session->widget();
        const qreal dpr = window() ? window()->devicePixelRatio() : 1.0;
        const QSize pixels = widget->size() * dpr;
        if (m_frame.size() != pixels || m_frame.devicePixelRatio() != dpr) {
            m_frame = QImage(pixels, QImage::Format_ARGB32_Premultiplied);
            m_frame.setDevicePixelRatio(dpr); m_dirty = true;
        }
        if (!m_dirty || m_frame.isNull()) return;
        m_dirty = false; m_capturing = true;
        m_frame.fill(Qt::transparent);
        widget->render(&m_frame);
        m_capturing = false;
        ++m_captureCount;
    } else {
        if (m_frame.isNull()) return;
        m_frame = {};
    }
#endif
    update();
}

void EmbeddedTerminal::paint(QPainter *painter) {
    painter->fillRect(boundingRect(), QColor("#101014"));
    if (!m_frame.isNull()) painter->drawImage(QPointF(0, 0), m_frame);
}

void EmbeddedTerminal::keyPressEvent(QKeyEvent *event) {
#ifndef Q_OS_WIN
    if (m_session && m_session->widget()) {
        if (event->modifiers() == (Qt::ControlModifier | Qt::ShiftModifier)) {
            if (event->key() == Qt::Key_C) { copy(); event->accept(); return; }
            if (event->key() == Qt::Key_V) { paste(); event->accept(); return; }
        }
        m_session->widget()->sendKeyEvent(event);
        event->accept(); return;
    }
#endif
    event->ignore();
}

void EmbeddedTerminal::keyReleaseEvent(QKeyEvent *event) { event->accept(); }

void EmbeddedTerminal::forwardMouse(QMouseEvent *event) {
#ifndef Q_OS_WIN
    if (m_session && m_session->widget()) {
        QWidget *widget = m_session->widget();
        if (!m_mouseTarget || event->type() == QEvent::MouseButtonPress
            || event->type() == QEvent::MouseButtonDblClick)
            m_mouseTarget = widget->childAt(event->position().toPoint());
        QWidget *target = m_mouseTarget ? m_mouseTarget.data() : widget;
        const QPointF point = target->mapFrom(widget, event->position());
        QMouseEvent forwarded(event->type(), point, point, target->mapToGlobal(point),
                              event->button(), event->buttons(), event->modifiers());
        QApplication::sendEvent(target, &forwarded);
        if (event->type() == QEvent::MouseButtonRelease) m_mouseTarget.clear();
        event->accept(); return;
    }
#endif
    event->ignore();
}
void EmbeddedTerminal::mousePressEvent(QMouseEvent *event) { forceActiveFocus(); forwardMouse(event); }
void EmbeddedTerminal::mouseMoveEvent(QMouseEvent *event) { forwardMouse(event); }
void EmbeddedTerminal::mouseReleaseEvent(QMouseEvent *event) { forwardMouse(event); }
void EmbeddedTerminal::mouseDoubleClickEvent(QMouseEvent *event) { forwardMouse(event); }

void EmbeddedTerminal::wheelEvent(QWheelEvent *event) {
#ifndef Q_OS_WIN
    if (m_session && m_session->widget()) {
        QWidget *widget = m_session->widget();
        QWidget *target = widget->childAt(event->position().toPoint());
        if (!target) target = widget;
        const QPointF point = target->mapFrom(widget, event->position());
        QWheelEvent forwarded(point, target->mapToGlobal(point), event->pixelDelta(),
                              event->angleDelta(), event->buttons(), event->modifiers(),
                              event->phase(), event->inverted());
        QApplication::sendEvent(target, &forwarded);
        event->accept(); return;
    }
#endif
    event->ignore();
}

void EmbeddedTerminal::focusInEvent(QFocusEvent *event) {
    QQuickPaintedItem::focusInEvent(event);
#ifndef Q_OS_WIN
    if (m_session && m_session->widget()) {
        QWidget *widget = m_session->widget()->focusProxy();
        if (widget) QApplication::sendEvent(widget, event);
    }
#endif
}
void EmbeddedTerminal::focusOutEvent(QFocusEvent *event) {
    QQuickPaintedItem::focusOutEvent(event);
#ifndef Q_OS_WIN
    if (m_session && m_session->widget()) {
        QWidget *widget = m_session->widget()->focusProxy();
        if (widget) QApplication::sendEvent(widget, event);
    }
#endif
}

void EmbeddedTerminal::inputMethodEvent(QInputMethodEvent *event) {
#ifndef Q_OS_WIN
    if (m_session && m_session->widget()) {
        QWidget *widget = m_session->widget()->focusProxy();
        if (widget) { QApplication::sendEvent(widget, event); event->accept(); return; }
    }
#endif
    event->ignore();
}
QVariant EmbeddedTerminal::inputMethodQuery(Qt::InputMethodQuery query) const {
#ifndef Q_OS_WIN
    if (m_session && m_session->widget()) {
        QWidget *widget = m_session->widget()->focusProxy();
        if (widget) return widget->inputMethodQuery(query);
    }
#endif
    return QQuickPaintedItem::inputMethodQuery(query);
}
void EmbeddedTerminal::copy() {
#ifndef Q_OS_WIN
    if (m_session && m_session->widget()) m_session->widget()->copyClipboard();
#endif
}
void EmbeddedTerminal::paste() {
#ifndef Q_OS_WIN
    // QTermWidget applies bracketed paste according to the emulator's current mode.
    if (m_session && m_session->running()) m_session->widget()->pasteClipboard();
#endif
}

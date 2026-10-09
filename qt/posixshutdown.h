#pragma once

#include <QCoreApplication>
#include <QObject>
#include <QSocketNotifier>
#include <QString>

#ifdef Q_OS_UNIX
#include <atomic>
#include <cerrno>
#include <csignal>
#include <fcntl.h>
#include <unistd.h>
#endif

// No Qt, allocation, logging or filesystem persistence in the signal handler.
// The notifier translates SIGINT/SIGTERM into normal event-loop shutdown so
// aboutToQuit handlers and stack-owned backends can finish their cleanup.
class PosixShutdown final : public QObject {
public:
    explicit PosixShutdown(QObject *parent = nullptr) : QObject(parent) {
#ifdef Q_OS_UNIX
        if (s_handlerWriteFd.load(std::memory_order_acquire) >= 0) {
            m_error = QStringLiteral("A POSIX shutdown handler is already installed.");
            return;
        }
        if (!ensurePipe()) {
            m_error = QStringLiteral("Cannot create POSIX shutdown pipe (errno %1).").arg(errno);
            return;
        }
        drainPipe();
        struct sigaction action {};
        action.sa_handler = &PosixShutdown::handleSignal;
        sigemptyset(&action.sa_mask);
        sigaddset(&action.sa_mask, SIGINT);
        sigaddset(&action.sa_mask, SIGTERM);
        action.sa_flags = SA_RESTART;
        s_handlerWriteFd.store(s_pipeWriteFd, std::memory_order_release);
        if (sigaction(SIGINT, &action, &m_oldInterrupt) != 0) {
            s_handlerWriteFd.store(-1, std::memory_order_release);
            m_error = QStringLiteral("Cannot install SIGINT handler (errno %1).").arg(errno);
            return;
        }
        if (sigaction(SIGTERM, &action, &m_oldTerminate) != 0) {
            const int error = errno;
            s_handlerWriteFd.store(-1, std::memory_order_release);
            sigaction(SIGINT, &m_oldInterrupt, nullptr);
            m_error = QStringLiteral("Cannot install SIGTERM handler (errno %1).").arg(error);
            return;
        }
        m_notifier = new QSocketNotifier(s_pipeReadFd, QSocketNotifier::Read, this);
        connect(m_notifier, &QSocketNotifier::activated, this, [this] {
            m_notifier->setEnabled(false);
            drainPipe();
            m_requested = true;
            QCoreApplication::quit();
        });
#endif
        m_installed = true;
    }
    ~PosixShutdown() override {
#ifdef Q_OS_UNIX
        if (!m_installed) return;
        m_notifier->setEnabled(false);
        s_handlerWriteFd.store(-1, std::memory_order_release);
        sigaction(SIGINT, &m_oldInterrupt, nullptr);
        sigaction(SIGTERM, &m_oldTerminate, nullptr);
#endif
    }
    bool isInstalled() const { return m_installed; }
    bool wasRequested() const { return m_requested; }
    QString error() const { return m_error; }

private:
    bool m_installed = false, m_requested = false;
    QString m_error;
#ifdef Q_OS_UNIX
    static_assert(std::atomic<int>::is_always_lock_free,
                  "Signal delivery requires a lock-free descriptor atomic.");
    inline static std::atomic<int> s_handlerWriteFd{-1};
    // These two nonblocking, CLOEXEC descriptors are process-owned, not
    // instance-owned. Keep them alive until process exit: a handler already
    // running on another thread must never write to a closed/reused descriptor
    // or to a pipe whose read end has closed (SIGPIPE). Reinstallation reuses
    // the same pair, so lifetime safety doesn't leak descriptors per instance.
    inline static int s_pipeReadFd = -1, s_pipeWriteFd = -1;
    QSocketNotifier *m_notifier = nullptr;
    struct sigaction m_oldInterrupt {}, m_oldTerminate {};

    static void handleSignal(int) noexcept {
        const int savedErrno = errno;
        const int fd = s_handlerWriteFd.load(std::memory_order_acquire);
        if (fd >= 0) {
            const char wakeup = 1;
            ssize_t result;
            do { result = ::write(fd, &wakeup, 1); } while (result < 0 && errno == EINTR);
            // EAGAIN means a wakeup is already buffered. Never block a handler.
        }
        errno = savedErrno;
    }
    static bool ensurePipe() {
        if (s_pipeReadFd >= 0) return true;
        int descriptors[2];
        if (::pipe(descriptors) != 0) return false;
        for (const int fd : descriptors) {
            const int flags = ::fcntl(fd, F_GETFL);
            const int descriptorFlags = ::fcntl(fd, F_GETFD);
            if (flags < 0 || descriptorFlags < 0
                || ::fcntl(fd, F_SETFL, flags | O_NONBLOCK) != 0
                || ::fcntl(fd, F_SETFD, descriptorFlags | FD_CLOEXEC) != 0) {
                const int error = errno;
                ::close(descriptors[0]); ::close(descriptors[1]);
                errno = error;
                return false;
            }
        }
        s_pipeReadFd = descriptors[0]; s_pipeWriteFd = descriptors[1];
        return true;
    }
    static void drainPipe() {
        char bytes[256];
        for (;;) {
            const ssize_t count = ::read(s_pipeReadFd, bytes, sizeof(bytes));
            if (count > 0 || (count < 0 && errno == EINTR)) continue;
            break;
        }
    }
#endif
};

#pragma once
#include <QQuickAsyncImageProvider>
#include <QThreadPool>
#include <QUrl>
#include <atomic>

class CoverImageProvider final : public QQuickAsyncImageProvider {
public:
    CoverImageProvider();
    ~CoverImageProvider() override;
    QQuickImageResponse *requestImageResponse(const QString &id, const QSize &requestedSize) override;
    static bool isSafeUrl(const QUrl &url);
private:
    QThreadPool pool;
    std::atomic_int pending{0};
};

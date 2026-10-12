#pragma once
#include <QQuickAsyncImageProvider>
#include <QThreadPool>
#include <QUrl>
#include <memory>
#include <QImage>
#include <functional>

class CoverImageProvider final : public QQuickAsyncImageProvider {
public:
    CoverImageProvider();
    ~CoverImageProvider() override;
    QQuickImageResponse *requestImageResponse(const QString &id, const QSize &requestedSize) override;
    static bool isSafeUrl(const QUrl &url);
private:
    friend class CoverImageProviderTests;
    static void store(const QString &directory, const QString &path, const QByteArray &bytes);
    static QImage decode(const QByteArray &bytes, const QSize &requested);
    static QByteArray download(const QUrl &url, const std::function<bool()> &canceled);
    struct State;
    std::shared_ptr<State> state;
    QThreadPool pool;
};

#include "bridge.h"
#include <QClipboard>


#include <QSet>
#include <QCollator>
#include <QDateTime>
#include <QDesktopServices>
#include <QGuiApplication>
#include <QJsonDocument>
#include <QNetworkReply>

#include <QUrlQuery>
#include <algorithm>
#include <QRegularExpression>
#include <memory>

static QString trackId(const QJsonObject &row) {
    const auto id = row.value("id");
    if (id.isString()) {
        static const QRegularExpression valid("\\A[0-9]{1,32}\\z");
        const QString value = id.toString();
        static const QRegularExpression nonzero("[1-9]");
        if (valid.match(value).hasMatch() && value.contains(nonzero)) return value;
        return {};
    }
    const qint64 number = id.toInteger(-1);
    return id.isDouble() && number > 0 && id.toDouble() == double(number) ? QString::number(number) : QString();
}
QVariant TrackModel::data(const QModelIndex &index, int role) const {
    if (!index.isValid() || index.model() != this || index.column() != 0 || index.row() < 0
        || index.row() >= m_rows.size() || role != Qt::UserRole) return {};
    if (const auto cached = m_dataCache.object(index.row())) return *cached;
    auto value = m_rows[index.row()].toVariantMap();
    value["id"] = trackId(m_rows[index.row()]);
    m_dataCache.insert(index.row(), new QVariantMap(value));
    return value;
}
int TrackModel::indexOf(const QString &id) const { return m_indexes.value(id, -1); }
void TrackModel::rebuildIndexes() {
    // Row-keyed conversions cannot survive structural changes or sorting.
    m_dataCache.clear();
    m_indexes.clear();
    for (int i = 0; i < m_rows.size(); ++i) m_indexes.insert(trackId(m_rows[i]), i);
}
void TrackModel::sync(const QList<QJsonObject> &input) {
    QList<QJsonObject> rows;
    QSet<QString> ids;
    for (const auto &row : input) {
        const QString id = trackId(row);
        // First valid occurrence wins, including numeric/string duplicate IDs.
        if (id.isEmpty() || ids.contains(id)) continue;
        ids.insert(id); rows.append(row);
    }
    const int oldCount = count();
    int membershipChanges = 0;
    for (auto it = m_indexes.cbegin(); it != m_indexes.cend(); ++it)
        if (!ids.contains(it.key())) ++membershipChanges;
    for (const auto &row : rows) if (!m_indexes.contains(trackId(row))) ++membershipChanges;
    if (membershipChanges > 256) {
        beginResetModel(); m_rows = rows; rebuildIndexes(); endResetModel();
    } else {
        for (int i = m_rows.size() - 1; i >= 0; --i) {
            if (!ids.contains(trackId(m_rows[i]))) {
                beginRemoveRows({}, i, i); m_rows.removeAt(i); rebuildIndexes(); endRemoveRows();
            }
        }
        QList<QJsonObject> added;
        for (const auto &row : rows) if (!m_indexes.contains(trackId(row))) added.append(row);
        if (!added.isEmpty()) {
            const int start = m_rows.size();
            beginInsertRows({}, start, start + added.size() - 1);
            m_rows.append(added); rebuildIndexes(); endInsertRows();
        }
        bool reordered = false;
        for (int i = 0; i < rows.size(); ++i)
            if (trackId(rows[i]) != trackId(m_rows[i])) { reordered = true; break; }
        if (reordered) {
            emit layoutAboutToBeChanged();
            const auto persistent = persistentIndexList();
            QStringList persistentIds;
            for (const auto &idx : persistent) persistentIds.append(trackId(m_rows[idx.row()]));
            QList<QJsonObject> ordered;
            for (const auto &row : rows) ordered.append(m_rows[m_indexes.value(trackId(row))]);
            m_rows = std::move(ordered); rebuildIndexes();
            QModelIndexList destinations;
            for (const auto &id : persistentIds) destinations.append(index(m_indexes.value(id)));
            changePersistentIndexList(persistent, destinations);
            emit layoutChanged();
        }
        for (int i = 0; i < rows.size(); ++i) {
            if (m_rows[i] != rows[i]) {
                m_dataCache.remove(i);
                m_rows[i] = rows[i]; emit dataChanged(index(i), index(i), {Qt::UserRole});
            }
        }
    }
    if (oldCount != count()) emit countChanged();
}
Bridge::Bridge(const QUrl &base, const QByteArray &key, QObject *parent, Config *config, qint64 maximumResponseBytes)
    : QObject(parent), m_base(base), m_key(key),
      m_config(config ? config : new Config(this)), m_tracks(this), m_cards(this) {
    // Qt's JSON parser uses bounded signed offsets. Overrides apply to future
    // requests and must also leave room for the overflow sentinel byte.
    m_maximumResponseBytes = qBound(qint64(1), maximumResponseBytes, qint64(1024) * 1024 * 1024);
    m_cachedHideIgnored = hideIgnored();
    connect(m_config, &Config::repositoryPathChanged, this, [this] {
        m_nativeAttemptedPath.clear();
        configureNativeRepository();
    });
    connect(m_config, &Config::settingsChanged, this, [this] {
        emit settingsChanged();
        syncYandexToken();
        if (m_cachedHideIgnored != hideIgnored()) {
            m_cachedHideIgnored = hideIgnored();
            rebuild();
        }
    });

    m_yandexRetry.setSingleShot(true);
    connect(&m_yandexRetry, &QTimer::timeout, this, &Bridge::syncYandexToken);
    connect(&m_poll, &QTimer::timeout, this, [this] { request("/api/state", {}, true); });
    m_poll.start(750);
    request("/api/state", {}, true);
    connect(&m_censorPoll, &QTimer::timeout, this, [this] { censorRequest(); configureNativeRepository(); });
    m_censorPoll.start(1500);
    censorRequest();
}

bool Bridge::nativePublicationBusy() const {
    if (m_nativeRecoveryPending) return true;
    if (!m_censorState.value("native").toBool()) return false;
    if (m_censorBusy || m_censorState.value("busy").toBool() || m_censorState.value("running").toBool()) return true;
    for (const auto &value : m_censorState.value("jobs").toArray()) {
        const auto job = value.toObject();
        const auto status = job.value("status").toString();
        const auto stage = job.value("stage").toString();
        if (status == "publishing" || status == "rolling_back" || stage == "publication" || stage == "rollback" || stage == "recovery") return true;
    }
    return false;
}

void Bridge::configureNativeRepository() {
    const auto path = m_nativeRepositoryProvider ? m_nativeRepositoryProvider() : QString();
    if (!m_nativePublicationValidator || !m_censorState.value("native").toBool() || m_censorBusy
        || m_censorState.value("busy").toBool() || m_censorState.value("running").toBool() || path.isEmpty()
        || path == m_nativeConfiguredPath || path == m_nativeAttemptedPath) return;
    if (m_nativePublicationValidator) {
        const auto error = m_nativePublicationValidator();
        if (!error.isEmpty()) return;
    }
    m_nativeAttemptedPath = path;
    ++m_censorGeneration;
    censorRequest("native_configure", {{"repository_path", path}});
}

void Bridge::censorCommand(const QString &action, const QVariantMap &args) {
    static const QSet<QString> actions = {"fetch", "start", "pause", "resume", "stop", "retry",
        "analyze", "search", "approve", "reject", "defer", "rollback", "settings", "check_track", "native_configure"};
    if (!actions.contains(action) || m_censorBusy) return;
    if (action == "native_configure") {
        m_nativeAttemptedPath.clear();
        configureNativeRepository();
        return;
    }
    QVariantMap commandArgs = args;
    if (action == "check_track") {
        const auto text = args.value("track_id").toString();
        bool valid = false;
        const auto id = text.toLongLong(&valid);
        static const QRegularExpression digits("\\A[0-9]+\\z");
        if (!m_censorState.value("native").toBool() || !valid || !digits.match(text).hasMatch()
            || id <= 0 || id > 9007199254740991LL || args.size() != 1) {
            const QString error = "Manual analysis requires a native backend and a positive, safely representable integer track ID (at most 9007199254740991)";
            emit toast(error); emit censorCommandFinished(action, false, error); return;
        }
        commandArgs["track_id"] = id;
    }
    if (m_censorState.value("native").toBool() && (action == "approve" || action == "rollback")) {
        const auto error = m_nativePublicationValidator ? m_nativePublicationValidator() : QString("Repository backend is unavailable");
        const auto path = m_nativeRepositoryProvider ? m_nativeRepositoryProvider() : QString();
        if (!error.isEmpty() || path.isEmpty()) {
            const auto message = error.isEmpty() ? QString("Wait for the configured repository to be validated before saving locally") : error;
            emit toast(message); emit censorCommandFinished(action, false, message); return;
        }
        if (m_nativeConfiguredPath != path) {
            m_nativeAction = action; m_nativeArgs = args;
            m_nativeAttemptedPath.clear(); configureNativeRepository(); return;
        }
    }
    ++m_censorGeneration;
    if (action != "settings") {
        m_censorSelectedJob = {};
        emit censorSelectedJobChanged();
    }
    censorRequest(action, commandArgs);
}


void Bridge::censorSelectJob(const QString &id) {
    ++m_censorSelectionGeneration;
    m_censorSelectedId = id;
    m_censorSelectedJob = {};
    emit censorSelectedJobChanged();
    if (!id.isEmpty()) censorRequest("__detail");
}

void Bridge::censorPage(int offset) {
    if (offset < 0 || offset > 1000 || m_censorBusy) return;
    m_censorOffset = offset;
    ++m_censorGeneration;
    censorRequest();
}

void Bridge::censorResolvePlayback(QString jobId, QString candidateId, int requestId) {
    if (m_signingOut || !m_state.value("authenticated").toBool()) {
        emit censorPlaybackResolved(requestId, jobId, candidateId, {}, "Playback requires an authenticated account");
        return;
    }
    if (jobId.isEmpty() || candidateId.isEmpty()) {
        emit censorPlaybackResolved(requestId, jobId, candidateId, {}, "Invalid playback selection");
        return;
    }
    QUrl url = m_base.resolved(QUrl("/api/censor/playback"));
    QUrlQuery query;
    query.addQueryItem("id", jobId);
    query.addQueryItem("candidate_id", candidateId);
    url.setQuery(query);
    QNetworkRequest req(url);
    req.setRawHeader("Authorization", "Bearer " + m_key);
    req.setHeader(QNetworkRequest::ContentTypeHeader, "application/json");
    req.setAttribute(QNetworkRequest::RedirectPolicyAttribute, QNetworkRequest::ManualRedirectPolicy);
    req.setTransferTimeout(m_requestDeadlineMs);
    auto reply = m_network.get(req);
    struct Transfer { QByteArray data; bool oversized = false, timedOut = false; };
    const auto transfer = std::make_shared<Transfer>();
    const qint64 limit = qMin(m_maximumResponseBytes, qint64(16 * 1024 * 1024));
    reply->setReadBufferSize(qMin(limit + 1, qint64(65536)));
    auto drain = [reply, transfer, limit] {
        if (transfer->oversized) return;
        while (reply->isOpen() && reply->bytesAvailable() > 0 && transfer->data.size() <= limit) {
            const auto bytes = reply->read(qMin(qint64(65536), limit + 1 - transfer->data.size()));
            if (bytes.isEmpty()) break;
            transfer->data += bytes;
        }
        const auto length = reply->header(QNetworkRequest::ContentLengthHeader);
        if (transfer->data.size() > limit || (length.isValid() && length.toLongLong() > limit)) {
            transfer->oversized = true;
            reply->abort();
        }
    };
    connect(reply, &QIODevice::readyRead, reply, drain);
    connect(reply, &QNetworkReply::metaDataChanged, reply, drain);
    auto deadline = new QTimer(reply);
    deadline->setSingleShot(true);
    connect(deadline, &QTimer::timeout, reply, [reply, transfer] { transfer->timedOut = true; reply->abort(); });
    deadline->start(m_requestDeadlineMs);
    const auto generation = m_censorPlaybackGeneration;
    const auto stateGeneration = m_stateGeneration;
    const auto account = m_state.value("user").toObject().value("id").toString();
    connect(reply, &QNetworkReply::finished, this,
            [this, reply, transfer, drain, deadline, generation, stateGeneration, account, jobId, candidateId, requestId] {
        deadline->stop();
        drain();
        QUrl playbackUrl;
        QString error;
        if (m_signingOut || generation != m_censorPlaybackGeneration || stateGeneration != m_stateGeneration
                || !m_state.value("authenticated").toBool()
                || account != m_state.value("user").toObject().value("id").toString()) {
            error = "Playback request is no longer current";
        } else {
            const int status = reply->attribute(QNetworkRequest::HttpStatusCodeAttribute).toInt();
            QJsonParseError parse;
            const auto doc = QJsonDocument::fromJson(transfer->data, &parse);
            const QUrl resolved(doc.object().value("url").toString(), QUrl::StrictMode);
            if (!transfer->oversized && !transfer->timedOut && reply->error() == QNetworkReply::NoError
                    && status >= 200 && status < 300 && parse.error == QJsonParseError::NoError && doc.isObject()
                    && resolved.isValid() && resolved.scheme() == "https" && !resolved.host().isEmpty()
                    && resolved.userInfo().isEmpty()) {
                playbackUrl = resolved;
            } else {
                // Backend and network errors may contain signed URLs; never forward them.
                error = transfer->oversized ? "Playback response exceeds size limit"
                    : transfer->timedOut ? "Playback request timed out"
                    : status == 409 ? (doc.object().value("error").toString() == "another operation is running"
                                            ? "Playback is busy. Wait for the current operation to finish and try again"
                                            : "The selected candidate is no longer available for playback. Analyze the track again")
                    : status == 401 || status == 403 ? "Playback authorization expired. Reconnect your account"
                    : status == 404 ? "Playback is unavailable. Restart the application and select the candidate again"
                    : status == 503 ? "Playback provider is unavailable. Check your Yandex connection"
                    : QString("Unable to resolve playback (HTTP %1)").arg(status);
            }
        }
        reply->deleteLater();
        emit censorPlaybackResolved(requestId, jobId, candidateId, playbackUrl, error);
    });
}

void Bridge::censorRequest(const QString &action, const QVariantMap &args, int busyRetries) {
    if (m_signingOut) return;
    const bool detail = action == "__detail";
    const bool get = action.isEmpty() || detail;
    if (detail && (m_censorDetailPolling || m_censorBusy || m_censorSelectedId.isEmpty())) return;
    if (detail) m_censorDetailPolling = true;
    if (get && !detail && (m_censorPolling || m_censorBusy)) return;
    if (get && !detail) m_censorPolling = true;
    else if (!get) { m_censorBusy = true; emit censorBusyChanged(); }
    const auto generation = m_censorGeneration;
    const auto selectionGeneration = m_censorSelectionGeneration;
    const auto selectedId = m_censorSelectedId;
    QString path = get ? "/api/censor/state" : "/api/censor/control";
    if (detail) path = "/api/censor/job?id=" + QString::fromLatin1(QUrl::toPercentEncoding(selectedId));
    else if (get && m_censorOffset > 0) path = "/api/censor/jobs?offset=" + QString::number(m_censorOffset);
    QNetworkRequest req(m_base.resolved(QUrl(path)));
    req.setRawHeader("Authorization", "Bearer " + m_key);
    req.setHeader(QNetworkRequest::ContentTypeHeader, "application/json");
    req.setAttribute(QNetworkRequest::RedirectPolicyAttribute, QNetworkRequest::ManualRedirectPolicy);
    const int timeout = get ? m_requestDeadlineMs : m_postDeadlineMs;
    req.setTransferTimeout(timeout);
    auto body = QJsonObject::fromVariantMap(args);
    body.insert("action", action);
    auto reply = get ? m_network.get(req) : m_network.post(req, QJsonDocument(body).toJson(QJsonDocument::Compact));
    struct Transfer { QByteArray data; bool oversized = false; };
    const auto transfer = std::make_shared<Transfer>();
    const qint64 limit = qMin(m_maximumResponseBytes, qint64(16 * 1024 * 1024));
    reply->setReadBufferSize(65536);
    auto drain = [reply, transfer, limit] {
        while (reply->isOpen() && reply->bytesAvailable() && transfer->data.size() <= limit) {
            transfer->data += reply->read(qMin(qint64(65536), limit + 1 - transfer->data.size()));
        }
        if (transfer->data.size() > limit) { transfer->oversized = true; reply->abort(); }
    };
    connect(reply, &QIODevice::readyRead, reply, drain);
    auto deadline = new QTimer(reply);
    deadline->setSingleShot(true);
    connect(deadline, &QTimer::timeout, reply, &QNetworkReply::abort);
    deadline->start(timeout);
    connect(reply, &QNetworkReply::finished, this, [this, reply, transfer, drain, deadline, get, detail, generation, selectionGeneration, selectedId, action, args, busyRetries] {
        deadline->stop();
        drain();
        if (detail) m_censorDetailPolling = false;
        else if (get) m_censorPolling = false;
        else { m_censorBusy = false; emit censorBusyChanged(); }
        const int status = reply->attribute(QNetworkRequest::HttpStatusCodeAttribute).toInt();
        QJsonParseError parse;
        const auto doc = QJsonDocument::fromJson(transfer->data, &parse);
        if (!get && status == 409 && doc.object().value("error").toString().contains("operation") && busyRetries < 8) {
            reply->deleteLater();
            QTimer::singleShot(150, this, [this, action, args, busyRetries] { censorRequest(action, args, busyRetries + 1); });
            return;
        }
        const bool ok = !transfer->oversized && reply->error() == QNetworkReply::NoError
            && status >= 200 && status < 300 && doc.isObject() && parse.error == QJsonParseError::NoError
                        && (detail ? doc.object().value("job").isObject() && doc.object().value("job").toObject().value("id").toString() == selectedId
                                        : doc.object().value("jobs").isArray() && doc.object().value("configured").isBool());
        QString error;
        if (detail) {
            if (selectionGeneration == m_censorSelectionGeneration && generation == m_censorGeneration) {
                auto nextJob = ok ? doc.object().value("job").toObject() : QJsonObject{};
                if (!ok) nextJob.insert("detail_error", doc.object().value("error").toString("Unable to load selected job; retry selection"));
                if (m_censorSelectedJob != nextJob) {
                    m_censorSelectedJob = nextJob;
                    emit censorSelectedJobChanged();
                }
            }
            reply->deleteLater();
            if (selectionGeneration != m_censorSelectionGeneration && !m_censorSelectedId.isEmpty()) censorRequest("__detail");
            return;
        }
        if (generation == m_censorGeneration) {
            const auto previousState = m_censorState;
            if (ok) {
                m_censorState = doc.object();
                m_nativeRecoveryPending = false;
                const bool repositoryReady = m_censorState.value("native_repository_ready") != QJsonValue(false);
                if (!repositoryReady) {
                    if (!m_nativeConfiguredPath.isEmpty()) m_nativeAttemptedPath.clear();
                    m_nativeConfiguredPath.clear();
                }
                else if (action == "native_configure" && m_nativeRepositoryProvider
                         && m_nativeRepositoryProvider() == args.value("repository_path").toString())
                    m_nativeConfiguredPath = args.value("repository_path").toString();
            } else {
                error = transfer->oversized ? "Censor response exceeds size limit" :
                    status == 404 ? "Censor bridge endpoints are unavailable; update the application backend" :
                    doc.object().value("error").toString();
                if (error.isEmpty()) error = reply->error() != QNetworkReply::NoError ? reply->errorString() : "Invalid Censor bridge response";
                m_censorState.insert("error", error);
                const bool settingsDeferred = action == "settings" && status == 409
                    && error == "Censor settings are waiting for active processing to finish";
                if (!get && !settingsDeferred) emit toast(error);
            }
            // Polling identical snapshots must not reset QML models and control state.
            if (m_censorState != previousState) emit censorStateChanged();
            if (ok) {
                if (!doc.object().value("configured").toBool()) censorSelectJob({});
                m_censorOffset = doc.object().value("offset").toInt();
                censorRequest("__detail");
            }
        }
        if (!get) emit censorCommandFinished(action, ok, error);
        if (action == "native_configure" && !m_nativeAction.isEmpty()) {
            const auto pending = m_nativeAction; const auto args = m_nativeArgs;
            m_nativeAction.clear(); m_nativeArgs.clear();
            if (ok && !m_nativeConfiguredPath.isEmpty()) censorCommand(pending, args);
            else emit censorCommandFinished(pending, false, error);
        }
        // Failed attempts may be transient (upload/terminal activity or transport).
        // Release the latch, but let the next poll retry rather than looping here.
        if (action == "native_configure" && (!ok || m_nativeConfiguredPath != args.value("repository_path").toString()))
            m_nativeAttemptedPath.clear();
        if (ok && action != "native_configure") configureNativeRepository();
        reply->deleteLater();
        if (!get || generation != m_censorGeneration) censorRequest();
    });
}

void Bridge::setHideIgnored(bool value) {
    m_config->setHideIgnored(value);
}

void Bridge::setSearch(const QString &value) { if (m_search != value) { m_search = value; rebuild(); } }
void Bridge::setSort(int value) { if (value >= 0 && value <= 3 && value != m_sort) { m_sort = value; rebuild(); } }
void Bridge::setConnectionError(const QString &error) {
    if (m_connectionError != error) { m_connectionError = error; emit connectionErrorChanged(); }
}
void Bridge::request(const QString &path, const QJsonObject &body, bool get, int busyRetries) {
    if (get && m_polling) return;
    if (get) m_polling = true;
    QNetworkRequest req(m_base.resolved(QUrl(path)));
    req.setRawHeader("Authorization", "Bearer " + m_key);
    const bool forced = get && m_forceRefresh;
    if (get) {
        m_forceRefresh = false;
        if (!forced && !m_stateETag.isEmpty()) req.setRawHeader("If-None-Match", m_stateETag);
    }
    req.setHeader(QNetworkRequest::ContentTypeHeader, "application/json");
    req.setAttribute(QNetworkRequest::RedirectPolicyAttribute, QNetworkRequest::ManualRedirectPolicy);
    const int deadlineMs = get ? m_requestDeadlineMs : m_postDeadlineMs;
    req.setTransferTimeout(deadlineMs);
    auto reply = get ? m_network.get(req) : m_network.post(req, QJsonDocument(body).toJson(QJsonDocument::Compact));
    struct Transfer { QByteArray bytes; bool tooLarge = false, timedOut = false; };
    const auto transfer = std::make_shared<Transfer>();
    // Control POSTs don't return the track collection; keep their old budget.
    const qint64 limit = get ? m_maximumResponseBytes : qMin(m_maximumResponseBytes, qint64(16) * 1024 * 1024);
    reply->setReadBufferSize(qMin(limit + 1, qint64(64 * 1024)));
    auto drain = [reply, transfer, limit] {
        if (transfer->tooLarge) return;
        while (reply->isOpen() && reply->bytesAvailable() > 0 && transfer->bytes.size() <= limit) {
            const qint64 count = qMin(qint64(64 * 1024), limit + 1 - transfer->bytes.size());
            const auto bytes = reply->read(count);
            if (bytes.isEmpty()) break;
            transfer->bytes += bytes;
        }
        const auto length = reply->header(QNetworkRequest::ContentLengthHeader);
        if (transfer->bytes.size() > limit || (length.isValid() && length.toLongLong() > limit)) {
            transfer->tooLarge = true; reply->abort();
        }
    };
    connect(reply, &QIODevice::readyRead, reply, drain);
    connect(reply, &QNetworkReply::metaDataChanged, reply, drain);
    auto deadline = new QTimer(reply);
    deadline->setSingleShot(true);
    connect(deadline, &QTimer::timeout, reply, [reply, transfer] { transfer->timedOut = true; reply->abort(); });
    deadline->start(deadlineMs);
    const auto stateGeneration = m_stateGeneration;
    const QString sentAccount = m_yandexPendingAccount;
    connect(reply, &QNetworkReply::finished, this, [this, reply, get, path, body, transfer, drain, deadline, stateGeneration, sentAccount, busyRetries] {
        deadline->stop();
        drain();
        if (get) m_polling = false;
        if (get && stateGeneration != m_stateGeneration) {
            reply->deleteLater(); m_forceRefresh = true; request("/api/state", {}, true); return;
        }
        if (get && reply->attribute(QNetworkRequest::HttpStatusCodeAttribute).toInt() == 304
                && reply->error() == QNetworkReply::NoError && !m_stateETag.isEmpty()) {
            setConnectionError({});
            reply->deleteLater();
            if (m_forceRefresh) request("/api/state", {}, true);
            return;
        }
        QJsonParseError parse;
        const auto document = QJsonDocument::fromJson(transfer->bytes, &parse);
        const auto object = document.object();
        const int status = reply->attribute(QNetworkRequest::HttpStatusCodeAttribute).toInt();
        const QString backendError = object.value("error").toString();
        if (!get && status == 409 && backendError.contains("operation") && busyRetries < 8) {
            reply->deleteLater();
            QTimer::singleShot(150, this, [this, path, body, busyRetries] { request(path, body, false, busyRetries + 1); });
            return;
        }
        const bool succeeded = reply->error() == QNetworkReply::NoError && !transfer->tooLarge && !transfer->timedOut
            && status >= 200 && status < 300 && parse.error == QJsonParseError::NoError && document.isObject()
            && (get || object.value("error").toString().isEmpty());
        if (!succeeded) {
            // Do not forward raw URLs, headers, or authorization responses to QML.
            const auto message = transfer->tooLarge ? QString("Backend response exceeds size limit")
                : transfer->timedOut ? QString("Backend request timed out")
                : object.value("error").toString("Unable to contact DDetector backend");
            if (get) setConnectionError(message); else emit toast(message);
                        if (path == "/api/login") { m_pendingProvider.clear(); emit pendingProviderChanged(); }
        } else if (get) {
            if (!object.value("authenticated").isBool() || !object.value("tracks").isArray())
                setConnectionError("Invalid backend state response");
            else {
                m_stateETag = reply->rawHeader("ETag");
                setConnectionError({}); acceptState(object);
            }
        } else {
            if (path == "/api/login" && !object.value("browser_opened").toBool())
                openBrowser(QUrl(object.value("authorization_url").toString()));
        }
        if (path == "/api/logout") { m_signingOut = false; ++m_stateGeneration; }
        if (path == "/api/yandex/token") {
            m_yandexSyncing = false;
            const auto sentToken = body.value("access_token").toString();
            if (succeeded) {
                m_yandexAcknowledged = true;
                m_yandexAttemptedToken = sentToken; m_yandexAccount = sentAccount;
                m_yandexMayHaveToken = !sentToken.isEmpty();
                m_yandexRejected = false;
                m_yandexRejectedToken.clear(); m_yandexRejectedAccount.clear();
                m_yandexRetryDelay = 1000;
                if (!sentToken.isEmpty() && sentToken == m_config->musicToken()
                    && sentAccount == m_state.value("user").toObject().value("id").toString()
                    && m_state.value("authenticated").toBool() && m_state.value("hasAccess").toBool()
                    && !m_state.value("isBanned").toBool()) request("/api/load");
                syncYandexToken();
            } else if (status == 409 || status == 408 || status == 429 || status >= 500 || status == 0
                       || (status >= 200 && status < 400 && (transfer->timedOut || transfer->tooLarge
                           || parse.error != QJsonParseError::NoError || !document.isObject()
                           || reply->error() != QNetworkReply::NoError))) {
                m_yandexRetry.start(m_yandexRetryDelay);
                m_yandexRetryDelay = qMin(30000, m_yandexRetryDelay * 2);
            } else {
                m_yandexRejected = true;
                m_yandexRejectedToken = sentToken; m_yandexRejectedAccount = sentAccount;
                syncYandexToken();
            }
        }
        reply->deleteLater();
        // Failed POSTs can also change state (for example access revocation).
        if (!get) m_forceRefresh = true;
        if (m_forceRefresh) request("/api/state", {}, true);
    });
}
void Bridge::acceptState(QJsonObject state) {
    if (state.value("authenticated") != m_state.value("authenticated")
            || state.value("user").toObject().value("id") != m_state.value("user").toObject().value("id")
            || state.value("hasAccess") != m_state.value("hasAccess")
            || state.value("isBanned") != m_state.value("isBanned")) {
        ++m_censorPlaybackGeneration;
    }
    if (m_signingOut) {
        state.insert("authenticated", false);
        state.insert("hasAccess", false);
    }
    auto source = state.take("tracks").toArray();
    // Explicit allowlist: neither backend session tokens nor arbitrary fields reach QML.
    QJsonObject safe;
    for (const auto &name : {"authenticated", "yandexConnected", "user", "accessChecked", "hasAccess", "isBanned", "loginPending", "loading", "fetching", "processing", "processed", "foundCount", "error", "lastFetch"}) {
        if (state.contains(name)) safe.insert(name, state.value(name));
    }
    if (safe.value("user").isObject()) {
        const auto user = safe.value("user").toObject();
        QJsonObject publicUser;
        for (const auto &name : {"id", "email"}) if (user.contains(name)) publicUser.insert(name, user.value(name));
                const auto metadata = user.value("user_metadata").toObject();
                QJsonObject publicMetadata;
                for (const auto &name : {"name", "full_name", "avatar_url", "picture", "preferred_username"})
                    if (metadata.value(name).isString()) {
                                            const auto value = metadata.value(name).toString();
                                            if (QString(name) == "avatar_url" || QString(name) == "picture") {
                                                const QUrl url(value);
                                                if (!url.isValid() || url.scheme() != "https" || url.host().isEmpty() || !url.userInfo().isEmpty()) continue;
                                            }
                                            publicMetadata.insert(name, value);
                                        }
                publicUser.insert("user_metadata", publicMetadata);
        QJsonArray publicIdentities;
        for (const auto &value : user.value("identities").toArray()) {
            const auto identity = value.toObject();
            QJsonObject publicIdentity, publicData;
            if (identity.value("provider").isString())
                publicIdentity.insert("provider", identity.value("provider"));
            const auto data = identity.value("identity_data").toObject();
            for (const auto &name : {"avatar_url", "picture"}) {
                if (!data.value(name).isString()) continue;
                const QUrl url(data.value(name).toString());
                if (url.isValid() && url.scheme() == "https" && !url.host().isEmpty() && url.userInfo().isEmpty())
                    publicData.insert(name, data.value(name));
            }
            publicIdentity.insert("identity_data", publicData);
            publicIdentities.append(publicIdentity);
        }
        publicUser.insert("identities", publicIdentities);
        safe.insert("user", publicUser);
    }
    if (!safe.value("authenticated").toBool() || !safe.value("hasAccess").toBool() || safe.value("isBanned").toBool()) source = {};
    QJsonArray publicTracks;
        QSet<QString> publicIds;
        for (const auto &value : source) {
            const auto track = value.toObject();
            const QString id = trackId(track);
            if (id.isEmpty() || publicIds.contains(id)) continue;
            publicIds.insert(id);
            QJsonObject publicTrack;
            for (const auto &name : {"id", "title", "artist", "cover", "added_at", "status", "ignored", "lyrics", "lines", "allLines"})
                if (track.contains(name)) publicTrack.insert(name, track.value(name));
            publicTracks.append(publicTrack);
        }
        source = publicTracks;
        safe.insert("trackCount", source.size());
        const auto last = safe.value("lastFetch").toObject();
    if (!safe.value("fetching").toBool() && !last.isEmpty()
                && (last != m_state.value("lastFetch").toObject()
                    || (m_state.value("fetching").toBool() && safe.value("error").toString().isEmpty())))
        emit toast(QString("Done: %1 tracks, +%2 added, -%3 removed, %4 lyrics fetched")
                   .arg(last.value("total").toInt()).arg(last.value("added").toInt())
                   .arg(last.value("removed").toInt()).arg(last.value("lyrics_fetched").toInt()));
    if (!m_pendingProvider.isEmpty() && (safe.value("authenticated").toBool()
            || !safe.value("error").toString().isEmpty()
            || (m_state.value("loginPending").toBool() && !safe.value("loginPending").toBool()))) {
            m_pendingProvider.clear(); emit pendingProviderChanged();
        }
        const bool changed = source != m_source;
    m_source = source;
    if (safe != m_state) { m_state = safe; emit stateChanged(); }
    if (changed) rebuild();
    syncYandexToken();
}
void Bridge::rebuild() {
    QList<QJsonObject> tracks, cards;
    const QString query = m_search.trimmed();
    for (const auto &value : m_source) {
        auto row = value.toObject();
        if (hideIgnored() && row.value("ignored").toBool()) continue;
        const auto date = QDateTime::fromString(row.value("added_at").toString(), Qt::ISODateWithMs);
        row.insert("date", date.isValid() ? date.toLocalTime().toString("MM/dd/yyyy") : "");
        if (row.value("status").toString() == "found") cards.append(row);
        if (query.isEmpty() || row.value("title").toString().contains(query, Qt::CaseInsensitive)
            || row.value("artist").toString().contains(query, Qt::CaseInsensitive) || trackId(row).contains(query)) tracks.append(row);
    }
    QCollator collator; collator.setCaseSensitivity(Qt::CaseInsensitive);
    QHash<QString, int> artistCount;
    QHash<QString, QDateTime> dates;
    for (const auto &row : tracks) {
        ++artistCount[row.value("artist").toString()];
        if (m_sort == 1) dates.insert(trackId(row), QDateTime::fromString(row.value("added_at").toString(), Qt::ISODateWithMs));
    }
    if (m_sort) std::stable_sort(tracks.begin(), tracks.end(), [&](const auto &a, const auto &b) {
        if (m_sort == 1) return dates.value(trackId(a)) > dates.value(trackId(b));
        const QString field = m_sort == 2 ? "title" : "artist";
        const auto x = a.value(field).toString(), y = b.value(field).toString();
        if (m_sort == 3 && artistCount[x] != artistCount[y]) return artistCount[x] > artistCount[y];
        return collator.compare(x, y) < 0;
    });
    m_tracks.sync(tracks); m_cards.sync(cards);
}
void Bridge::login(const QString &provider) {
    if (!m_pendingProvider.isEmpty() || m_state.value("loginPending").toBool()) return;
        if (provider == "github" || provider == "discord" || provider == "spotify") {
            m_pendingProvider = provider; emit pendingProviderChanged();
            request("/api/login", {{"provider", provider}});
        }
}
void Bridge::syncYandexToken() {
    if (m_yandexSyncing) return;
    const bool allowed = m_state.value("authenticated").toBool() && m_state.value("hasAccess").toBool()
        && !m_state.value("isBanned").toBool();
    const QString account = allowed ? m_state.value("user").toObject().value("id").toString() : QString();
    const QString token = allowed ? m_config->musicToken() : QString();
    if (m_yandexRetry.isActive()) {
        if (token == m_yandexPendingToken && account == m_yandexPendingAccount) return;
        m_yandexRetry.stop(); m_yandexRetryDelay = 1000;
    }
    if (token.isEmpty() && !m_yandexMayHaveToken) { m_yandexAccount = account; return; }
    if (m_yandexAcknowledged && token == m_yandexAttemptedToken && account == m_yandexAccount) return;
    if (m_yandexRejected && token == m_yandexRejectedToken && account == m_yandexRejectedAccount) return;
    m_yandexPendingToken = token;
    m_yandexPendingAccount = account;
    if (!token.isEmpty()) m_yandexMayHaveToken = true;
    m_yandexAcknowledged = false;
    m_yandexSyncing = true;
    request("/api/yandex/token", {{"access_token", token}});
}
void Bridge::logout() {

    m_signingOut = true;
    ++m_censorPlaybackGeneration;
    ++m_censorGeneration;
    censorSelectJob({});
    m_censorState = {};
    emit censorStateChanged();
    ++m_stateGeneration;
    // Stop local playback through stateChanged immediately, without waiting for the network.
    m_state.insert("authenticated", false);
    m_state.insert("hasAccess", false);
    m_source = {};
    rebuild();
    emit stateChanged();
    request("/api/logout");
    syncYandexToken();
}
void Bridge::load() { request("/api/load"); }
void Bridge::fetch() { request("/api/fetch"); }
QVariantMap Bridge::trackById(const QString &id) const {
    for (const auto &value : m_source) {
        const auto track = value.toObject();
        if (trackId(track) == id) return track.toVariantMap();
    }
    return {};
}

void Bridge::ignore(const QString &id, bool ignored) {

    bool ok = false; const qint64 number = id.toLongLong(&ok);
    if (ok && number > 0) request("/api/ignore", {{"track_id", number}, {"ignored", ignored}});
}
void Bridge::copyId(const QString &id) { QGuiApplication::clipboard()->setText(id); emit toast("ID copied: " + id); }
void Bridge::searchYandex(const QString &title, const QString &artist) {
    QUrl url("https://yandex.ru/search/"); QUrlQuery query;
    query.addQueryItem("text", title + " " + artist + " download mp3"); url.setQuery(query); openBrowser(url);
}
void Bridge::openBrowser(const QUrl &url) {
    if ((url.scheme() != "https" && url.scheme() != "http") || !QDesktopServices::openUrl(url)) emit toast("Unable to open system browser");
}

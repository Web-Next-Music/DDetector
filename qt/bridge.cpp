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
    auto value = m_rows[index.row()].toVariantMap();
    value["id"] = trackId(m_rows[index.row()]);
    return value;
}
int TrackModel::indexOf(const QString &id) const { return m_indexes.value(id, -1); }
void TrackModel::rebuildIndexes() {
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
}

void Bridge::setHideIgnored(bool value) {
    m_config->setHideIgnored(value);
}

void Bridge::setSearch(const QString &value) { if (m_search != value) { m_search = value; rebuild(); } }
void Bridge::setSort(int value) { if (value >= 0 && value <= 3 && value != m_sort) { m_sort = value; rebuild(); } }
void Bridge::setConnectionError(const QString &error) {
    if (m_connectionError != error) { m_connectionError = error; emit connectionErrorChanged(); }
}
void Bridge::request(const QString &path, const QJsonObject &body, bool get) {
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
    connect(reply, &QNetworkReply::finished, this, [this, reply, get, path, body, transfer, drain, deadline, stateGeneration, sentAccount] {
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
        emit toast(QString("Done: %1 tracks, +%2 added, −%3 removed, %4 lyrics fetched")
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

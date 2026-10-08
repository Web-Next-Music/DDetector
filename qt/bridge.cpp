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

static QString trackId(const QJsonObject &row) {
    const auto id = row.value("id");
    return id.isString() ? id.toString() : QString::number(id.toInteger());
}
QVariant TrackModel::data(const QModelIndex &index, int role) const {
    if (!index.isValid() || index.row() >= m_rows.size() || role != Qt::UserRole) return {};
    auto value = m_rows[index.row()].toVariantMap();
    value["id"] = trackId(m_rows[index.row()]);
    return value;
}
int TrackModel::indexOf(const QString &id) const {
    for (int i = 0; i < m_rows.size(); ++i) if (trackId(m_rows[i]) == id) return i;
    return -1;
}
void TrackModel::sync(const QList<QJsonObject> &rows) {
    const int oldCount = count();
    QSet<QString> ids;
    for (const auto &row : rows) ids.insert(trackId(row));
    for (int i = m_rows.size() - 1; i >= 0; --i) {
        if (!ids.contains(trackId(m_rows[i]))) {
            beginRemoveRows({}, i, i); m_rows.removeAt(i); endRemoveRows();
        }
    }
    for (int i = 0; i < rows.size(); ++i) {
        const QString id = trackId(rows[i]);
        int from = i < m_rows.size() && trackId(m_rows[i]) == id ? i : indexOf(id);
        if (from < 0) {
            beginInsertRows({}, i, i); m_rows.insert(i, rows[i]); endInsertRows();
        } else {
            if (from != i) {
                beginMoveRows({}, from, from, {}, from < i ? i + 1 : i);
                m_rows.move(from, i); endMoveRows();
            }
            if (m_rows[i] != rows[i]) {
                m_rows[i] = rows[i]; emit dataChanged(index(i), index(i), {Qt::UserRole});
            }
        }
    }
    if (oldCount != count()) emit countChanged();
}
Bridge::Bridge(const QUrl &base, const QByteArray &key, QObject *parent, Config *config)
    : QObject(parent), m_base(base), m_key(key),
      m_config(config ? config : new Config(this)), m_tracks(this), m_cards(this) {
    m_cachedHideIgnored = hideIgnored();
    connect(m_config, &Config::settingsChanged, this, [this] {
        emit settingsChanged();
        syncYandexToken();
        if (m_cachedHideIgnored != hideIgnored()) {
            m_cachedHideIgnored = hideIgnored();
            rebuild();
        }
    });

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
    req.setTransferTimeout(100000);
    auto reply = get ? m_network.get(req) : m_network.post(req, QJsonDocument(body).toJson(QJsonDocument::Compact));
    connect(reply, &QNetworkReply::finished, this, [this, reply, get, path, body] {
        if (get) m_polling = false;
        if (get && reply->attribute(QNetworkRequest::HttpStatusCodeAttribute).toInt() == 304
                && reply->error() == QNetworkReply::NoError && !m_stateETag.isEmpty()) {
            setConnectionError({});
            reply->deleteLater();
            if (m_forceRefresh) request("/api/state", {}, true);
            return;
        }
        const auto bytes = reply->readAll();
        const auto object = QJsonDocument::fromJson(bytes).object();
        if (reply->error() != QNetworkReply::NoError) {
            // Do not forward raw URLs, headers, or authorization responses to QML.
            const auto message = object.value("error").toString("Unable to contact DDetector backend");
            if (get) setConnectionError(message); else emit toast(message);
                        if (path == "/api/login") { m_pendingProvider.clear(); emit pendingProviderChanged(); }
        } else if (get) {
            if (!object.contains("authenticated") || !object.value("tracks").isArray())
                setConnectionError("Invalid backend state response");
            else {
                m_stateETag = reply->rawHeader("ETag");
                setConnectionError({}); acceptState(object);
            }
        } else {
            if (path == "/api/login" && !object.value("browser_opened").toBool())
                openBrowser(QUrl(object.value("authorization_url").toString()));
        }
        if (path == "/api/logout") m_signingOut = false;
        if (path == "/api/yandex/token") {
            m_yandexSyncing = false;
            const auto sentToken = body.value("access_token").toString();
            if (reply->attribute(QNetworkRequest::HttpStatusCodeAttribute).toInt() == 409) {
                m_yandexAttemptedToken.clear();
                m_yandexAccount.clear();
                QTimer::singleShot(1000, this, &Bridge::syncYandexToken);
            } else {
                if (reply->error() == QNetworkReply::NoError && !sentToken.isEmpty()
                    && sentToken == m_config->musicToken() && m_state.value("authenticated").toBool()
                    && m_state.value("hasAccess").toBool()) request("/api/load");
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
        for (const auto &value : source) {
            const auto track = value.toObject();
            QJsonObject publicTrack;
            for (const auto &name : {"id", "title", "artist", "cover", "added_at", "status", "ignored", "lyrics", "lines", "allLines"})
                if (track.contains(name)) publicTrack.insert(name, track.value(name));
            publicTracks.append(publicTrack);
        }
        source = publicTracks;
        safe.insert("trackCount", source.size());
        const auto last = safe.value("lastFetch").toObject();
    if (!last.isEmpty() && last != m_state.value("lastFetch").toObject())
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
    for (const auto &row : tracks) ++artistCount[row.value("artist").toString()];
    if (m_sort) std::stable_sort(tracks.begin(), tracks.end(), [&](const auto &a, const auto &b) {
        if (m_sort == 1) return QDateTime::fromString(a.value("added_at").toString(), Qt::ISODateWithMs)
                            > QDateTime::fromString(b.value("added_at").toString(), Qt::ISODateWithMs);
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
    if (token.isEmpty() && m_yandexAttemptedToken.isEmpty()) {
        m_yandexAccount = account;
        return;
    }
    if (token == m_yandexAttemptedToken && account == m_yandexAccount) return;
    m_yandexAttemptedToken = token;
    m_yandexAccount = account;
    m_yandexSyncing = true;
    request("/api/yandex/token", {{"access_token", token}});
}
void Bridge::logout() {

    m_signingOut = true;
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
    query.addQueryItem("text", title + " " + artist + " скачать mp3"); url.setQuery(query); openBrowser(url);
}
void Bridge::openBrowser(const QUrl &url) {
    if ((url.scheme() != "https" && url.scheme() != "http") || !QDesktopServices::openUrl(url)) emit toast("Unable to open system browser");
}

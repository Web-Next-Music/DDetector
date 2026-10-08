#pragma once
#include <QAbstractListModel>
#include <QJsonArray>
#include <QJsonObject>
#include <QNetworkAccessManager>
#include <QSettings>
#include <QTimer>

class TrackModel final : public QAbstractListModel {
    Q_OBJECT
    Q_PROPERTY(int count READ count NOTIFY countChanged)
public:
    explicit TrackModel(QObject *parent = nullptr) : QAbstractListModel(parent) {}
    int rowCount(const QModelIndex &parent = {}) const override { return parent.isValid() ? 0 : m_rows.size(); }
    int count() const { return m_rows.size(); }
    QVariant data(const QModelIndex &index, int role) const override;
    QHash<int, QByteArray> roleNames() const override { return {{Qt::UserRole, "track"}}; }
    void sync(const QList<QJsonObject> &rows);
    Q_INVOKABLE int indexOf(const QString &id) const;
signals:
    void countChanged();
private:
    QList<QJsonObject> m_rows;
};

class Bridge final : public QObject {
    Q_OBJECT
    Q_PROPERTY(QVariantMap state READ state NOTIFY stateChanged)
    Q_PROPERTY(TrackModel *tracks READ tracks CONSTANT)
    Q_PROPERTY(TrackModel *cards READ cards CONSTANT)
    Q_PROPERTY(bool hideIgnored READ hideIgnored WRITE setHideIgnored NOTIFY settingsChanged)

    Q_PROPERTY(QString connectionError READ connectionError NOTIFY connectionErrorChanged)
        Q_PROPERTY(QString pendingProvider READ pendingProvider NOTIFY pendingProviderChanged)
public:
    Bridge(const QUrl &base, const QByteArray &key, bool demo, QObject *parent = nullptr);
    QVariantMap state() const { return m_state.toVariantMap(); }
    TrackModel *tracks() { return &m_tracks; }
    TrackModel *cards() { return &m_cards; }
    bool hideIgnored() const { return m_settings.value("hideIgnored", false).toBool(); }

    QString connectionError() const { return m_connectionError; }
        QString pendingProvider() const { return m_pendingProvider; }
    void setHideIgnored(bool value);

    Q_INVOKABLE void setSearch(const QString &value);
    Q_INVOKABLE void setSort(int value);
    Q_INVOKABLE void login(const QString &provider);
    Q_INVOKABLE void logout();
    Q_INVOKABLE void load();
    Q_INVOKABLE void fetch();
    Q_INVOKABLE void ignore(const QString &id, bool ignored);
    Q_INVOKABLE void copyId(const QString &id);
    Q_INVOKABLE void searchYandex(const QString &title, const QString &artist);
    Q_INVOKABLE void openBrowser(const QUrl &url);
signals:
    void stateChanged();
    void settingsChanged();
    void connectionErrorChanged();
        void pendingProviderChanged();
    void toast(const QString &message);
private:
    void request(const QString &path, const QJsonObject &body = {}, bool get = false);
    void acceptState(QJsonObject state);
    void rebuild();
    void setConnectionError(const QString &error);
    QNetworkAccessManager m_network;
    QUrl m_base;
    QByteArray m_key; // Never registered with QML or written to QSettings/logs.
    bool m_demo = false;
    bool m_polling = false;
    QSettings m_settings;
    QTimer m_poll;
    QJsonObject m_state;
    QJsonArray m_source;
    TrackModel m_tracks, m_cards;
    QString m_search, m_connectionError, m_pendingProvider;
    int m_sort = 0;
};

#pragma once

#include <QObject>
#include <QAudioOutput>
#include <QMediaPlayer>
#include <QNetworkAccessManager>
#include <QSet>
#include <QStringList>
#include <QTimer>
#include <functional>
#include <QAudioBufferOutput>
#include <QChronoTimer>
#include <QElapsedTimer>
#include <QVariantList>
#include <memory>
#include "spectrumanalyser.h"

class QNetworkReply;

// Playback keeps its credential in memory; Config owns persistence and the settings UI.
// No token getter is exposed by the player.
// This player is independent of Ynison and all other playback sessions.
class MusicPlayer final : public QObject {
    Q_OBJECT
    Q_PROPERTY(bool enabled READ enabled NOTIFY stateChanged)
    Q_PROPERTY(bool busy READ busy NOTIFY stateChanged)
    Q_PROPERTY(bool playing READ playing NOTIFY stateChanged)
    Q_PROPERTY(QString title READ title NOTIFY stateChanged)
    Q_PROPERTY(QString artist READ artist NOTIFY stateChanged)
    Q_PROPERTY(QString cover READ cover NOTIFY stateChanged)
    Q_PROPERTY(QString error READ error NOTIFY stateChanged)
    Q_PROPERTY(QString trackId READ trackId NOTIFY stateChanged)
    Q_PROPERTY(qint64 duration READ duration NOTIFY durationChanged)
    Q_PROPERTY(qint64 position READ position NOTIFY positionChanged)
    Q_PROPERTY(double volume READ volume WRITE setVolume NOTIFY stateChanged)
    Q_PROPERTY(bool visualizerEnabled READ visualizerEnabled WRITE setVisualizerEnabled NOTIFY visualizerEnabledChanged)
    Q_PROPERTY(QVariantList spectrumLevels READ spectrumLevels NOTIFY spectrumLevelsChanged)
    Q_PROPERTY(QVariantList spectrumPeaks READ spectrumPeaks NOTIFY spectrumPeaksChanged)
public:
    bool visualizerEnabled() const { return m_visualizerEnabled; }
    void setVisualizerEnabled(bool enabled);
    QVariantList spectrumLevels() const { return m_spectrumLevels; }
    QVariantList spectrumPeaks() const { return m_spectrumPeaks; }
    explicit MusicPlayer(QObject *parent = nullptr);
    bool enabled() const { return m_enabled; }
    bool busy() const { return m_busy; }
    bool playing() const { return m_player.playbackState() == QMediaPlayer::PlayingState; }
    QString trackId() const { return m_trackId; }
    QString title() const { return m_title; }
    QString artist() const { return m_artist; }
    QString cover() const { return m_cover; }
    QString error() const { return m_error; }
    qint64 duration() const { return m_player.duration() > 0 ? m_player.duration() : m_duration; }
    qint64 position() const { return m_player.position(); }
    double volume() const { return m_audio.volume(); }
    Q_INVOKABLE void setToken(QString token);
    Q_INVOKABLE void playTrack(QString trackId);
    Q_INVOKABLE void playAt(QString trackId, QString timestamp);
    Q_INVOKABLE void toggle();
    Q_INVOKABLE void seek(qint64 milliseconds);
    Q_INVOKABLE void setVolume(double volume);
    Q_INVOKABLE void next();
    Q_INVOKABLE void previous();
signals:
    void visualizerEnabledChanged();
    void spectrumLevelsChanged();
    void spectrumPeaksChanged();
    void stateChanged();
    void positionChanged();
    void durationChanged();
private:
    friend class MusicPlayerTests;
#ifdef MUSIC_PLAYER_TEST
    std::function<void()> m_requestHook;
#endif
    void resetSpectrum();
    void attachSpectrumOutput();
    void receiveSpectrumBuffer(const QAudioBuffer &buffer);
    void updateSpectrum();
    bool m_visualizerEnabled = false;
    quint64 m_spectrumEpoch = 0;
    qint64 m_spectrumSeekFloor = -1;
    std::unique_ptr<QAudioBufferOutput> m_bufferOutput;
    QChronoTimer m_spectrumTimer;
    QElapsedTimer m_spectrumClock;
    Spectrum::AudioTap m_spectrumTap;
    Spectrum::WideAnalyser m_spectrumAnalyser;
    QVariantList m_spectrumLevels, m_spectrumPeaks;
    void applyPendingSeek();
    void cancelRequests();
    void fail(const QString &message);
    void loadTrack(const QString &id);
    void get(const QUrl &url, bool oauth, quint64 generation,
             std::function<void(const QByteArray &)> callback);
    QNetworkAccessManager m_network;
    QAudioOutput m_audio;
    QMediaPlayer m_player;
    QTimer m_mediaTimeout;
    QSet<QNetworkReply *> m_replies;
    QByteArray m_token;
    quint64 m_generation = 0;
    bool m_enabled = false, m_busy = false;
    QString m_trackId, m_title, m_artist, m_cover, m_error;
    quint64 m_sourceGeneration = 0, m_pendingGeneration = 0;
    QUrl m_source;
    qint64 m_pendingSeek = -1;
    qint64 m_duration = 0;
    QStringList m_history;
    int m_historyIndex = -1;
};

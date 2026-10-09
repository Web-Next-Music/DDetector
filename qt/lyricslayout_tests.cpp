#include "lyricslayout.h"


#include <QtQuickTest/quicktest.h>

// Exercise the same native measurement backend registered by the application.
class LyricsTestSetup : public QObject {
    Q_OBJECT
public slots:
    void applicationAvailable()
    {
        qmlRegisterType<LyricsLayout>("DDetector.Lyrics", 1, 0, "LyricsLayout");
    }

};

QUICK_TEST_MAIN_WITH_SETUP(lyricslayout, LyricsTestSetup)
#include "lyricslayout_tests.moc"

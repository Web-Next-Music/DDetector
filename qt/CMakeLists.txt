cmake_minimum_required(VERSION 3.21)
project(DDetectorQt VERSION 1.0 LANGUAGES CXX)
set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_EXPORT_COMPILE_COMMANDS ON)
# ContextMenu.menu requires 6.9; MultiEffect requires 6.5.
find_package(Qt6 6.9 REQUIRED COMPONENTS Quick QuickControls2 Network Multimedia Widgets Concurrent)
if(UNIX)
    find_package(qtermwidget6 REQUIRED CONFIG)
endif()
find_package(KF6SyntaxHighlighting REQUIRED CONFIG)
find_package(Qt6Keychain REQUIRED CONFIG)
find_package(PkgConfig REQUIRED)
pkg_check_modules(LIBGIT2 REQUIRED IMPORTED_TARGET libgit2>=1.5)
qt_standard_project_setup()
qt_add_executable(ddetector-qt workspace.cpp workspace.h uploader.cpp uploader.h main.cpp config.cpp config.h bridge.cpp bridge.h player.cpp player.h coverimageprovider.cpp coverimageprovider.h avatarimage.cpp avatarimage.h)
target_link_libraries(ddetector-qt PRIVATE Qt6::Quick Qt6::QuickControls2 Qt6::Network Qt6::Multimedia)
if(WIN32)
    enable_language(RC)
    target_sources(ddetector-qt PRIVATE app-icon.rc)
    target_include_directories(ddetector-qt PRIVATE "${CMAKE_CURRENT_SOURCE_DIR}")
    set_property(SOURCE app-icon.rc APPEND PROPERTY OBJECT_DEPENDS "${CMAKE_CURRENT_SOURCE_DIR}/assets/app-icon.ico")
endif()
file(GLOB APP_ASSETS CONFIGURE_DEPENDS RELATIVE "${CMAKE_CURRENT_SOURCE_DIR}" assets/*)
# Preserve qrc:/Main.qml and runtime native registrations until they can be
# migrated together. A QML module must not silently change the engine entry URL.
set(SHARED_QML
    qml/Components/SiteFocusRing.qml qml/Components/SiteIcon.qml qml/Components/SiteButton.qml
    qml/Components/SiteTextField.qml qml/Components/SiteComboBox.qml qml/Components/SiteSpinBox.qml
    qml/Components/SiteSettingsCard.qml qml/Components/SiteToggleRow.qml qml/Components/SiteSpinner.qml
    qml/Components/FileIcon.qml qml/Components/SiteMenu.qml qml/Components/SiteMenuItem.qml
    qml/Components/SiteMenuSeparator.qml qml/Components/SiteEditMenu.qml qml/Components/SiteDialog.qml
    qml/Components/SiteToolTip.qml qml/Components/DialogHeader.qml qml/Components/WindowButton.qml
    qml/Components/SiteQuote.qml qml/Components/SiteSectionCard.qml qml/Components/SiteSubtitle.qml
    qml/Components/ScrollEdgeShadow.qml qml/Components/NotificationStack.qml)
set(APP_QML
    ${SHARED_QML}
    qml/App/Main.qml
    qml/Censor/CensorDetector.qml qml/Censor/CensorSettingsDialog.qml qml/Censor/CensorSettingsPanel.qml
    qml/Git/GitWorkspace.qml qml/Git/terminal.qml
    qml/Library/ResultCard.qml qml/Library/SendDialog.qml qml/Library/SiteView.qml
    qml/Library/SpectrumVisualizer.qml qml/Library/TrackRow.qml)
foreach(qml_file IN LISTS APP_QML)
    get_filename_component(qml_alias "${qml_file}" NAME)
    set_source_files_properties("${qml_file}" PROPERTIES QT_RESOURCE_ALIAS "${qml_alias}")
endforeach()
qt_add_resources(ddetector-qt ui PREFIX "/" FILES ${APP_QML} ${APP_ASSETS})
get_target_property(QT_QMAKE_EXECUTABLE Qt6::qmake IMPORTED_LOCATION)
get_filename_component(QT_TOOL_DIR "${QT_QMAKE_EXECUTABLE}" DIRECTORY)
find_program(QT_QMLLINT NAMES qmllint HINTS "${QT_TOOL_DIR}" NO_DEFAULT_PATH REQUIRED)
set(QML_LINT_RESOURCE "${CMAKE_CURRENT_BINARY_DIR}/.qt/rcc/ui.qrc")
add_custom_target(qml-lint
    COMMAND "${QT_QMLLINT}" --resource "${QML_LINT_RESOURCE}" -W 0 qml/Components/SiteButton.qml qml/Components/SiteFocusRing.qml qml/Components/SiteTextField.qml qml/Components/SiteToggleRow.qml qml/Components/WindowButton.qml
    COMMAND "${QT_QMLLINT}" --resource "${QML_LINT_RESOURCE}" ${APP_QML}
    WORKING_DIRECTORY "${CMAKE_CURRENT_SOURCE_DIR}"
    COMMENT "Lint all QML (runtime context/native types may produce warnings)")
install(TARGETS ddetector-qt RUNTIME DESTINATION bin)
if(UNIX AND NOT APPLE)
    install(FILES ddetector-qt.desktop DESTINATION share/applications)
    install(FILES assets/app-icon.png DESTINATION share/pixmaps RENAME ddetector-qt.png)
endif()

include(CTest)
if(BUILD_TESTING)
    find_package(Qt6 6.9 REQUIRED COMPONENTS Test QuickTest)
    find_package(Python3 REQUIRED COMPONENTS Interpreter)

    add_test(NAME ui-harness COMMAND "${Python3_EXECUTABLE}" -B -m unittest discover
        -s "${CMAKE_CURRENT_SOURCE_DIR}/ui-tests" -p "test_*.py")
    set_tests_properties(ui-harness PROPERTIES TIMEOUT 30)
    add_test(NAME ui-smoke COMMAND "${Python3_EXECUTABLE}" -B "${CMAKE_CURRENT_SOURCE_DIR}/smoke.py"
        --executable $<TARGET_FILE:ddetector-qt> --output "${CMAKE_CURRENT_BINARY_DIR}/screenshots")
    set_tests_properties(ui-smoke PROPERTIES TIMEOUT 120)
    qt_add_executable(ddetector-uploader-tests uploader_tests.cpp uploader.cpp uploader.h)
    target_link_libraries(ddetector-uploader-tests PRIVATE Qt6::Network Qt6::Test)
    add_test(NAME local-uploader COMMAND ddetector-uploader-tests)
    qt_add_executable(ddetector-workspace-tests workspace.cpp workspace.h)
    target_compile_definitions(ddetector-workspace-tests PRIVATE GIT_WORKSPACE_TEST GIT_WORKSPACE_QML_TEST)
    target_link_libraries(ddetector-workspace-tests PRIVATE Qt6::Quick Qt6::QuickControls2 Qt6::Test)
    qt_add_resources(ddetector-workspace-tests workspace_assets PREFIX "/" FILES ${SHARED_QML} qml/Git/GitWorkspace.qml qml/Git/terminal.qml ${APP_ASSETS})
    add_test(NAME git-workspace COMMAND ddetector-workspace-tests)
    set_tests_properties(git-workspace PROPERTIES ENVIRONMENT "QT_QPA_PLATFORM=offscreen;QT_QUICK_BACKEND=software")
    qt_add_executable(ddetector-avatar-tests avatarimage_tests.cpp avatarimage.cpp avatarimage.h)
    target_link_libraries(ddetector-avatar-tests PRIVATE Qt6::Quick Qt6::Network Qt6::Test)
    add_test(NAME avatar-image COMMAND ddetector-avatar-tests)
    set_tests_properties(avatar-image PROPERTIES ENVIRONMENT "QT_QPA_PLATFORM=offscreen;QT_QUICK_BACKEND=software")
    qt_add_executable(ddetector-cover-tests coverimageprovider_tests.cpp coverimageprovider.cpp coverimageprovider.h)
    target_link_libraries(ddetector-cover-tests PRIVATE Qt6::Quick Qt6::Network Qt6::Test)
    add_test(NAME cover-provider COMMAND ddetector-cover-tests)
    set_tests_properties(cover-provider PROPERTIES ENVIRONMENT "QT_QPA_PLATFORM=offscreen")
    qt_add_executable(ddetector-player-tests player.cpp player.h)
    target_compile_definitions(ddetector-player-tests PRIVATE MUSIC_PLAYER_TEST)
    target_link_libraries(ddetector-player-tests PRIVATE Qt6::Network Qt6::Multimedia Qt6::Test)
    add_test(NAME native-player COMMAND ddetector-player-tests)
    set_tests_properties(native-player PROPERTIES ENVIRONMENT "QT_QPA_PLATFORM=offscreen")
    qt_add_executable(ddetector-qt-tests workspace.cpp workspace.h uploader.cpp uploader.h tests.cpp config.cpp config.h bridge.cpp bridge.h player.cpp player.h avatarimage.cpp avatarimage.h)
    target_link_libraries(ddetector-qt-tests PRIVATE Qt6::Quick Qt6::QuickControls2 Qt6::Network Qt6::Multimedia Qt6::Test)
    qt_add_resources(ddetector-qt-tests test_ui PREFIX "/" FILES ${APP_QML} ${APP_ASSETS})
    qt_add_resources(ddetector-qt-tests test_fixtures PREFIX "/" FILES test-fixtures/state.json)
    add_test(NAME native-ui COMMAND ddetector-qt-tests)
    set_tests_properties(native-ui PROPERTIES ENVIRONMENT "QT_QPA_PLATFORM=offscreen;QT_QUICK_BACKEND=software")
endif()

foreach(target IN ITEMS ddetector-qt ddetector-workspace-tests ddetector-qt-tests)
    if(TARGET ${target})
        target_sources(${target} PRIVATE syntaxhighlighter.cpp syntaxhighlighter.h terminalsession.cpp terminalsession.h terminal.cpp terminal.h lyricslayout.cpp lyricslayout.h)
        target_link_libraries(${target} PRIVATE Qt6::Widgets)
        if(UNIX)
            target_link_libraries(${target} PRIVATE qtermwidget6)
        endif()
    endif()
endforeach()
if(BUILD_TESTING)
    qt_add_executable(ddetector-terminal-tests terminal_tests.cpp terminal.cpp terminal.h terminalsession.cpp terminalsession.h workspace.cpp workspace.h)
    target_link_libraries(ddetector-terminal-tests PRIVATE Qt6::Quick Qt6::QuickControls2 Qt6::Widgets Qt6::Test)
    if(UNIX)
        target_link_libraries(ddetector-terminal-tests PRIVATE qtermwidget6)
    endif()
    qt_add_resources(ddetector-terminal-tests terminal_assets PREFIX "/" FILES ${SHARED_QML} qml/Git/terminal.qml ${APP_ASSETS})
    qt_add_executable(ddetector-syntax-tests syntaxhighlighter_tests.cpp syntaxhighlighter.cpp syntaxhighlighter.h)
    target_link_libraries(ddetector-syntax-tests PRIVATE Qt6::Quick Qt6::QuickControls2 Qt6::Test)
    add_test(NAME syntax-highlighter COMMAND ddetector-syntax-tests)
    set_tests_properties(syntax-highlighter PROPERTIES ENVIRONMENT "QT_QPA_PLATFORM=offscreen;QT_QUICK_BACKEND=software")
    qt_add_executable(ddetector-lyrics-tests lyricslayout_tests.cpp lyricslayout.cpp lyricslayout.h)
    target_link_libraries(ddetector-lyrics-tests PRIVATE Qt6::Quick Qt6::QuickControls2 Qt6::QuickTest)
    qt_add_resources(ddetector-lyrics-tests lyrics_ui PREFIX "/" FILES ${APP_QML} ${APP_ASSETS})
    add_test(NAME native-lyrics COMMAND ddetector-lyrics-tests -input "${CMAKE_CURRENT_SOURCE_DIR}/ui-tests" -import "${CMAKE_CURRENT_SOURCE_DIR}")
    set_tests_properties(native-lyrics PROPERTIES TIMEOUT 45
        ENVIRONMENT "QT_QPA_PLATFORM=offscreen;QT_QUICK_BACKEND=software")
    add_test(NAME embedded-terminal COMMAND ddetector-terminal-tests)
    set_tests_properties(embedded-terminal PROPERTIES ENVIRONMENT "QT_QPA_PLATFORM=offscreen;QT_QUICK_BACKEND=software")
endif()

foreach(target IN ITEMS ddetector-qt ddetector-qt-tests ddetector-workspace-tests ddetector-terminal-tests ddetector-syntax-tests)
    if(TARGET ${target})
        target_link_libraries(${target} PRIVATE KF6::SyntaxHighlighting PkgConfig::LIBGIT2 Qt6::Concurrent)
    endif()
endforeach()
foreach(target IN ITEMS ddetector-qt ddetector-qt-tests)
    if(TARGET ${target})
        target_link_libraries(${target} PRIVATE Qt6Keychain::Qt6Keychain)
    endif()
endforeach()

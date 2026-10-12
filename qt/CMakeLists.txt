cmake_minimum_required(VERSION 3.21)
project(DDetectorQt VERSION 1.0 LANGUAGES CXX)
set(DDETECTOR_GIT_TAG "" CACHE STRING "Git tag embedded in the application")
if(NOT DDETECTOR_GIT_TAG)
    execute_process(
        COMMAND git describe --tags --exact-match
        WORKING_DIRECTORY "${CMAKE_CURRENT_SOURCE_DIR}"
        RESULT_VARIABLE DDETECTOR_GIT_TAG_RESULT
        OUTPUT_VARIABLE DDETECTOR_GIT_TAG
        OUTPUT_STRIP_TRAILING_WHITESPACE
        ERROR_QUIET)
    if(NOT DDETECTOR_GIT_TAG_RESULT EQUAL 0)
        set(DDETECTOR_GIT_TAG "dev")
    endif()
endif()
configure_file(version.h.in generated/version.h @ONLY)
set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_EXPORT_COMPILE_COMMANDS ON)
if(NOT CMAKE_CXX_COMPILER_LAUNCHER)
    find_program(CCACHE_PROGRAM ccache)
    if(CCACHE_PROGRAM)
        set(CMAKE_CXX_COMPILER_LAUNCHER "${CMAKE_COMMAND}" -E env CCACHE_SLOPPINESS=pch_defines,time_macros "${CCACHE_PROGRAM}")
    endif()
endif()
if(UNIX AND NOT APPLE)
    include(CheckLinkerFlag)
    check_linker_flag(CXX -fuse-ld=lld DDETECTOR_HAS_LLD)
    if(DDETECTOR_HAS_LLD)
        add_link_options(-fuse-ld=lld)
    endif()
endif()
# ContextMenu.menu requires 6.9; MultiEffect requires 6.5.
file(STRINGS "${CMAKE_CURRENT_SOURCE_DIR}/../packaging/deps.env" DEPS_ENV_LINES)
foreach(deps_line IN LISTS DEPS_ENV_LINES)
    if(deps_line MATCHES "^([A-Z0-9_]+)=(.*)$")
        set(DEPS_${CMAKE_MATCH_1} "${CMAKE_MATCH_2}")
    endif()
endforeach()
find_package(Qt6 6.9 REQUIRED COMPONENTS Quick QuickControls2 Network Multimedia Widgets Concurrent)
if(UNIX)
    find_package(qtermwidget6 ${DEPS_QTERMWIDGET_VERSION} REQUIRED CONFIG)
    message(STATUS "qtermwidget6 ${qtermwidget6_VERSION}")
endif()
find_package(KF6SyntaxHighlighting ${DEPS_SYNTAX_HIGHLIGHTING_VERSION} REQUIRED CONFIG)
message(STATUS "Qt ${Qt6_VERSION}, KF6SyntaxHighlighting ${KF6SyntaxHighlighting_VERSION}")
find_package(Qt6Keychain REQUIRED CONFIG)
find_package(PkgConfig REQUIRED)
pkg_check_modules(LIBGIT2 REQUIRED IMPORTED_TARGET libgit2>=1.5)
qt_standard_project_setup()
qt_add_executable(ddetector-qt updater.cpp updater.h workspace.cpp workspace.h uploader.cpp uploader.h main.cpp config.cpp config.h bridge.cpp bridge.h player.cpp player.h coverimageprovider.cpp coverimageprovider.h avatarimage.cpp avatarimage.h)
target_include_directories(ddetector-qt PRIVATE "${CMAKE_CURRENT_BINARY_DIR}/generated")
target_link_libraries(ddetector-qt PRIVATE Qt6::Quick Qt6::QuickControls2 Qt6::Network Qt6::Multimedia)
if(WIN32)
    set_target_properties(ddetector-qt PROPERTIES WIN32_EXECUTABLE TRUE)
    enable_language(RC)
    target_sources(ddetector-qt PRIVATE app-icon.rc)
    target_include_directories(ddetector-qt PRIVATE "${CMAKE_CURRENT_SOURCE_DIR}")
    set_property(SOURCE app-icon.rc APPEND PROPERTY OBJECT_DEPENDS "${CMAKE_CURRENT_SOURCE_DIR}/assets/icons/app/app-icon.ico")
endif()
file(GLOB_RECURSE APP_ASSETS CONFIGURE_DEPENDS RELATIVE "${CMAKE_CURRENT_SOURCE_DIR}" assets/*)
list(REMOVE_ITEM APP_ASSETS assets/icons/lucide/lucide-flask-conical.svg)
set(DEV_QML)
if(CMAKE_BUILD_TYPE STREQUAL "Debug")
    target_compile_definitions(ddetector-qt PRIVATE DDETECTOR_DEV_TOOLS)
    list(APPEND APP_ASSETS assets/icons/lucide/lucide-flask-conical.svg)
    list(APPEND DEV_QML qml/App/DevTools.qml)
endif()
# Keep existing runtime URLs independent of the on-disk asset layout.
set(APP_ASSET_ALIASES)
foreach(asset IN LISTS APP_ASSETS)
    get_filename_component(asset_name "${asset}" NAME)
    if(asset_name IN_LIST APP_ASSET_ALIASES)
        message(FATAL_ERROR "Duplicate asset resource name: ${asset_name}")
    endif()
    list(APPEND APP_ASSET_ALIASES "${asset_name}")
    set_source_files_properties("${asset}" PROPERTIES QT_RESOURCE_ALIAS "assets/${asset_name}")
endforeach()
# Preserve qrc:/Main.qml and runtime native registrations until they can be
# migrated together. A QML module must not silently change the engine entry URL.
set(SHARED_QML
    qml/Components/SiteFocusRing.qml qml/Components/SiteIcon.qml qml/Components/SiteButton.qml
    qml/Components/SiteTextField.qml qml/Components/SiteComboBox.qml qml/Components/SiteSpinBox.qml
    qml/Components/SiteSettingsCard.qml qml/Components/SiteToggleRow.qml qml/Components/SiteSpinner.qml qml/Components/SiteProgressRing.qml
    qml/Components/FileIcon.qml qml/Components/SiteMenu.qml qml/Components/SiteMenuItem.qml
    qml/Components/SiteMenuSeparator.qml qml/Components/SiteEditMenu.qml qml/Components/SiteDialog.qml
    qml/Components/SiteToolTip.qml qml/Components/DialogHeader.qml qml/Components/WindowButton.qml
    qml/Components/SiteQuote.qml qml/Components/SiteSectionCard.qml qml/Components/SiteSubtitle.qml
    qml/Components/ScrollEdgeShadow.qml qml/Components/NotificationStack.qml)
set(APP_QML
    ${SHARED_QML} ${DEV_QML}
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
# Tooling-only foreign declarations use the actual automoc metadata. They are
# never linked into the application: main.cpp remains the registration owner.
qt_extract_metatypes(ddetector-qt)
get_target_property(APP_METATYPES ddetector-qt INTERFACE_QT_META_TYPES_BUILD_FILE)
set(NATIVE_QML_IMPORT_DIR "${CMAKE_CURRENT_BINARY_DIR}/qml-lint-imports")
set(NATIVE_QML_TYPE_TARGETS)
# The installed KF class is a QObject base, but KF does not ship QML metadata.
# Extract its real header rather than inventing a replacement prototype.
get_target_property(SYNTAX_INCLUDE_DIRS KF6::SyntaxHighlighting INTERFACE_INCLUDE_DIRECTORIES)
find_path(SYNTAX_HEADER_DIR KSyntaxHighlighting/syntaxhighlighter.h
    HINTS ${SYNTAX_INCLUDE_DIRS} REQUIRED)
qt_add_library(ddetector-lint-kf STATIC EXCLUDE_FROM_ALL
    "${SYNTAX_HEADER_DIR}/KSyntaxHighlighting/syntaxhighlighter.h")
target_link_libraries(ddetector-lint-kf PRIVATE KF6::SyntaxHighlighting)
qt_extract_metatypes(ddetector-lint-kf)
get_target_property(SYNTAX_METATYPES ddetector-lint-kf INTERFACE_QT_META_TYPES_BUILD_FILE)
foreach(module IN ITEMS Terminal Syntax Lyrics Avatar)
    if(module STREQUAL "Terminal")
        set(uri DDetector.Terminal)
        set(declarations [=[
#include "terminal.h"
struct TerminalForeign {
    Q_GADGET
    QML_FOREIGN(EmbeddedTerminal)
    QML_NAMED_ELEMENT(EmbeddedTerminal)
};
struct SessionForeign {
    Q_GADGET
    QML_FOREIGN(TerminalSession)
    QML_NAMED_ELEMENT(TerminalSession)
    QML_UNCREATABLE("Owned by GitWorkspace")
};
]=])
    elseif(module STREQUAL "Syntax")
        set(uri DDetector.Syntax)
        set(declarations [=[
#include "syntaxhighlighter.h"
struct SyntaxForeign {
    Q_GADGET
    QML_FOREIGN(SyntaxHighlighter)
    QML_NAMED_ELEMENT(SyntaxHighlighter)
};
]=])
    elseif(module STREQUAL "Lyrics")
        set(uri DDetector.Lyrics)
        set(declarations [=[
#include "lyricslayout.h"
struct LyricsForeign {
    Q_GADGET
    QML_FOREIGN(LyricsLayout)
    QML_NAMED_ELEMENT(LyricsLayout)
};
]=])
    else()
        set(uri DDetector)
        set(declarations [=[
#include "avatarimage.h"
struct AvatarForeign {
    Q_GADGET
    QML_FOREIGN(AvatarImage)
    QML_NAMED_ELEMENT(AvatarImage)
};
]=])
    endif()
    set(metadata_target "ddetector-lint-${module}")
    set(metadata_header "${CMAKE_CURRENT_BINARY_DIR}/generated/${metadata_target}.h")

    file(CONFIGURE OUTPUT "${metadata_header}" CONTENT "#pragma once\n#include <QtQml/qqmlregistration.h>\n${declarations}" @ONLY)
    qt_add_library(${metadata_target} STATIC EXCLUDE_FROM_ALL)
    string(REPLACE "." "/" module_path "${uri}")
    qt_add_qml_module(${metadata_target} URI ${uri} VERSION 1.0
        NO_PLUGIN RESOURCE_PREFIX /qt/qml OUTPUT_DIRECTORY "${NATIVE_QML_IMPORT_DIR}/${module_path}"
        DEPENDENCIES QtQuick
        SOURCES "${metadata_header}" "${APP_METATYPES}")
    if(module STREQUAL "Syntax")
        target_sources(${metadata_target} PRIVATE "${SYNTAX_METATYPES}")
    endif()
    target_include_directories(${metadata_target} PRIVATE "${CMAKE_CURRENT_SOURCE_DIR}")
    target_link_libraries(${metadata_target} PRIVATE Qt6::Quick KF6::SyntaxHighlighting)
    list(APPEND NATIVE_QML_TYPE_TARGETS ${metadata_target}_qmltyperegistration)
endforeach()
add_custom_target(qml-lint
    COMMAND "${QT_QMLLINT}" --resource "${QML_LINT_RESOURCE}" -W 0 qml/Components/SiteButton.qml qml/Components/SiteFocusRing.qml qml/Components/SiteTextField.qml qml/Components/SiteToggleRow.qml qml/Components/WindowButton.qml
    COMMAND "${QT_QMLLINT}" --ignore-settings -W 0 -I "${NATIVE_QML_IMPORT_DIR}" --resource "${QML_LINT_RESOURCE}"
            --json "${CMAKE_CURRENT_BINARY_DIR}/qml-lint.json" ${APP_QML}
    WORKING_DIRECTORY "${CMAKE_CURRENT_SOURCE_DIR}"
    DEPENDS ${NATIVE_QML_TYPE_TARGETS}
    COMMENT "Lint all QML with native type metadata")

install(TARGETS ddetector-qt RUNTIME DESTINATION bin)
if(UNIX AND NOT APPLE)
    install(FILES ddetector-qt.desktop DESTINATION share/applications)
    install(FILES assets/icons/app/app-icon.png DESTINATION share/pixmaps RENAME ddetector-qt.png)
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
    qt_add_executable(ddetector-updater-tests updater_tests.cpp updater.cpp updater.h)
    target_include_directories(ddetector-updater-tests PRIVATE "${CMAKE_CURRENT_BINARY_DIR}/generated")
    target_link_libraries(ddetector-updater-tests PRIVATE Qt6::Network Qt6::Gui Qt6::Test)
    if(CMAKE_BUILD_TYPE STREQUAL "Debug")
        target_compile_definitions(ddetector-updater-tests PRIVATE DDETECTOR_DEV_TOOLS)
    endif()
    add_test(NAME native-updater COMMAND ddetector-updater-tests)
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
foreach(target IN ITEMS ddetector-qt ddetector-qt-tests ddetector-workspace-tests)
    if(TARGET ${target})
        target_precompile_headers(${target} PRIVATE
            <QObject> <QString> <QStringList> <QVariantList> <QTimer> <QFile> <QDir> <QUrl>
            <QJsonArray> <QJsonDocument> <QJsonObject> <QNetworkAccessManager> <QNetworkReply>
            <QPointer> <algorithm> <functional> <memory>)
    endif()
endforeach()

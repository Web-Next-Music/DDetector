Unicode true
SetCompressor /SOLID lzma
!include "FileFunc.nsh"
Var RestartApp

Function .onInit
  StrCpy $RestartApp 0
  ${GetParameters} $R0
  ClearErrors
  ${GetOptions} $R0 "/RESTARTAPP" $R1
  IfErrors restart_not_requested
  StrCpy $RestartApp 1
restart_not_requested:
FunctionEnd

Function .onInstSuccess
  StrCmp $RestartApp 1 0 restart_done
  ExecShell "open" "$INSTDIR\ddetector.exe" '--qt "$INSTDIR\ddetector-qt.exe"'
restart_done:
FunctionEnd

!ifndef VERSION
  !define VERSION "0.0.0"
!endif
!ifndef VERSION_NUM
  !define VERSION_NUM "0.0.0.0"
!endif
!ifndef DIST_DIR
  !error "DIST_DIR must point to the deployed application directory"
!endif
!ifndef OUTPUT_FILE
  !define OUTPUT_FILE "DDetector-Setup.exe"
!endif

Name "DDetector"
OutFile "${OUTPUT_FILE}"
InstallDir "$LOCALAPPDATA\Programs\DDetector"
InstallDirRegKey HKCU "Software\DDetector" "InstallLocation"
RequestExecutionLevel user

VIProductVersion "${VERSION_NUM}"
VIAddVersionKey "ProductName" "DDetector"
VIAddVersionKey "FileDescription" "DDetector installer"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "ProductVersion" "${VERSION}"
VIAddVersionKey "LegalCopyright" "DDetector contributors"

Page directory
Page instfiles
UninstPage uninstConfirm
UninstPage instfiles

Section "DDetector" SEC_MAIN
  SetOutPath "$INSTDIR"
  File /r "${DIST_DIR}\*"
  WriteRegStr HKCU "Software\DDetector" "InstallLocation" "$INSTDIR"
  WriteUninstaller "$INSTDIR\Uninstall.exe"

  CreateDirectory "$SMPROGRAMS\DDetector"
  CreateShortcut "$SMPROGRAMS\DDetector\DDetector.lnk" "$INSTDIR\ddetector.exe" '--qt "$INSTDIR\ddetector-qt.exe"' "$INSTDIR\ddetector.exe"
  CreateShortcut "$DESKTOP\DDetector.lnk" "$INSTDIR\ddetector.exe" '--qt "$INSTDIR\ddetector-qt.exe"' "$INSTDIR\ddetector.exe"

  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\DDetector" "DisplayName" "DDetector"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\DDetector" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\DDetector" "DisplayIcon" "$INSTDIR\ddetector.exe"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\DDetector" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\DDetector" "UninstallString" '"$INSTDIR\Uninstall.exe"'
  WriteRegDWORD HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\DDetector" "NoModify" 1
  WriteRegDWORD HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\DDetector" "NoRepair" 1
SectionEnd

Section "Uninstall"
  Delete "$DESKTOP\DDetector.lnk"
  RMDir /r "$SMPROGRAMS\DDetector"
  RMDir /r "$INSTDIR"
  DeleteRegKey HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\DDetector"
  DeleteRegKey HKCU "Software\DDetector"
SectionEnd

Unicode true
SetCompressor /SOLID lzma

!ifndef VERSION
  !define VERSION "0.0.0"
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

VIProductVersion "${VERSION}.0"
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

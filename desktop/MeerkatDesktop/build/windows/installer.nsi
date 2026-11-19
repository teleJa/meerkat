Unicode true
!include "MUI2.nsh"
!include "LogicLib.nsh"

!define APP_NAME "Meerkat"
!define APP_VERSION "0.1.0"
!define APP_EXE "Meerkat.exe"
!define COMPANY "Meerkat"
!define UNINST_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\Meerkat"
!define RUN_KEY "Software\Microsoft\Windows\CurrentVersion\Run"

Name "${APP_NAME}"
Caption "${APP_NAME} ${APP_VERSION} 安装程序"
OutFile "..\..\bin\Meerkat-Setup-${APP_VERSION}.exe"
InstallDir "$LOCALAPPDATA\Programs\Meerkat"
InstallDirRegKey HKCU "Software\Meerkat" "InstallDir"
RequestExecutionLevel user
SetCompressor /SOLID lzma
SetCompressorDictSize 64
CRCCheck on

!define MUI_ICON "icon.ico"
!define MUI_UNICON "icon.ico"
!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_RUN "$INSTDIR\${APP_EXE}"
!define MUI_FINISHPAGE_RUN_TEXT "启动 Meerkat"
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "SimpChinese"
!insertmacro MUI_LANGUAGE "English"

VIProductVersion "${APP_VERSION}.0"
VIFileVersion "${APP_VERSION}.0"
VIAddVersionKey "ProductName" "${APP_NAME}"
VIAddVersionKey "CompanyName" "${COMPANY}"
VIAddVersionKey "FileDescription" "${APP_NAME} 安装程序"
VIAddVersionKey "FileVersion" "${APP_VERSION}"
VIAddVersionKey "ProductVersion" "${APP_VERSION}"
VIAddVersionKey "LegalCopyright" "© 2026 Meerkat"

Section "Meerkat" SecMain
  SetShellVarContext current
  ; 升级安装时结束旧进程，避免文件占用。
  nsExec::ExecToLog 'taskkill /IM "${APP_EXE}" /F /T'
  Sleep 800
  SetOutPath "$INSTDIR"
  File "..\..\bin\Meerkat-Windows\${APP_EXE}"
  File "..\..\bin\Meerkat-Windows\meerkat-edge.exe"
  WriteUninstaller "$INSTDIR\Uninstall.exe"

  CreateShortcut "$SMPROGRAMS\Meerkat.lnk" "$INSTDIR\${APP_EXE}"
  CreateShortcut "$DESKTOP\Meerkat.lnk" "$INSTDIR\${APP_EXE}"

  ; 用户登录时自动启动（HKCU Run，无需管理员）。
  WriteRegStr HKCU "${RUN_KEY}" "Meerkat" '"$INSTDIR\${APP_EXE}"'
  WriteRegStr HKCU "Software\Meerkat" "InstallDir" "$INSTDIR"

  WriteRegStr HKCU "${UNINST_KEY}" "DisplayName" "${APP_NAME}"
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayVersion" "${APP_VERSION}"
  WriteRegStr HKCU "${UNINST_KEY}" "Publisher" "${COMPANY}"
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayIcon" "$INSTDIR\${APP_EXE}"
  WriteRegStr HKCU "${UNINST_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINST_KEY}" "UninstallString" '"$INSTDIR\Uninstall.exe"'
  WriteRegStr HKCU "${UNINST_KEY}" "QuietUninstallString" '"$INSTDIR\Uninstall.exe" /S'
  WriteRegDWORD HKCU "${UNINST_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINST_KEY}" "NoRepair" 1
SectionEnd

Section "Uninstall"
  SetShellVarContext current
  nsExec::ExecToLog 'taskkill /IM "${APP_EXE}" /F /T'
  Sleep 500
  DeleteRegValue HKCU "${RUN_KEY}" "Meerkat"
  DeleteRegKey HKCU "${UNINST_KEY}"
  DeleteRegKey HKCU "Software\Meerkat"
  Delete "$SMPROGRAMS\Meerkat.lnk"
  Delete "$DESKTOP\Meerkat.lnk"
  RMDir /r "$INSTDIR"
  ; 保留 $APPDATA\Meerkat 用户配置与 Edge 身份，便于重装后继续使用。
SectionEnd

; Agent Mask - per-user installer (no admin rights needed).
Unicode true
Target amd64-unicode
!define APP "Agent Mask"
!ifndef VER
  !define VER "0.1.3"
!endif
!define UNINST "Software\Microsoft\Windows\CurrentVersion\Uninstall\AgentMask"
Name "${APP} ${VER}"
OutFile "AgentMask-Setup-${VER}.exe"
RequestExecutionLevel user
InstallDir "$LOCALAPPDATA\Programs\Agent Mask"
SetCompressor /SOLID lzma
!include "MUI2.nsh"
!define MUI_ICON "${NSISDIR}\Contrib\Graphics\Icons\modern-install.ico"
!define MUI_UNICON "${NSISDIR}\Contrib\Graphics\Icons\modern-uninstall.ico"
!define MUI_WELCOMEPAGE_TITLE "${APP} ${VER}"
!define MUI_WELCOMEPAGE_TEXT "Hide emails, phone numbers, keys and passwords in a whole folder of videos and screenshots, checked again, with a report. Free and open source (MIT).$\r$\n$\r$\nInstalls for your account only, no admin rights needed."
!define MUI_FINISHPAGE_RUN "powershell.exe"
!define MUI_FINISHPAGE_RUN_PARAMETERS "-NoProfile -ExecutionPolicy Bypass -File $\"$INSTDIR\setup-deps.ps1$\""
!define MUI_FINISHPAGE_RUN_TEXT "Check and install ffmpeg and Tesseract (asks first)"
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_LICENSE "LICENSE"
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"

Section "Install"
  SetOutPath "$INSTDIR"
  File "agent-mask.exe"
  File "path.ps1"
  File "setup-deps.ps1"
  File "README.md"
  File "LICENSE"
  nsExec::ExecToLog 'powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$INSTDIR\path.ps1" -Dir "$INSTDIR" -Add'
  WriteUninstaller "$INSTDIR\Uninstall.exe"
  CreateDirectory "$SMPROGRAMS\${APP}"
  CreateShortcut "$SMPROGRAMS\${APP}\Agent Mask (command line).lnk" "$SYSDIR\cmd.exe" '/k "$INSTDIR\agent-mask.exe"'
  CreateShortcut "$SMPROGRAMS\${APP}\Check components.lnk" "powershell.exe" '-NoProfile -ExecutionPolicy Bypass -File "$INSTDIR\setup-deps.ps1"'
  CreateShortcut "$SMPROGRAMS\${APP}\Uninstall Agent Mask.lnk" "$INSTDIR\Uninstall.exe"
  WriteRegStr HKCU "${UNINST}" "DisplayName" "${APP}"
  WriteRegStr HKCU "${UNINST}" "DisplayVersion" "${VER}"
  WriteRegStr HKCU "${UNINST}" "Publisher" "Agent Mask"
  WriteRegStr HKCU "${UNINST}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINST}" "UninstallString" '"$INSTDIR\Uninstall.exe"'
  WriteRegDWORD HKCU "${UNINST}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINST}" "NoRepair" 1
SectionEnd

Section "Uninstall"
  nsExec::ExecToLog 'powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$INSTDIR\path.ps1" -Dir "$INSTDIR" -Remove'
  Delete "$INSTDIR\agent-mask.exe"
  Delete "$INSTDIR\path.ps1"
  Delete "$INSTDIR\setup-deps.ps1"
  Delete "$INSTDIR\README.md"
  Delete "$INSTDIR\LICENSE"
  Delete "$INSTDIR\Uninstall.exe"
  RMDir "$INSTDIR"
  RMDir /r "$SMPROGRAMS\${APP}"
  DeleteRegKey HKCU "${UNINST}"
SectionEnd

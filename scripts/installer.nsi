; freshbreath Windows installer.
;
; Installs freshbreath as a machine-wide, auto-starting Windows service under
; NT AUTHORITY\LocalService, using NSSM as the service wrapper (freshbreath.exe
; itself is a plain foreground process and doesn't speak the SCM protocol).
;
; Expects these preprocessor defines from the command line:
;   /DVERSION=1.2.3
;   /DARCH=x64            (or arm64 - display purposes only)
;   /DOUTFILE=..\dist\freshbreath-1.2.3-windows-x64-setup.exe
;
; Expects the payload already staged at ..\dist\nsis-staging\ relative to this
; script, containing: freshbreath.exe, nssm.exe, README.txt, and the web and
; skills directories.

!ifndef VERSION
  !define VERSION "0.0.0-dev"
!endif
!ifndef ARCH
  !define ARCH "x64"
!endif
!ifndef OUTFILE
  !define OUTFILE "..\dist\freshbreath-${VERSION}-windows-${ARCH}-setup.exe"
!endif

!define STAGING "..\dist\nsis-staging"
!define SERVICE_NAME "Fresh Breath Service"
!define SERVICE_ACCOUNT "NT AUTHORITY\LocalService"
!define UNINST_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\freshbreath"

; URL the finish-page checkbox opens in the user's default browser. Matches
; the default FRBR_BASE_URL / FRBR_LISTEN_ADDR (:9009) documented in README.md.
; Defaulted to http:// for v1 - if FRBR_TLS_CERT/FRBR_TLS_KEY are configured the
; base URL becomes https://localhost:9009, which the installer can't easily
; detect at install time. Guarded so a future follow-up (or a build that knows
; the scheme) can pass /DFRESHBREATH_URL=https://localhost:9009 from the
; makensis command line without editing this script.
!ifndef FRESHBREATH_URL
  !define FRESHBREATH_URL "http://localhost:9009"
!endif

!include "MUI2.nsh"
!include "LogicLib.nsh"

Name "Fresh Breath"
OutFile "${OUTFILE}"
Unicode true
RequestExecutionLevel admin
InstallDir "$PROGRAMFILES64\Fresh Breath"
InstallDirRegKey HKLM "Software\freshbreath" "InstallDir"

Var DataDir

!define MUI_ABORTWARNING

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES

; Finish-page "launch" checkbox. MUI_FINISHPAGE_RUN must be defined (even
; empty) for the checkbox to render; MUI_FINISHPAGE_RUN_FUNCTION redirects the
; checkbox's action to the LaunchFreshBreath function below, which opens the
; URL via the shell "open" verb (ExecShell) rather than launching a bare
; process. The checkbox is checked by default - friendly for a personal app
; server the user just installed.
!define MUI_FINISHPAGE_RUN
!define MUI_FINISHPAGE_RUN_TEXT "Launch Fresh Breath now"
!define MUI_FINISHPAGE_RUN_FUNCTION LaunchFreshBreath
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"

Function .onInit
  ; SetRegView is a runtime instruction, so it can't live at top level - it
  ; has to be set here, early, since it also governs the InstallDirRegKey
  ; lookup that happens once the directory page initializes.
  SetRegView 64
  ; NSIS has no built-in $PROGRAMDATA constant. SetShellVarContext all makes
  ; $APPDATA resolve to the machine-wide C:\ProgramData instead of the
  ; per-user roaming AppData folder.
  SetShellVarContext all
  StrCpy $DataDir "$APPDATA\freshbreath"
FunctionEnd

; Invoked by the finish-page "Launch Fresh Breath now" checkbox (see the
; MUI_FINISHPAGE_RUN_FUNCTION define above). The service was started near the
; end of the install section via nssm, but the browser can beat the process
; to binding :9009; a brief sleep softens the race. If the user is quicker
; than the sleep they can just refresh.
Function LaunchFreshBreath
  Sleep 2000
  ExecShell "open" "${FRESHBREATH_URL}"
FunctionEnd

Section "freshbreath" SecMain
  SectionIn RO
  SetOutPath "$INSTDIR"

  File "${STAGING}\freshbreath.exe"
  File "${STAGING}\nssm.exe"
  File "${STAGING}\README.txt"
  File /r "${STAGING}\web"
  File /r "${STAGING}\skills"

  SetOutPath "$DataDir"
  ; Optional out-of-band payload (see build-installer.sh).
  File /r "${STAGING}\data\*.*"

  ; Data directory: LocalService needs an explicit grant, ProgramData's
  ; default ACLs only give BUILTIN\Users read & execute.
  nsExec::ExecToLog 'icacls "$DataDir" /grant "${SERVICE_ACCOUNT}:(OI)(CI)M"'
  Pop $0

  ; Stop/remove any previous install of the service so re-running the
  ; installer (e.g. an upgrade) doesn't fail on an already-registered name.
  nsExec::ExecToLog '"$INSTDIR\nssm.exe" stop "${SERVICE_NAME}"'
  Pop $0
  nsExec::ExecToLog '"$INSTDIR\nssm.exe" remove "${SERVICE_NAME}" confirm'
  Pop $0

  nsExec::ExecToLog '"$INSTDIR\nssm.exe" install "${SERVICE_NAME}" "$INSTDIR\freshbreath.exe"'
  Pop $0
  ${If} $0 != 0
    DetailPrint "nssm install failed (exit $0)"
    Abort "Could not install the Fresh Breath service."
  ${EndIf}

  nsExec::ExecToLog '"$INSTDIR\nssm.exe" set "${SERVICE_NAME}" AppDirectory "$INSTDIR"'
  Pop $0
  nsExec::ExecToLog '"$INSTDIR\nssm.exe" set "${SERVICE_NAME}" AppEnvironmentExtra "FRBR_DATA_DIR=$DataDir"'
  Pop $0
  nsExec::ExecToLog '"$INSTDIR\nssm.exe" set "${SERVICE_NAME}" AppStdout "$DataDir\freshbreath.log"'
  Pop $0
  nsExec::ExecToLog '"$INSTDIR\nssm.exe" set "${SERVICE_NAME}" AppStderr "$DataDir\freshbreath.log"'
  Pop $0
  nsExec::ExecToLog '"$INSTDIR\nssm.exe" set "${SERVICE_NAME}" AppRotateFiles 1'
  Pop $0
  nsExec::ExecToLog '"$INSTDIR\nssm.exe" set "${SERVICE_NAME}" DisplayName "Fresh Breath"'
  Pop $0
  nsExec::ExecToLog '"$INSTDIR\nssm.exe" set "${SERVICE_NAME}" Description "Fresh Breath personal app server and MCP gateway"'
  Pop $0
  nsExec::ExecToLog '"$INSTDIR\nssm.exe" set "${SERVICE_NAME}" Start SERVICE_AUTO_START'
  Pop $0
  nsExec::ExecToLog '"$INSTDIR\nssm.exe" set "${SERVICE_NAME}" ObjectName "${SERVICE_ACCOUNT}" ""'
  Pop $0
  ${If} $0 != 0
    DetailPrint "nssm set ObjectName failed (exit $0)"
    Abort "Could not configure the Fresh Breath service account."
  ${EndIf}

  nsExec::ExecToLog '"$INSTDIR\nssm.exe" start "${SERVICE_NAME}"'
  Pop $0

  WriteRegStr HKLM "Software\freshbreath" "InstallDir" "$INSTDIR"
  WriteRegStr HKLM "Software\freshbreath" "DataDir" "$DataDir"

  WriteRegStr HKLM "${UNINST_KEY}" "DisplayName" "Fresh Breath"
  WriteRegStr HKLM "${UNINST_KEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKLM "${UNINST_KEY}" "Publisher" "Poggers Institute"
  WriteRegStr HKLM "${UNINST_KEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegStr HKLM "${UNINST_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegDWORD HKLM "${UNINST_KEY}" "NoModify" 1
  WriteRegDWORD HKLM "${UNINST_KEY}" "NoRepair" 1

  WriteUninstaller "$INSTDIR\uninstall.exe"
SectionEnd

Function un.onInit
  SetRegView 64
FunctionEnd

Section "Uninstall"
  nsExec::ExecToLog '"$INSTDIR\nssm.exe" stop "${SERVICE_NAME}"'
  Pop $0
  nsExec::ExecToLog '"$INSTDIR\nssm.exe" remove "${SERVICE_NAME}" confirm'
  Pop $0

  Delete "$INSTDIR\freshbreath.exe"
  Delete "$INSTDIR\nssm.exe"
  Delete "$INSTDIR\README.txt"
  Delete "$INSTDIR\uninstall.exe"
  RMDir /r "$INSTDIR\web"
  RMDir /r "$INSTDIR\skills"
  RMDir "$INSTDIR"

  ; Data directory (freshbreath.db, logs) is left in place intentionally -
  ; uninstalling shouldn't destroy the user's data. They can remove
  ; %PROGRAMDATA%\freshbreath by hand if they really want a clean slate.

  DeleteRegKey HKLM "${UNINST_KEY}"
  DeleteRegKey HKLM "Software\freshbreath"
SectionEnd

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
Var IsUpgrade

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

  ; Detect an existing install: is the service already registered? sc query
  ; returns 0 if the service exists (any state — running, stopped, disabled),
  ; non-zero if it doesn't. A registered service means a previous install,
  ; and we upgrade it in place — preserving its nssm tuning — instead of the
  ; old nuclear stop/remove/re-register path that lost every `nssm set` the
  ; operator had applied by hand.
  StrCpy $IsUpgrade 0
  nsExec::ExecToLog 'sc query "${SERVICE_NAME}"'
  Pop $0
  ${If} $0 == 0
    StrCpy $IsUpgrade 1
    DetailPrint "Existing Fresh Breath service found — upgrading in place."
  ${Else}
    DetailPrint "No existing service — fresh install."
  ${EndIf}

  ; On upgrade, stop the running service BEFORE copying files. Windows
  ; locks a running .exe against overwrite, and the service holds
  ; freshbreath.exe open while it runs. The old nssm.exe is still on disk
  ; at this point, so use it to stop the service. nssm stop sends a graceful
  ; stop control; if the service won't die within nssm's window, taskkill /F
  ; is the force fallback (logged so a stubborn stop isn't a silent mystery).
  ${If} $IsUpgrade == 1
    nsExec::ExecToLog '"$INSTDIR\nssm.exe" stop "${SERVICE_NAME}"'
    Pop $0
    ${If} $0 != 0
      DetailPrint "nssm stop did not confirm (exit $0) — taskkill fallback"
      nsExec::ExecToLog 'taskkill /F /IM freshbreath.exe'
      Pop $0
    ${EndIf}
  ${EndIf}

  ; Rollback artifact: rename the previous binary out of the way before the
  ; new copy lands. A failed File copy then leaves freshbreath.exe.old for
  ; manual recovery instead of a missing binary the service points at. The
  ; stale .old from a prior upgrade is cleared first, so at most one lives
  ; here at a time. (Rename works even on a just-stopped service's exe; on a
  ; fresh install there's nothing to rename, so this block is skipped.)
  ${If} $IsUpgrade == 1
    Delete "$INSTDIR\freshbreath.exe.old"
    Rename "$INSTDIR\freshbreath.exe" "$INSTDIR\freshbreath.exe.old"
  ${EndIf}

  ; Copy payload. On upgrade this overwrites everything except the .old.
  SetOutPath "$INSTDIR"
  File "${STAGING}\freshbreath.exe"
  File "${STAGING}\nssm.exe"
  File "${STAGING}\README.txt"
  File /r "${STAGING}\web"
  File /r "${STAGING}\skills"

  ; Data dir: safe on both paths. File /r only writes files from the
  ; staging archive (just a README.txt from build-installer.sh); it never
  ; deletes existing files, so the runtime-created DB (freshbreath.db) and
  ; uploaded app content are untouched on upgrade.
  SetOutPath "$DataDir"
  ; Optional out-of-band payload (see build-installer.sh).
  File /r "${STAGING}\data\*.*"

  ; Data directory ACL: LocalService needs an explicit grant — ProgramData's
  ; default ACLs only give BUILTIN\Users read & execute. Harmless to re-run
  ; on upgrade (re-asserts the same grant).
  nsExec::ExecToLog 'icacls "$DataDir" /grant "${SERVICE_ACCOUNT}:(OI)(CI)M"'
  Pop $0

  ; Fresh install only: register the service and apply all nssm settings.
  ; Upgrade skips this entire block — the service is already registered
  ; and the operator's nssm tuning (anything set by hand after the first
  ; install, or any future knob we don't know about yet) is preserved. No
  ; nssm remove, no re-install, no re-set.
  ${If} $IsUpgrade == 0
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
  ${EndIf}

  ; Start (or restart) the service. On a fresh install this starts the
  ; just-registered service; on upgrade this restarts the preserved service
  ; with the new binary. The start type (auto-start) was set on the fresh
  ; install and is held by the SCM, so it survives the upgrade untouched.
  nsExec::ExecToLog '"$INSTDIR\nssm.exe" start "${SERVICE_NAME}"'
  Pop $0

  ; Registry writes: unconditional (both fresh and upgrade). The version
  ; bump and install-location refresh run on every pass, so an upgrade over
  ; vN rewrites DisplayVersion to vN+1 and refreshes the uninstall key.
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
  Delete "$INSTDIR\freshbreath.exe.old"
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

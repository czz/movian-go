; movian-go.nsi — NSIS installer for the Windows CEF package.
; Usage: makensis -DARCH=amd64 -DVERSION=8.0.0 -DSRCDIR=<pkgdir> \
;                 -DOUTFILE=movian-go-windows-amd64-setup.exe movian-go.nsi
; SRCDIR must contain the exe + the CEF runtime files (flat layout).

!ifndef ARCH
  !define ARCH amd64
!endif
!if ${ARCH} == amd64
  !ifndef EXENAME
    !define EXENAME movian-go.exe
  !endif
  !define REGVIEW 64
  !define PROGFILES $PROGRAMFILES64
!else
  !ifndef EXENAME
    !define EXENAME movian-go32.exe
  !endif
  !define REGVIEW 32
  !define PROGFILES $PROGRAMFILES32
!endif
!ifndef VERSION
  !define VERSION 0.0.0
!endif
!ifndef SRCDIR
  !error "SRCDIR (package dir with exe + CEF) is required"
!endif
!ifndef OUTFILE
  !define OUTFILE movian-go-setup.exe
!endif

!define APPNAME "Movian Go"
!define PUBLISHER "Movian Go Project"
!define UNINSTKEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\MovianGo"

Name "${APPNAME} ${VERSION}"
OutFile "${OUTFILE}"
Icon "..\res\movian-go.ico"
UninstallIcon "..\res\movian-go.ico"

InstallDir "${PROGFILES}\${APPNAME}"
RequestExecutionLevel admin
SetCompressor /SOLID lzma
ShowInstDetails show
ShowUninstDetails show

; Modern UI
!include "MUI2.nsh"
!define MUI_ABORTWARNING
!define MUI_ICON "..\res\movian-go.ico"
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"
!insertmacro MUI_LANGUAGE "Italian"

Section "Install" SecMain
  SetRegView ${REGVIEW}
  SetOutPath "$INSTDIR"
  ; exe + CEF runtime (dlls, paks, subdirs like locales/)
  File /r "${SRCDIR}\*.*"

  WriteRegStr HKLM "Software\${APPNAME}" "InstallDir" "$INSTDIR"
  WriteRegStr HKLM "${UNINSTKEY}" "DisplayName" "${APPNAME}"
  WriteRegStr HKLM "${UNINSTKEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKLM "${UNINSTKEY}" "Publisher" "${PUBLISHER}"
  WriteRegStr HKLM "${UNINSTKEY}" "DisplayIcon" '"$INSTDIR\${EXENAME}"'
  WriteRegStr HKLM "${UNINSTKEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegDWORD HKLM "${UNINSTKEY}" "NoModify" 1
  WriteRegDWORD HKLM "${UNINSTKEY}" "NoRepair" 1

  WriteUninstaller "$INSTDIR\uninstall.exe"

  CreateDirectory "$SMPROGRAMS\${APPNAME}"
  CreateShortcut "$SMPROGRAMS\${APPNAME}\${APPNAME}.lnk" \
    "$INSTDIR\${EXENAME}" "" "$INSTDIR\${EXENAME}" 0
  CreateShortcut "$DESKTOP\${APPNAME}.lnk" \
    "$INSTDIR\${EXENAME}" "" "$INSTDIR\${EXENAME}" 0
SectionEnd

Section "Uninstall"
  SetRegView ${REGVIEW}
  Delete "$DESKTOP\${APPNAME}.lnk"
  Delete "$SMPROGRAMS\${APPNAME}\${APPNAME}.lnk"
  RMDir "$SMPROGRAMS\${APPNAME}"
  Delete "$INSTDIR\uninstall.exe"
  RMDir /r "$INSTDIR"
  DeleteRegKey HKLM "${UNINSTKEY}"
  DeleteRegKey HKLM "Software\${APPNAME}"
SectionEnd

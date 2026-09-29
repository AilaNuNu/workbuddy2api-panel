; wb2api-desktop.nsi — WorkBuddy2API 桌面版 Windows 安装包（NSIS）
;
; 设计取舍：
;  1. **按用户安装**（$LOCALAPPDATA\Programs，RequestExecutionLevel user）：不弹 UAC，
;     普通用户可装卸。开机自启本来就写 HKCU（每用户），两者一致；若按机器安装，
;     自启就变成「所有用户各写一份」的语义，反而更绕。
;  2. **不把自启做成安装选项**：自启的唯一开关是托盘的「开机自启」（读/写 HKCU 下
;     workbuddy2api 项）。安装器再放一个勾选会造出第二个写同一注册表值的地方，
;     两边判断逻辑一旦分叉就是「装完显示已勾但实际没生效」。这里只管装文件。
;  3. **卸载默认保留用户数据**（%APPDATA%\WorkBuddy2API：账号凭证、配置、用量），
;     单独弹一次询问。卸载顺手删掉别人的登录态是数据丢失，不能默认做。
;
; 变量由打包脚本用 /D 传入：VERSION、SRC_EXE、OUT_FILE

Unicode true

!ifndef VERSION
  !define VERSION "0.0.0"
!endif
!ifndef SRC_EXE
  !error "必须通过 /DSRC_EXE=<exe 路径> 传入要打包的二进制"
!endif
!ifndef OUT_FILE
  !define OUT_FILE "wb2api-desktop-setup.exe"
!endif
; 图标由打包脚本用 /D 传入绝对路径。.nsi 必须 UTF-8 带 BOM，且这里只放路径不含中文。
!ifndef ICON_FILE
  !define ICON_FILE ""
!endif

!define APP_NAME      "WorkBuddy2API"
!define APP_PUBLISHER "WorkBuddy2API"
!define APP_EXE       "wb2api-desktop.exe"
!define UNINST_KEY    "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APP_NAME}"
!define RUN_KEY       "Software\Microsoft\Windows\CurrentVersion\Run"
; 自启注册表值名必须与桌面壳用的完全一致：Wails 的 autostartSlug(Options.Name)
; = 转小写并去掉非 [a-z0-9._-] 的字符 → "WorkBuddy2API" ⇒ "workbuddy2api"。
; 不一致的后果：托盘里勾「开机自启」会**再写一项**，卸载只删掉其中一项，留下孤儿启动项。
!define AUTOSTART_ID  "workbuddy2api"

Name "${APP_NAME} ${VERSION}"
OutFile "${OUT_FILE}"
InstallDir "$LOCALAPPDATA\Programs\${APP_NAME}"
InstallDirRegKey HKCU "Software\${APP_NAME}" "InstallDir"
RequestExecutionLevel user
SetCompressor /SOLID lzma

VIProductVersion "${VERSION}.0"
VIAddVersionKey "ProductName"     "${APP_NAME}"
VIAddVersionKey "FileDescription" "${APP_NAME} 安装程序"
VIAddVersionKey "FileVersion"     "${VERSION}"
VIAddVersionKey "ProductVersion"  "${VERSION}"
VIAddVersionKey "CompanyName"     "${APP_PUBLISHER}"
VIAddVersionKey "LegalCopyright"  "${APP_PUBLISHER}"

!include "MUI2.nsh"
; 安装包与卸载器图标：不设这两项，产物就是 NSIS 默认图标（与 exe 图标不一致）。
; 注意 MUI_ICON / MUI_UNICON 必须在 !include "MUI2.nsh" 之后定义。
!if "${ICON_FILE}" != ""
  !define MUI_ICON   "${ICON_FILE}"
  !define MUI_UNICON "${ICON_FILE}"
!endif
!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_RUN "$INSTDIR\${APP_EXE}"
!define MUI_FINISHPAGE_RUN_TEXT "立即启动 ${APP_NAME}"
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "SimpChinese"
!insertmacro MUI_LANGUAGE "English"

Section "Install"
  ; 覆盖安装前先关掉在跑的实例，否则 exe 被占用、复制失败。
  ; taskkill 找不到进程会返回非零码，属正常情况，不检查结果。
  nsExec::ExecToLog 'taskkill /F /IM ${APP_EXE}'
  Pop $0

  SetOutPath "$INSTDIR"
  File "${SRC_EXE}"

  ; 卸载器必须能自删：拷到临时目录再执行。
  WriteUninstaller "$INSTDIR\uninstall.exe"

  CreateShortCut "$SMPROGRAMS\${APP_NAME}.lnk" "$INSTDIR\${APP_EXE}"
  CreateShortCut "$DESKTOP\${APP_NAME}.lnk" "$INSTDIR\${APP_EXE}"

  WriteRegStr HKCU "Software\${APP_NAME}" "InstallDir" "$INSTDIR"
  WriteRegStr HKCU "Software\${APP_NAME}" "Version" "${VERSION}"

  ; 控制面板「应用和功能」条目 + 卸载信息
  WriteRegStr   HKCU "${UNINST_KEY}" "DisplayName"     "${APP_NAME}"
  WriteRegStr   HKCU "${UNINST_KEY}" "DisplayVersion"  "${VERSION}"
  WriteRegStr   HKCU "${UNINST_KEY}" "Publisher"       "${APP_PUBLISHER}"
  WriteRegStr   HKCU "${UNINST_KEY}" "DisplayIcon"     "$INSTDIR\${APP_EXE}"
  WriteRegStr   HKCU "${UNINST_KEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegStr   HKCU "${UNINST_KEY}" "QuietUninstallString" '"$INSTDIR\uninstall.exe" /S'
  WriteRegDWORD HKCU "${UNINST_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINST_KEY}" "NoRepair" 1
  WriteRegDWORD HKCU "${UNINST_KEY}" "EstimatedSize" 19000  ; KiB 量级，仅用于显示

  ; 刷新资源管理器图标缓存，避免关机/卸载后仍显示旧图标
  System::Call 'shell32::SHChangeNotify(i 0x08000000, i 0, i 0, i 0)'
SectionEnd

Section "Uninstall"
  ; 先关进程：卸载时还开着的话 exe/uninstall.exe 都可能删不掉。
  nsExec::ExecToLog 'taskkill /F /IM ${APP_EXE}'
  Pop $0

  Delete "$INSTDIR\${APP_EXE}"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"

  Delete "$SMPROGRAMS\${APP_NAME}.lnk"
  Delete "$DESKTOP\${APP_NAME}.lnk"

  ; 自启项：值名与桌面壳一致（见文件头 AUTOSTART_ID 的说明）。
  ; 不删会留下指向已删除 exe 的启动项，每次登录都报一次错。
  DeleteRegValue HKCU "${RUN_KEY}" "${AUTOSTART_ID}"

  DeleteRegKey HKCU "${UNINST_KEY}"
  DeleteRegKey HKCU "Software\${APP_NAME}"

  ; 用户数据默认**保留**：账号凭证/配置/用量都在 %APPDATA%\WorkBuddy2API，
  ; 卸载程序顺手删掉别人的登录态属于数据丢失，必须由用户明确选择。
  IfFileExists "$APPDATA\${APP_NAME}\*.*" 0 done
    MessageBox MB_YESNO|MB_ICONQUESTION \
      "是否同时删除用户数据？$\r$\n$\r$\n$APPDATA\${APP_NAME}$\r$\n$\r$\n其中包含已登录账号的凭证、网关配置与用量记录。选择「否」将保留它们，重新安装后可直接继续使用。" \
      /SD IDNO IDNO done
    RMDir /r "$APPDATA\${APP_NAME}"
  done:

  System::Call 'shell32::SHChangeNotify(i 0x08000000, i 0, i 0, i 0)'
SectionEnd

Function .onInit
  ; 已装过就沿用原目录，避免同一用户装出两份、注册表 InstallDir 互相覆盖。
  ReadRegStr $0 HKCU "Software\${APP_NAME}" "InstallDir"
  ${If} $0 != ""
    StrCpy $INSTDIR $0
  ${EndIf}
FunctionEnd

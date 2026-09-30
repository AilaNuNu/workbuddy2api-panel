# WorkBuddy2API 桌面版

把网关装进一个原生窗口（Windows 系统 WebView / WebView2），带托盘常驻。
窗口里显示的就是原 Web 管理面板的页面，**前端代码零分叉**。

## 构建

```bash
cd desktop
go run ./scripts/build.go          # 产出 wb2api-desktop.exe（GUI 子系统，无控制台黑窗）
go run ./scripts/build.go -debug   # 保留控制台窗口，调试图标/启动问题时用
```

要求：Go ≥ 1.25（Wails v3 的要求，见下方「为什么是独立模块」）、WebView2 Runtime
（Windows 11 自带；Win10 可能需要装 Evergreen Runtime）、Node 仅 Wails CLI 需要，
本仓库的构建脚本直接调 `go build`，**不需要 Node**。

Windows 上还需要 `go-winres`（生成 exe 图标与版本信息的 `.syso`）：

```bash
go install github.com/tc-hib/go-winres@latest
```

缺了它会**直接终止构建**（不是静默跳过一个没图标的 exe）；临时跳过用
`go run ./scripts/build.go -skip-res`，代价是 exe 保持上一次的图标与版本资源。

## 打安装包（Windows）

```bash
cd desktop
go run ./scripts/package                 # 构建 exe → 安装包 + 便携版 zip → dist/
go run ./scripts/package -version 1.11.7 # 显式指定版本号（覆盖自动解析）
go run ./scripts/package -skip-build     # 复用已构建的 exe，不重新打包 exe
go run ./scripts/package -no-zip         # 只出安装包，不出便携版 zip
```

产出两个文件（都在 `dist/`，已 gitignore）：

| 文件 | 说明 |
|---|---|
| `wb2api-desktop-<版本>-setup.exe` | NSIS 安装包，约 5.3 MiB |
| `wb2api-desktop-<版本>-portable.zip` | 免安装版，约 7.4 MiB |

## 发布

桌面版有独立的 tag 版本线：**`desktop-v<版本>`**，与上游服务端/CLI 的 `v<版本>`
分开。

为什么必须分开：`.github/workflows/go-binaries.yml` 监听 `tags: ["v*"]`，并带一条
「tag 版本 == 源码 AppVersion」的断言。桌面版走自己的版本号规则，若也用 `v*`
前缀，会互相触发、并且把桌面版 tag 丢进那条与它无关的断言里。`desktop-v*` 不匹配
`v*`，两条发布线彻底隔离。

```bash
# 打 tag 并推送即触发发布（.github/workflows/desktop-release.yml）
git tag -a desktop-v1.11.10 -m "桌面版 1.11.10"
git push <remote> desktop-v1.11.10
```

工作流在 `windows-latest` 上：装 NSIS → 装 go-winres → `go run ./scripts/package`
→ 自检（产物存在/大小/zip 无敏感条目/exe 内嵌修订号 == 本次提交）→ 建 Release
（两个产物 + `checksums.txt`）。也可以 `workflow_dispatch` 手动跑。

**tag 名会被规范化之后再当版本号用**：解析器剥掉 `desktop-` 与 `v` 前缀、并校验
余下部分是纯数字段（`desktop-v1.11.10` → `1.11.10`）。这样 `git describe` 无论
命中哪条版本线的 tag，出来的都是可用的版本串；抽不出合法版本号时报错降级，
不会把 `desktop-v1.11.10` 这种串塞进 NSIS 的 `VIProductVersion`（那会被直接拒绝）。

**发布前请从干净的已提交状态构建。** Go 会把当前 commit 与工作树是否脏嵌进二进制
（`go version -m` 可见 `...+dirty`），脏树构建出来的产物带 `+dirty` 戳、对不上任何
提交号。代码本身是确定性的：同一状态下连续构建，产物逐字节一致。

版本号从**源码单一来源**解析：`internal/appcore/runtime.go` 的 `AppVersion` 常量
（剥掉 `-panel` 之类后缀）→ 退回 git tag → 再退回兜底常量。四处使用者
（面板自报版本、安装包文件名、NSIS 的 `VIProductVersion`、exe 的 PE 资源）都取这一个值。

> 不要用 git tag 当首选：tag 是在版本号 bump 提交**之后**才打的，bump 落地但尚未打
> tag 时 tag 会落后一档（实测源码已 `1.11.10-panel`，最新 tag 仍 `v1.11.9` → 只按 tag
> 打包会产出比面板自报版本还旧的安装包）。解析逻辑在 `scripts/internal/version`，两个
> 脚本共用一份实现。

需要 NSIS（`winget install NSIS.NSIS`）。脚本按 `PATH` → `C:\Program Files (x86)\NSIS`
→ `C:\nsis` 的顺序自动查找 `makensis`，找不到会给出装法。

安装包行为：

- **按用户安装**到 `%LOCALAPPDATA%\Programs\WorkBuddy2API`，不弹 UAC。
- 覆盖安装前自动关掉在跑的实例（否则 exe 被占用会复制失败）。
- 卸载器删除：程序文件、两个快捷方式、卸载信息、**自启注册表项**。
- 卸载**默认保留**用户数据（`%APPDATA%\WorkBuddy2API`），只在交互卸载时询问一次；
  静默卸载（`/S`）一律不删。
- **开机自启不作为安装选项**：唯一开关是托盘菜单里的「开机自启」。安装器再放一个
  勾选会造出第二处写同一注册表值的地方，两边逻辑一旦分叉就是「装完显示已勾但没生效」。

卸载：`%LOCALAPPDATA%\Programs\WorkBuddy2API\uninstall.exe`，或控制面板「应用和功能」。

## 覆盖安装与升级

**直接双击新的 `setup.exe` 即可**，不要先卸载。覆盖安装不会动用户数据。

保证来自这三处（改动任何一处都要同步检查）：

| # | 机制 | 位置 |
|---|---|---|
| 1 | 安装脚本只写程序目录与注册表，**从不触碰数据目录** | `nsis/wb2api-desktop.nsi` |
| 2 | 首次生成配置用 `O_EXCL` **原子拒绝覆盖** | `internal/appcore/config.go` 的 `WriteDefault` |
| 3 | 只有 `fs.ErrNotExist` 才生成新配置 | `internal/appcore/runtime.go` 的 `loadOrGenerate` |

沿用原安装目录：`.onInit` 读 `HKCU\Software\WorkBuddy2API` 的 `InstallDir`，所以不会
装出第二份。

### 升级前

1. **手动退出正在跑的实例**（托盘 → 退出），不要依赖安装器的 `taskkill /F`。
   那是强杀，会跳过优雅停机（`app.Close()` → `pool.Flush()`）；万一此刻有账号状态
   变更，最后一次落盘可能丢。`state.json` 本身是「先写 tmp 再 rename」的原子替换，
   不存在写到一半变空文件的情形。
2. **备份数据目录**：

   ```cmd
   xcopy /E /I /Y "%APPDATA%\WorkBuddy2API" "%USERPROFILE%\Desktop\wb2a-backup"
   ```

   不是怕安装包删数据，而是怕**手滑走卸载**：卸载器会弹一次「是否删除用户数据」，
   默认虽为「否」（`/SD IDNO`），但一次确认挡不住点错。3 个账号的凭证重新获取是有
   成本的。

### 升级后怎么确认真的没出问题

「面板能打开、版本号对」**不足以**证明升级正确 —— 版本号只是界面上的一个字符串。
至少核对这几项：

```cmd
:: 1) 数据目录里有什么（配置 + 每个账号一个凭证文件）
dir /b "%APPDATA%\WorkBuddy2API"
dir /b "%APPDATA%\WorkBuddy2API\auths"

:: 2) 装的是新 exe：与下载页 checksums.txt 里的 sha256 比
certutil -hashfile "%LOCALAPPDATA%\Programs\WorkBuddy2API\wb2api-desktop.exe" SHA256

:: 3) PE 版本资源（右键属性 → 详细信息，或）
powershell "(Get-Item 'C:\path\to\wb2api-desktop.exe').VersionInfo | Format-List"
```

不打印密钥值。`auths/*.json` 与 `config.json` 的内容一致才叫没问题；
`state.json` / `usage.json` / 日志**本来就会变**（运行时持续写入），不算异常。
**`api_key` 一旦被换，所有已用旧密钥配置的客户端会立刻全部 401**，这是最需要盯的一项。

> 判断 `api_key` 有没有被换：不要看文件内容（那会打印密钥），只比指纹 ——
> 例如 PowerShell 里 `(Get-FileHash '%APPDATA%\WorkBuddy2API\config.json').Hash`，
> 或者拿面板「配置」页显示的那串前缀对一下。指纹没变就是没被换。

### 数据目录不跟着安装目录走

用户数据固定在 `%APPDATA%\WorkBuddy2API`（或 `WB2A_DATA_DIR`），与装在哪无关。
所以把数据目录整体搬走、或在别的机器上恢复，都不需要重装。

实测（v1.11.6 → v1.11.10 覆盖安装）：`config.json` 与 3 个 `auths/*.json` 逐字节
不变，`api_key` 指纹不变，安装目录沿用原来的（读注册表 `InstallDir`，不会装出第二份）。

一个容易踩的坑：便携版通过 `Run-Portable.cmd` 设 `WB2A_DATA_DIR=%~dp0data` 指向自己
的文件夹，但**直接双击便携版目录里的 exe 不会经过它** —— 那会去找
`%APPDATA%\WorkBuddy2API`，也就是安装版的数据。

### 免安装版 zip 里有什么

只有两个文件：`wb2api-desktop.exe` + `Run-Portable.cmd`。**不含任何 `data/` 或
`config.json`** —— 首次运行自动生成。删除解压出来的整个文件夹即无残留。

zip 由 `scripts/package` 用「逐个文件加入」的方式组装（`makePortableZip`），
**不遍历目录**。这不是洁癖：开发机上的便携版目录里往往已有 `data/`（含真实账号凭证），
「压缩整个文件夹」会把凭证打进公开发布的产物，且不可逆。


## 使用

双击 `wb2api-desktop.exe`。首次运行会在数据目录自动生成配置（含随机 `api_key`），
并在窗口里打开面板，用「添加账号」完成 OAuth 登录。

数据目录（凭证、状态、配置、日志都在这里）：

| 平台 | 位置 |
|---|---|
| Windows | `%APPDATA%\WorkBuddy2API` |
| macOS | `~/Library/Application Support/WorkBuddy2API` |
| Linux | `$XDG_DATA_HOME/workbuddy2api` 或 `~/.local/share/workbuddy2api` |

可用环境变量 `WB2A_DATA_DIR` 覆盖（便携部署 / 多实例测试）。命令行 `-config` 可
直接指定配置文件。

```
%APPDATA%\WorkBuddy2API\
  config.json          配置（桌面模式自动生成，键 auth_dir/state_file 故意缺失）
  workbuddy2api.log    运行日志（超过 8 MiB 截断重写）
  auths/               账号凭证
  data/                state.json / usage.json / model.json 等状态
```

## 行为约定

- **监听地址**：`127.0.0.1` + 动态端口。配置里的 `listen` 只作为「首选端口」，
  被占用时自动换随机端口。**不对外网暴露**——桌面场景没有这个必要。
- **关闭窗口**：弹窗问「最小化到托盘」还是「退出程序」。最小化到托盘时网关继续
  运行，已连接的 API 客户端不断线。
- **托盘菜单**：打开面板 / 复制面板地址 / 运行状态 / 重启网关 / **开机自启** / 退出。
  点击托盘图标切换窗口显示与隐藏。开机自启写 `HKCU\...\Run` 下的 `workbuddy2api`
  项（值名 = Wails 的 slug，必须与安装包卸载器删的名字一致，否则会留下孤儿启动项）。
- **配置保存后自动重启**：面板「配置」页保存了装配期字段（`listen`、`auth_dir`、
  `upstream.*`、`session_sticky.*`、`logging.*`）时，桌面壳会**自动重建内核**并把窗口
  导航到新地址，不需要用户去托盘点「重启网关」。只改热生效字段（`api_key`、`pool.*`、
  `schedule.*`、`features.*`）则不会重启。
- **请求归档开关**：配置页「上游与高级」里的「请求日志归档」控制上游新增的 JSONL 归档
  （只存脱敏元数据，不含对话内容）。它在 `internal/reqlog` 里是启动期参数（无运行时重
  配置 API），所以改动走「需重启 → 自动重建内核」。关掉归档不影响「请求指标」（内存
  统计，始终启用）。
- **单实例**：第二次双击不会起新进程，而是把已有窗口叫出来。两个实例会争同一个
  `state.json` 并抢端口，必须挡住。
- **日志**：GUI 程序没有控制台，所有日志落 `workbuddy2api.log`。用户报故障时
  让 TA 发这个文件即可。

## 为什么是独立 Go 模块

`desktop/` 是一个**嵌套的独立模块**，不是根模块的一部分。原因只有一个：

> Wails v3 要求 `go >= 1.25`，而根模块（服务端 / CLI）保持 `go 1.22.5`，
> Dockerfile 用 `golang:1.23-alpine` 构建。

把 Wails 加进根模块，`go.mod` 会被工具链强行抬到 1.25，现有 Docker 构建与所有
Go 1.22/1.23 的构建机立刻失效。拆开之后：

- 根模块 `go build ./...` **看不到** `desktop/`（嵌套模块被自动排除）→ 服务端链路零影响
- 桌面版通过 `replace github.com/linguo2625469/workbuddy2api-panel => ../` 直接引用
  根模块源码，改完根模块立即生效，无需发版本

改根模块的公共代码（`internal/appcore`、`internal/panel` 等）时，**两个模块都要跑测试**：

```bash
go build ./... && go test ./...              # 根模块
cd desktop && go build ./... && go test ./... # 桌面模块
```

## 目录结构

```
desktop/
  go.mod          独立模块（go 1.25，replace 指向 ../）
  main.go         桌面壳：窗口 / 托盘 / 单实例 / 关闭对话框 / 开机自启 / 重启
  logfile.go      落盘日志（环形截断）
  autostart_test.go 开机自启的注册表往返测试（真实读写 HKCU\...\Run，用测试专用项名）
  wb2api-desktop.syso  **自动生成**（go-winres 产出，已 gitignore）：
                  PE 资源段里的 exe 图标 + 版本信息 + GUI manifest
  assets/
    tray.ico      托盘与应用图标（多尺寸 16→256）。同时也是 exe 与安装包图标的源
    app.png       透明底应用图标（安装包/文档用）
  nsis/
    wb2api-desktop.nsi  安装包脚本（UTF-8 **带 BOM**，否则 makensis 按 ACP 读会乱码）
  scripts/
    build.go      构建脚本（windowsgui ldflags + 生成 .syso + 体积报告）
    package/      安装包脚本（单独目录：与 build.go 同为 package main，同目录会 main 重定义）
    internal/version/  版本号解析（两个 package main 无法互相 import，抽成共享包）
  dist/           安装包输出（已 gitignore）
```

图标有三处使用者，缺一不可：**exe 的 PE 资源段**（资源管理器/任务栏/Alt-Tab 读它）、
**安装包**（NSIS `MUI_ICON`）、**托盘**（`go:embed`）。`go:embed` 只管托盘——只换
`tray.ico` 而不生成 `.syso` 的话，exe 会看起来「改了但没变」。

## 已知限制

- **Wails v3 目前是 beta**（当前锁定 `v3.0.0-beta.25`）。API 可能变动，升级时
  重点回归：托盘菜单项、窗口关闭钩子、对话框按钮回调（这三处是壳的全部依赖面）。
- **仅 Windows 实测**。macOS / Linux 的代码路径是按 Wails 文档写的，未构建验证；
  macOS 还需补一个单色 template 托盘图标（当前彩色图标在菜单栏观感一般）。
- **只提供 NSIS 安装包（`.exe`），不做 MSI**。NSIS 覆盖个人用户双击安装的全部需求
  （按用户装、不弹 UAC、写「应用和功能」条目、能静默卸载）；MSI 的优势在企业批量部署
  （组策略 / SCCM / `ADDLOCAL`）与「修复安装」，当前用不上。若日后要上 MSI，同一份 exe
  用 WiX 再产一个 `.msi` 即可，两种安装包互不冲突。
- **安装包未做代码签名**。首次运行会有 SmartScreen 警告（「Windows 已保护你的电脑」→
  「更多信息」→「仍要运行」）。要消除得买代码签名证书，用 `signtool` 签 exe 与安装包。
- 用户数据在 `%APPDATA%\WorkBuddy2API`，**不随安装目录走**。想整个搬走用
  `WB2A_DATA_DIR` 指定一个自选目录（便携部署）。

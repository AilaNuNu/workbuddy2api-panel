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

## 打安装包（Windows）

```bash
cd desktop
go run ./scripts/package                 # 构建 exe → 打安装包 → dist/wb2api-desktop-<版本>-setup.exe
go run ./scripts/package -version 1.11.7 # 指定版本号（默认 1.11.6）
go run ./scripts/package -skip-build     # 复用已构建的 exe，不重新编译
```

需要 NSIS（`winget install NSIS.NSIS`）。脚本按 `PATH` → `C:\Program Files (x86)\NSIS`
→ `D:\Tools\nsis` 的顺序自动查找 `makensis`，找不到会给出装法。

安装包行为：

- **按用户安装**到 `%LOCALAPPDATA%\Programs\WorkBuddy2API`，不弹 UAC。
- 覆盖安装前自动关掉在跑的实例（否则 exe 被占用会复制失败）。
- 卸载器删除：程序文件、两个快捷方式、卸载信息、**自启注册表项**。
- 卸载**默认保留**用户数据（`%APPDATA%\WorkBuddy2API`），只在交互卸载时询问一次；
  静默卸载（`/S`）一律不删。
- **开机自启不作为安装选项**：唯一开关是托盘菜单里的「开机自启」。安装器再放一个
  勾选会造出第二处写同一注册表值的地方，两边逻辑一旦分叉就是「装完显示已勾但没生效」。

卸载：`%LOCALAPPDATA%\Programs\WorkBuddy2API\uninstall.exe`，或控制面板「应用和功能」。

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
  `upstream.*`、`session_sticky.*`）时，桌面壳会**自动重建内核**并把窗口导航到新地址，
  不需要用户去托盘点「重启网关」。只改热生效字段（`api_key`、`pool.*`、`schedule.*`）
  则不会重启。
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
  assets/
    tray.ico      托盘与应用图标（多尺寸 16→256）
    app.png       透明底应用图标（安装包/文档用）
  nsis/
    wb2api-desktop.nsi  安装包脚本（UTF-8 **带 BOM**，否则 makensis 按 ACP 读会乱码）
  scripts/
    build.go      构建脚本（windowsgui ldflags + 体积报告）
    package/      安装包脚本（单独目录：与 build.go 同为 package main，同目录会 main 重定义）
  dist/           安装包输出（已 gitignore）
```

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

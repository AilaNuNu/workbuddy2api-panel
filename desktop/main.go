// main.go 桌面入口：把已有的网关内核装进一个原生窗口（系统 WebView），
// 带托盘常驻。窗口里加载的就是原 Web 面板的页面，前端代码零分叉。
//
// 架构要点：
//
//  1. 网关监听 **127.0.0.1 的随机端口**，不是配置里的 :7863。桌面机器上
//     7863 被别的程序占用是常态（用户还会自己起一份服务端），固定端口会让
//     应用直接起不来。端口只对回环地址开放，不对外网暴露。
//
//  2. 窗口和托盘加载 **同一个 origin** 的页面（http://127.0.0.1:<port>/panel/）。
//     不用 Wails 的资源服务器（wails://），因为面板的前端是绝对路径和
//     同源相对路径混用（index.html 引 ./app.js，app.js 请求 /panel/api/*），
//     只有浏览器同源模型下才成立。这样前端一行不用改，也不会有 origin 混淆。
//
//  3. 单实例：第二次启动不新开进程，而是把已有窗口叫出来（SingleInstance）。
//     两个实例会争同一个 state.json 并抢端口，必须挡住。
//
//  4. 关闭窗口时**问用户**：最小化到托盘（网关继续跑，已连接的客户端不断）
//     还是彻底退出。托盘菜单提供同一个入口，以及「重启网关」。
package main

import (
	_ "embed"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/appcore"
	"github.com/linguo2625469/workbuddy2api-panel/internal/logfmt"
	"github.com/linguo2625469/workbuddy2api-panel/internal/runtimepaths"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed assets/tray.ico
var trayIcon []byte

// appID 单实例锁与桌面集成的稳定标识。
const appID = "com.linguo2625469.workbuddy2api"

// desktopApp 桌面壳状态。
//
// 并发约定：除 appStarted 外，其余字段只在**主线程**访问（Wails 事件回调、以及经
// application.InvokeAsync/InvokeSync 切过来的任务）。跨 goroutine 的入口只有
// onConfigNeedsRestart（它先切主线程）与 fatal。
type desktopApp struct {
	app     *application.App
	runtime *appcore.Runtime
	window  *application.WebviewWindow
	tray    *application.SystemTray
	logFile *rotatingLogWriter

	// appStarted 标记 Wails 主循环已在运行。此前 windowImpl 尚不存在，
	// 对窗口/托盘的操作要直接做（构造期参数）而不是派发到主线程。
	// 原子类型：写入发生在主线程，读取可能来自 HTTP handler 的 goroutine。
	appStarted atomic.Bool

	// statusItem 托盘菜单里的状态行（重启后要更新显示的地址）。
	statusItem *application.MenuItem

	// autostartItem 托盘菜单里的「开机自启」勾选项（切换后回写勾选状态）。
	autostartItem *application.MenuItem

	// forceQuit 置位后关闭窗口不再询问，直接退出（托盘「退出」用）。
	forceQuit bool
	// windowHidden 记录窗口当前是否收在托盘里（托盘点击据此切换显示/隐藏）。
	windowHidden bool
	// firstRunKey 首启新生成的 api_key；非 nil 表示「这次是全新安装」，
	// 窗口就绪后要弹一次对话框把它交给用户（见 showFirstRunKey）。
	firstRunKey atomic.Pointer[string]
}

func main() {
	// 用 -config 指定配置文件；桌面模式默认在用户数据目录下（见 config_desktop.go）。
	cfgFlag := flag.String("config", "", "配置文件路径（缺省：桌面数据目录下的 config.json）")
	flag.Parse()

	// 数据目录先算出来：配置、日志、单实例锁都要用它。
	dataDir, err := absPath(runtimepaths.UserDataDir())
	if err != nil {
		fatal(nil, "无法确定数据目录: %v", err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		fatal(nil, "无法创建数据目录 %s: %v", dataDir, err)
	}

	cfgPath := *cfgFlag
	if cfgPath == "" {
		cfgPath = filepath.Join(dataDir, "config.json")
	}
	cfgPath, err = absPath(cfgPath)
	if err != nil {
		fatal(nil, "配置文件路径无效: %v", err)
	}

	// 日志落盘 + 同时写 stdout：GUI 子系统没有控制台，落盘是唯一的用户侧诊断通道；
	// 但开发期在终端直接跑时，stdout 仍然要有输出（否则调试体验退化）。
	logFile, err := newRotatingLogWriter(filepath.Join(dataDir, "workbuddy2api.log"))
	if err != nil {
		fatal(nil, "无法写入日志文件（数据目录 %s 是否可写？）: %v", dataDir, err)
	}
	defer logFile.Close()
	// 落盘日志。用 logfmt.Tee 而不是 io.MultiWriter：桌面版是 GUI 子系统，
	// 无控制台 → os.Stdout 写入会失败 → MultiWriter 会在这里截断，导致其后所有
	// 日志（含本行之后的启动信息）丢失。详见 internal/logfmt/writer.go。
	log.SetOutput(logfmt.Tee(logFile, os.Stdout))

	// 托盘图标（资源缺失不影响主流程，只是托盘没图标）。
	icon, iconErr := trayIconBytes()
	if iconErr != nil {
		log.Printf("托盘图标不可用: %v", iconErr)
	}

	app := application.New(application.Options{
		Name:        "WorkBuddy2API",
		Description: "CodeBuddy 账号 → OpenAI 兼容 API 网关",
		Icon:        icon,
		// 单实例：第二次启动只把已有窗口叫出来（两个实例会争 state.json 与端口）。
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: appID,
		},
		// WebView2 的用户数据目录默认在 %APPDATA%\<exe 名>\EBWebView，**不受
		// WB2A_DATA_DIR 控制**：便携版把数据目录指到本文件夹后，缓存仍会留在
		// %APPDATA%（实测 8.9 MB），于是「删文件夹即彻底干净」并不成立。
		// 这里把它钉进数据目录，让便携版名副其实；卸载/删除目录即无残留。
		Windows: application.WindowsOptions{
			WebviewUserDataPath: filepath.Join(dataDir, "webview"),
		},
	})

	da := &desktopApp{app: app, logFile: logFile}

	// appStarted 以**真正的启动事件**为准，而不是靠调用顺序猜：在它之前 windowImpl
	// 还是 nil，SetURL/SetLabel 只是更新构造参数（必须直接调用）；之后主循环已在跑，
	// 跨 goroutine 的操作必须经 InvokeAsync/InvokeSync 切回主线程。
	app.Event.RegisterApplicationEventHook(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		da.appStarted.Store(true)
		log.Printf("Wails 主循环已就绪（此后跨线程 UI 操作会切回主线程）")
	})

	// 网关先起来再开窗口：装配失败（配置坏了 / 目录不可写）要把错误直接呈现给
	// 用户，而不是打开一个永远转圈的白窗口。
	//
	// 首启提示（OnFirstRun）**攒到窗口出来之后再弹**：这一步 Wails 主循环还没跑，
	// 现在弹对话框会卡在 dispatchOnMainThread 上。见 showFirstRunKey。
	rt, err := appcore.New(appcore.EntryConfig{
		ConfigPath: cfgPath,
		Generate:   appcore.WriteDesktopDefault,
		LogWriter:  logFile,
		Headless:   true,
		// 面板保存了装配期字段（listen/auth_dir/upstream.* 等）→ 自动重建内核，
		// 不要求用户自己去找托盘的「重启网关」。见 onConfigNeedsRestart。
		OnConfigNeedsRestart: da.onConfigNeedsRestart,
		// 首启生成的 api_key 只落在 config.json 与日志里，而 GUI 程序没有控制台——
		// 不主动交付，用户就永远进不去面板。回调只置一个标志，真正的弹窗在窗口就绪后。
		OnFirstRun: func(key string) { da.firstRunKey.Store(&key) },
	})
	if err != nil {
		fatal(app, "启动失败：%v", err)
	}
	da.runtime = rt

	// 桌面语义：配置里的 listen 只作为「首选端口」，实际绑 127.0.0.1 回环 + 动态端口。
	// 固定 :7863 在用户机器上极易被占用，且桌面场景没有对外暴露端口的理由。
	addr, err := pickListenAddr(rt.Config.Listen, rt.Config.DesktopAllowLAN)
	if err != nil {
		rt.Close()
		fatal(app, "无法分配本地端口：%v", err)
	}
	// 用 SetListenAddr 而不是改写 rt.Config.Listen：Config 是「文件里写的配置」，
	// 面板保存时会拿它与磁盘内容比较来决定是否需要重启；就地改写会让基线永远与
	// 磁盘不一致 → 每次保存都误报「需要重启」。
	rt.SetListenAddr(addr)
	if err := rt.Start(); err != nil {
		rt.Close()
		fatal(app, "启动失败：%v", err)
	}
	log.Printf("桌面模式已启动：%s", rt.PanelURL())

	da.buildWindow(app)
	da.buildTray(app, icon)

	// 窗口与托盘都指向同一 origin（见文件头第 2 点）。
	da.window.SetURL(rt.PanelURL())

	// 首启密钥提示：挂在窗口显示事件上，而不是 app.Run() 之后直接调。
	// 因为 app.Run() 会阻塞到退出，而对话框/剪贴板都要在主循环里执行；
	// 窗口显示时（RegisterHook(WindowShow)）主循环已在跑，是能弹窗的最早时刻。
	// 非首次启动时 showFirstRunKey 会立刻返回（firstRunKey 为空）。
	da.window.RegisterHook(events.Common.WindowShow, func(*application.WindowEvent) {
		da.showFirstRunKey()
	})

	if err := app.Run(); err != nil {
		log.Printf("app.Run: %v", err)
	}
	// 应用退出：收掉网关（Flush 落盘 → 关连接）。
	rt.Close()
	log.Printf("bye")
}

// pickListenAddr 返回一个可用的监听地址。
//
// 先用配置里的端口（尊重用户习惯、也让 URL 可预期），占用则退回随机端口。
//
// 绑定范围由 allowLAN 决定：
//   - false（默认）：只绑 127.0.0.1。桌面应用不该默认把网关暴露到局域网。
//   - true：绑 0.0.0.0，局域网内其它设备可访问（用户在配置页显式打开）。
//
// 端口回退仍是"静默换"：日志里记一行，托盘状态行显示实际地址。
func pickListenAddr(preferred string, allowLAN bool) (string, error) {
	host := "127.0.0.1"
	if allowLAN {
		host = "0.0.0.0"
	}

	port := ""
	// 从 ":7863" / "0.0.0.0:7863" / "127.0.0.1:7863" 里取出端口部分。
	if _, p, err := net.SplitHostPort(preferred); err == nil {
		port = p
	}
	if port != "" {
		if ln, err := net.Listen("tcp", host+":"+port); err == nil {
			_ = ln.Close()
			return host + ":" + port, nil
		}
		log.Printf("端口 %s 已被占用，改用随机端口", port)
	}
	ln, err := net.Listen("tcp", host+":0")
	if err != nil {
		return "", err
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr, nil
}

// buildWindow 创建主窗口并挂关闭确认。
func (da *desktopApp) buildWindow(app *application.App) {
	win := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "main",
		Title:            "WorkBuddy2API",
		Width:            1280,
		Height:           860,
		MinWidth:         960,
		MinHeight:        600,
		InitialPosition:  application.WindowCentered,
		BackgroundColour: application.NewRGB(12, 14, 20), // 与面板深色底一致，避免开窗闪白
		Windows: application.WindowsWindow{
			Theme: application.Dark,
		},
	})
	da.window = win

	// 关闭窗口 → 问用户「最小化到托盘」还是「退出」。
	// 用 Cancel() 拦下原生关闭，再由我们的对话框决定后续动作。
	win.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		if da.forceQuit {
			return // 托盘「退出」已确认，放行关闭
		}
		e.Cancel()
		da.askOnClose()
	})
}

// buildTray 创建托盘图标与菜单。
func (da *desktopApp) buildTray(app *application.App, icon []byte) {
	tray := app.SystemTray.New()
	tray.SetTooltip("WorkBuddy2API 网关")
	if len(icon) > 0 {
		tray.SetIcon(icon)
	}
	tray.OnClick(func() { da.toggleWindow() })
	tray.OnDoubleClick(func() { da.toggleWindow() })

	menu := application.NewMenu()
	menu.Add("打开面板").OnClick(func(*application.Context) { da.showWindow() })
	menu.Add("复制面板地址").OnClick(func(*application.Context) { da.copyPanelURL() })
	menu.AddSeparator()
	// 状态行：不可点击，仅作信息展示。句柄留着，重启后需要更新地址。
	da.statusItem = menu.Add(fmt.Sprintf("运行中 · %s", da.runtime.Addr()))
	da.statusItem.SetEnabled(false)
	menu.AddSeparator()
	menu.Add("重启网关").OnClick(func(*application.Context) { da.restartGateway() })
	menu.AddSeparator()

	// 开机自启：勾选状态**每次从系统里读**（HKCU\...\Run），不在进程内缓存。
	// 用户可能用任务管理器/其他工具关掉自启，缓存的勾会与系统不一致；而自启本身是
	// 一个注册表项、不是本进程状态，读一次的成本可以忽略。
	au := menu.AddCheckbox("开机自启", da.autostartEnabled())
	au.OnClick(func(*application.Context) { da.toggleAutostart(au) })
	da.autostartItem = au

	menu.AddSeparator()
	menu.Add("退出").OnClick(func(*application.Context) { da.quit(app) })
	tray.SetMenu(menu)
	da.tray = tray
}

// autostartEnabled 读取当前是否已登记开机自启（失败一律当作「未启用」，不误报勾选）。
func (da *desktopApp) autostartEnabled() bool {
	on, err := da.app.Autostart.IsEnabled()
	if err != nil {
		log.Printf("读取开机自启状态失败：%v", err)
		return false
	}
	return on
}

// toggleAutostart 切换开机自启，并把菜单勾选状态对齐到**实际结果**。
//
// 用 Read→Apply→Verify 而不是「先翻勾再注册」：注册是写注册表，可能因权限/策略失败。
// 先改 UI 再注册的话，失败后勾会停在错误状态，用户看到的是「勾上了但没生效」。
func (da *desktopApp) toggleAutostart(item *application.MenuItem) {
	want := !da.autostartEnabled()
	var err error
	if want {
		err = da.app.Autostart.Enable()
	} else {
		err = da.app.Autostart.Disable()
	}
	if err != nil {
		log.Printf("切换开机自启失败（want=%v）：%v", want, err)
		item.SetChecked(da.autostartEnabled()) // 回读真实状态
		da.showInfo("开机自启", fmt.Sprintf("设置失败：%v", err))
		return
	}
	if got := da.autostartEnabled(); got != want {
		log.Printf("开机自启状态不符：want=%v got=%v", want, got)
	}
	item.SetChecked(da.autostartEnabled())
	log.Printf("开机自启已%s", map[bool]string{true: "启用", false: "禁用"}[want])
}

// toggleWindow 托盘点击：隐藏 → 显示并聚焦；显示 → 收进托盘。
func (da *desktopApp) toggleWindow() {
	if da.windowHidden || !da.window.IsVisible() {
		da.showWindow()
		return
	}
	da.window.Hide()
	da.windowHidden = true
}

func (da *desktopApp) showWindow() {
	da.window.Show()
	da.window.Focus()
	da.windowHidden = false
}

// copyPanelURL 把面板地址放进剪贴板（用户想用浏览器打开时用得上）。
func (da *desktopApp) copyPanelURL() {
	app := application.Get()
	if app == nil || app.Clipboard == nil {
		return
	}
	app.Clipboard.SetText(da.runtime.PanelURL())
	log.Printf("已复制面板地址：%s", da.runtime.PanelURL())
}

// askOnClose 关闭窗口时询问用户：最小化到托盘 / 退出 / 取消。
//
// 修复记录（用户报「点『是』没反应」）：原先用 application.Dialog.Question() +
// AddButton("最小化到托盘") 等中文标签。Wails v3 beta.25 的 Windows 实现把
// Question 做成 MessageBox(MB_YESNO)，再按 `if button.Label == result`
// 分派回调，result 是系统返回值的字符串（"Yes"/"No"）——自定义标签永远匹配不上，
// 于是三个回调全部静默丢失：对话框关掉了，但什么都没执行。
// 现在改为自己读返回值（见 closechoice*.go），行为与标签无关。
//
// 注意：本函数在主线程被调用（窗口关闭钩子），而 askCloseChoice 是**阻塞**的，
// 与原先 dialog.Show() 走 InvokeSync 的语义一致。
func (da *desktopApp) askOnClose() {
	askCloseChoice(da.nativeWindowHandle(), func(c closeChoice) {
		switch c {
		case closeChoiceTray:
			// 隐藏窗口：网关继续跑，已连接的客户端不受影响。
			da.window.Hide()
			da.windowHidden = true
		case closeChoiceQuit:
			// 先置位再关：forceQuit 让随后的窗口关闭不再追问。
			da.forceQuit = true
			da.runtime.Close()
			application.Get().Quit()
		default: // closeChoiceCancel：什么都不做，窗口保持打开
			da.showWindow()
		}
	})
}

// showFirstRunKey 首次启动时把新生成的 api_key 交给用户。
//
// 为什么必须有一次主动交付：密钥是首启用 crypto/rand 生成的，只写进 config.json 和
// 启动日志。桌面版是 GUI 子系统（无控制台），用户看不到日志；面板又只提示「请输入
// config.json 中的密钥」——用户既不知道路径也没打开过 JSON。结果是全新安装后**进不去
// 面板**。这里在窗口就绪后弹一次，并把密钥放进剪贴板让用户直接粘贴。
//
// 为什么用剪贴板而不是「自动填进页面」：面板由网关自己的 HTTP 服务器托管，不是 Wails
// 的资产服务器，Wails 的 JS runtime 不会注入这种页面（实测 WindowRuntimeReady 不触发），
// 因此 ExecJS 在此不可用。剪贴板是唯一能跨越该边界的交付通道。
//
// 必须在窗口就绪后调用（RegisterHook(WindowShow)）：更早的话 Wails 主循环还没跑，
// 对话框与剪贴板的跨线程调用会卡在 dispatchOnMainThread 上。
func (da *desktopApp) showFirstRunKey() {
	p := da.firstRunKey.Load()
	if p == nil {
		return // 不是首次启动
	}
	key := *p
	// 取走即清空：只提示一次。用户若没看清，配置页还有「重新生成密钥」。
	da.firstRunKey.Store(nil)

	app := application.Get()
	if app == nil {
		return
	}
	if app.Clipboard != nil {
		app.Clipboard.SetText(key)
	}

	msg := "已为你生成访问密钥（网关与面板共用同一个密钥）。\n\n" +
		"    " + key + "\n\n" +
		"密钥已复制到剪贴板，直接粘贴到面板的密钥框即可。\n\n" +
		"它保存在配置文件里：\n" + da.runtime.ConfigPath + "\n\n" +
		"提示：配置页可以随时重新生成密钥；" +
		"若开启「允许局域网访问」，请务必保留非空密钥。"

	d := app.Dialog.Info().
		SetTitle("首次运行：访问密钥").
		SetMessage(msg)
	if da.window != nil {
		d = d.AttachToWindow(da.window)
	}
	d.Show()
}

// quit 托盘「退出」：直接收尾退出（不再追问，用户已经明确点了退出）。
func (da *desktopApp) quit(app *application.App) {
	da.forceQuit = true
	da.runtime.Close()
	if app != nil {
		app.Quit()
	}
}

// onConfigNeedsRestart 面板保存了装配期字段后的回调（由面板在 HTTP 响应写完后异步调用）。
//
// 为什么必须切回主线程：这个回调来自 HTTP handler 的 goroutine，而重启过程要操作
// Wails 对象——`MenuItem.SetLabel` 内部**没有** InvokeSync 保护，直接调用会与托盘
// 点击、窗口关闭这些主线程回调并发读写同一批字段（数据竞争，且 Windows 下 Win32
// 菜单/窗口 API 本身要求同线程访问）。整个动作包进 InvokeAsync，一次切线程做完。
//
// 首次启动时 Runtime 在 app.Run() 之前构建，此时 Wails 主循环还没跑起来，
// InvokeAsync 派发的任务不会被执行——所以只有「已经开始运行」之后才走主线程。
func (da *desktopApp) onConfigNeedsRestart() {
	if da.appStarted.Load() {
		application.InvokeAsync(da.restartGateway)
		return
	}
	da.restartGateway()
}

// restartGateway 重启网关内核（换端口 / 让装配期配置生效），窗口保持打开。
//
// 顺序很关键：**先彻底收掉旧实例，再装配新实例**。反过来的话，新旧两个 pool 会
// 同时持有同一个 state.json（旧实例的异步落盘还没排空），存在互相覆盖的风险。
// 代价是中间有短暂的服务空窗（毫秒级），对本地网关可接受。
//
// 必须在主线程调用（见 onConfigNeedsRestart）。
func (da *desktopApp) restartGateway() {
	log.Printf("重启网关…")
	old := da.runtime
	old.StopHTTP() // 先断流量，避免新实例起来前还在写状态
	old.Close()    // 落盘 + 关连接，之后 state.json 才无人占用

	rt, err := appcore.New(appcore.EntryConfig{
		ConfigPath: old.ConfigPath,
		Generate:   appcore.WriteDesktopDefault,
		LogWriter:  da.logFile,
		Headless:   true,
		// 必须重新传入：Runtime 是重建出来的，回调不会自己继承。漏了这行的后果是
		// 「第一次自动重启之后，自动重启能力就永久消失」——后续保存装配期字段只会
		// 提示需重启而不再重启，而且要再改一次配置才能暴露。
		OnConfigNeedsRestart: da.onConfigNeedsRestart,
		// 重启路径上不放首启提示：配置已存在，Runtime 不会触发（传了也无害，
		// 传 nil 更明确地表达「这里不是首次启动」）。
	})
	if err != nil {
		log.Printf("重启失败：%v", err)
		da.showFatal("重启失败", err.Error())
		return
	}
	addr, err := pickListenAddr(rt.Config.Listen, rt.Config.DesktopAllowLAN)
	if err != nil {
		rt.Close()
		log.Printf("重启失败（分配端口）：%v", err)
		da.showFatal("重启失败", err.Error())
		return
	}
	rt.SetListenAddr(addr)
	if err := rt.Start(); err != nil {
		rt.Close()
		log.Printf("重启失败（监听）：%v", err)
		da.showFatal("重启失败", err.Error())
		return
	}

	da.runtime = rt
	if da.statusItem != nil {
		da.statusItem.SetLabel(fmt.Sprintf("运行中 · %s", rt.Addr()))
	}
	if da.tray != nil {
		da.tray.SetTooltip(fmt.Sprintf("WorkBuddy2API 网关 · %s", rt.Addr()))
	}
	// 窗口重新加载面板：端口可能变了，且旧页面里缓存的 api_key 要重新校验。
	da.window.SetURL(rt.PanelURL())
	log.Printf("网关已重启：%s", rt.PanelURL())
}

// showFatal 把致命错误呈现到 UI（有窗口时用对话框，否则退回日志 + 系统提示）。
func (da *desktopApp) showFatal(title, msg string) {
	app := application.Get()
	if app == nil {
		log.Printf("%s: %s", title, msg)
		return
	}
	d := app.Dialog.Error().SetTitle(title).SetMessage(msg + "\n\n日志：" + da.logFile.Path())
	if da.window != nil {
		d = d.AttachToWindow(da.window)
	}
	d.Show()
}

// fatal 启动期致命错误：还没有窗口，写日志 + 弹系统对话框 + 退出。
// 没有这一步的话，桌面应用启动失败时表现为「双击了没反应」，用户无从下手。
func (da *desktopApp) showInfo(title, msg string) {
	app := application.Get()
	if app == nil {
		log.Printf("%s: %s", title, msg)
		return
	}
	d := app.Dialog.Info().SetTitle(title).SetMessage(msg)
	if da.window != nil {
		d = d.AttachToWindow(da.window)
	}
	d.Show()
}

func fatal(app *application.App, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	log.Printf("FATAL: %s", msg)
	if app != nil {
		app.Dialog.Error().SetTitle("WorkBuddy2API 启动失败").SetMessage(msg).Show()
		// 给对话框一点时间画出来再退出进程。
		go func() {
			time.Sleep(300 * time.Millisecond)
			app.Quit()
		}()
		_ = app.Run()
		return
	}
	if runtime.GOOS == "windows" {
		// 极早期失败（数据目录/日志都建不出来）：尽力弹一个原始提示。
		_ = os.WriteFile(filepath.Join(os.TempDir(), "workbuddy2api-fatal.txt"), []byte(msg), 0o600)
	}
	os.Exit(1)
}

// absPath 绝对化并清理路径。
func absPath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

// trayIconBytes 返回托盘图标字节（内嵌资源；为空返回 error 由调用方降级）。
func trayIconBytes() ([]byte, error) {
	if len(trayIcon) == 0 {
		return nil, fmt.Errorf("内嵌托盘图标为空")
	}
	return trayIcon, nil
}

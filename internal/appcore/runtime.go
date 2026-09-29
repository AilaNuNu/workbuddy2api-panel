// runtime.go 装配与生命周期：把「配置 → 账号凭证 → 账号池 → 会话粘性 → 上游客户端 →
// 调度器 → 用量记录 → 面板 → HTTP handler」这条链封装成一个可复用的 Runtime。
//
// 为什么要抽出来：原先这套装配写在 cmd/server/main.go 里，桌面壳（另一个 main 包）
// 无法复用，只能复制一份——两份装配一旦漂移，就会出现「桌面版少接了某个调度器」
// 这类只在用户机器上暴露的问题。装配是同一个领域概念，只应有一份实现。
//
// 用法（见两个入口 main）：
//
//	app, err := appcore.New(appcore.EntryConfig{ConfigPath: *cfgPath})
//	if err != nil { ... }
//	defer app.Close()
//	if err := app.Start(); err != nil { ... }
package appcore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/livecfg"
	"github.com/linguo2625469/workbuddy2api-panel/internal/logfmt"
	"github.com/linguo2625469/workbuddy2api-panel/internal/panel"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/redisstore"
	"github.com/linguo2625469/workbuddy2api-panel/internal/scheduler"
	"github.com/linguo2625469/workbuddy2api-panel/internal/server"
	"github.com/linguo2625469/workbuddy2api-panel/internal/session"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
	"github.com/linguo2625469/workbuddy2api-panel/internal/usage"
)

// AppVersion 网关版本（fork 版：面板 + 任务体系），透出到 /panel/api/overview。
// 桌面壳与命令行入口共用同一版本号，避免回答用户时出现两个版本。
const AppVersion = "1.11.6-panel"

// EntryConfig 两个入口（cmd/server 与桌面壳）共用的启动参数。
type EntryConfig struct {
	// ConfigPath 配置文件路径。缺省 "config.json"；不存在时自动生成推荐配置。
	ConfigPath string
	// Generate func 在配置文件缺失时负责生成（返回随机 api_key）。
	// nil = 使用 WriteDefault（服务端语义）；桌面壳传 WriteDesktopDefault。
	Generate func(path string) (string, error)
	// LogWriter 日志去向（log 包与 chat 表格日志共用）。nil = 仅 stdout/stderr。
	// 桌面壳在无控制台的环境下需要一个落盘 writer，否则出故障时什么都看不到。
	LogWriter io.Writer
	// Headless 只读/无头模式：跳过需要交互的启动期提示（桌面壳用）。
	Headless bool
	// OnConfigNeedsRestart 面板保存了装配期字段后，由宿主要求重启进程/内核。
	//
	// 传入的是**已落盘并完成热应用**之后的回调时机；本函数由面板在写完 HTTP 响应
	// 后异步调用。nil = 不重启（服务端版本，由用户自己重启容器/进程）。
	//
	// 这里不直接让 Runtime 自己重启：重建 Runtime 是宿主（桌面壳）的职责——
	// 它还要更新窗口 URL、托盘状态，并保证同一时刻只有一个实例持有 state.json。
	OnConfigNeedsRestart func()

	// OnFirstRun 本次启动**新建了配置文件**时调用，参数是新生成的 api_key。
	//
	// 供宿主把密钥交给用户：首启生成的密钥只写在 config.json 与日志里，而 GUI
	// 程序没有控制台，用户看不到日志——不主动告知就永远进不去面板。
	// 仅在「文件原本不存在、本次由 Generate 创建」时触发一次；已有配置不触发。
	OnFirstRun func(apiKey string)
}

// Runtime 一个装配完成、可启停的网关实例。字段导出供面板/探针等外部装配读取。
type Runtime struct {
	Config     *Config
	ConfigPath string

	Pool      *pool.Pool
	Session   *session.Router
	Upstream  *upstream.Client
	Scheduler *scheduler.Scheduler
	Panel     *panel.Panel
	Usage     *usage.Recorder

	// RedisMode "upstash" / "noop"，透出到面板与日志。
	RedisMode string
	// DataDir 桌面模式下的数据目录（非桌面模式为空串）。
	DataDir string

	store redisstore.Store
	live  *livecfg.Holder
	// listenAddr 实际监听地址（可能与 Config.Listen 不同：桌面壳绑回环 + 动态端口）。
	//
	// 必须与 Config.Listen 分开存：Config 是「文件里写的配置」，会被面板保存逻辑拿去
	// 与磁盘内容比较来决定是否需要重启。就地改写 Config.Listen 会让基线永远与磁盘
	// 不一致，导致每次保存都误报「需要重启」——而这个误报只在真实桌面进程里出现，
	// 单元测试（不起监听）测不到。
	listenAddr string

	handler  http.Handler
	srv      *http.Server
	listener net.Listener
	cancel   context.CancelFunc
	started  bool
}

// SetListenAddr 指定实际监听地址（空 = 用 Config.Listen）。
// 在 Start 之前调用；不改动 Config（见 listenAddr 字段注释）。
func (r *Runtime) SetListenAddr(addr string) { r.listenAddr = addr }

// listenTarget 本轮 Start 要绑定的地址。
func (r *Runtime) listenTarget() string {
	if r.listenAddr != "" {
		return r.listenAddr
	}
	return r.Config.Listen
}

// New 加载配置并装配全部组件（不监听端口，便于在 UI 起来之前先报配置错误）。
//
// 装配失败时调用方应把 error 直接展示给用户：返回的信息是可操作的（路径不可写、
// 排程小时非法、提示词文件读不到等）。
func New(entry EntryConfig) (*Runtime, error) {
	if entry.ConfigPath == "" {
		entry.ConfigPath = "config.json"
	}
	gen := entry.Generate
	if gen == nil {
		gen = WriteDefault
	}

	cfg, desktopDataDir, firstRun, err := loadOrGenerate(entry.ConfigPath, gen)
	if err != nil {
		return nil, err
	}
	log.Printf("配置已加载：%s（listen=%s，api_key=%v）", entry.ConfigPath, cfg.Listen, cfg.APIKey != "")
	if desktopDataDir != "" {
		log.Printf("桌面模式：数据目录 %s（auth_dir=%s）", desktopDataDir, cfg.AuthDir)
	}
	if firstRun && entry.OnFirstRun != nil {
		entry.OnFirstRun(cfg.APIKey)
	}

	rt := &Runtime{Config: cfg, ConfigPath: entry.ConfigPath, DataDir: desktopDataDir}
	if err := rt.build(entry); err != nil {
		rt.Close()
		return nil, err
	}
	return rt, nil
}

// loadOrGenerate 读配置；文件缺失时生成一份再读。
//
// 第二个返回值是桌面数据目录，第三个是「本次新建了配置文件」（首启）。
// 首启标记用于把新生成的密钥主动告知用户：它只落在 config.json 与日志里，
// 而 GUI 程序没有控制台，不主动交付则用户永远进不去面板。
func loadOrGenerate(path string, gen func(string) (string, error)) (*Config, string, bool, error) {
	cfg, err := Load(path)
	if err == nil {
		return cfg, desktopDataDirOf(cfg), false, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, "", false, err
	}
	// 首次运行：目录下没有配置 → 落一份推荐配置（含随机 api_key）再加载。
	// 双击 exe / 裸跑 docker 即开，无需先手工复制样例。
	firstRun := false
	key, werr := gen(path)
	if werr == nil {
		firstRun = true
		log.Printf("配置文件 %s 不存在，已生成推荐配置（api_key=%s，记录在该文件里，可自行修改）", path, key)
		cfg, err = Load(path)
	}
	if err != nil {
		// 生成失败（目录只读等）：退回纯默认 + env（旧行为兜底），不阻塞启动。
		log.Printf("配置文件 %s not found (auto-generate failed), using defaults+env: %v", path, err)
		cfg, err = Load("")
		if err != nil {
			return nil, "", false, err
		}
	}
	return cfg, desktopDataDirOf(cfg), firstRun, nil
}

// desktopDataDirOf 取桌面模式数据目录（非桌面模式返回空串）。
// 重新计算而非缓存：数据目录是纯粹由配置推导出的值，不引入额外状态。
func desktopDataDirOf(c *Config) string {
	if !c.Desktop {
		return ""
	}
	if d, err := absClean(resolveDataDir(c)); err == nil {
		return d
	}
	return ""
}

// build 构建全部组件并挂好路由。
func (r *Runtime) build(entry EntryConfig) error {
	cfg := r.Config

	auths, err := auth.LoadDir(cfg.AuthDir)
	if err != nil {
		return fmt.Errorf("加载账号凭证失败（auth_dir=%s）: %w", cfg.AuthDir, err)
	}
	log.Printf("loaded %d account(s) from %s", len(auths), cfg.AuthDir)

	// redisstore：未配置/连接失败 → Noop（纯内存模式，一切功能照常）。
	r.store = redisstore.New(cfg.Upstash.URL, cfg.Upstash.Token)
	r.RedisMode = "noop"
	if _, ok := r.store.(redisstore.Noop); !ok {
		r.RedisMode = "upstash"
	}

	r.Pool = pool.New(cfg.StateFile)
	r.Pool.SetStore(r.store)
	r.Pool.RestoreFromSnapshot() // 择新恢复：Redis 快照比本地新才采用，否则本地优先
	r.Pool.SyncToDir(auths)      // 与 auths 目录对齐：新账号加入、已删除文件账号剔除

	r.Pool.SetBreaker(cfg.Pool.BreakerThreshold, cfg.BreakerCooldownDur, cfg.BreakerCooldownMaxD)
	r.Pool.SetMaxInFlight(cfg.Pool.MaxInFlight)
	r.Pool.SetMaxInFlightGlobal(cfg.Pool.MaxInFlightGlobal)
	r.Pool.SetDegrade(cfg.Pool.DegradeThreshold, cfg.DegradeCooldownDur, cfg.DegradeCooldownMaxD)
	r.Pool.SetSoftRateMax(cfg.SoftRateMaxDur)
	r.Pool.SetCostExploreInterval(cfg.CostExploreIntervalDur)
	r.Pool.SetWeights(cfg.Pool.IdleWeightPerHour, cfg.Pool.IdleWeightMax)

	// 会话粘性路由（可配关闭）。
	if cfg.SessionSticky.Enabled {
		r.Session = session.New(session.Config{
			TTL:        cfg.SessionTTL,
			GCInterval: cfg.SessionGCInterval,
			Store:      r.store,
			Available:  r.Pool.AvailableUIDs,
			// realm 感知闭包：带前缀模型名按 realm 过滤可用账号（跨 realm 不泄漏）；
			// 裸名走 cn（现状零回归）。
			AvailableForModel: realmAwareAvailableForModel(r.Pool),
		})
		r.Session.LoadFromStore()
		r.Session.StartGC()
	}
	sessCount := func() int {
		if r.Session != nil {
			return r.Session.Count()
		}
		return 0
	}

	r.Upstream = upstream.New()
	// 短 RPC 总时长上限（refresh/checkin/balance/FetchModels），语义不变。
	r.Upstream.HTTP.Timeout = time.Duration(cfg.Upstream.TimeoutSeconds) * time.Second
	// 聊天 SSE 首字节前（响应头）上限：cfg 已 normalize（缺省回落 timeout_seconds）。
	r.Upstream.HeaderTimeout = time.Duration(cfg.Upstream.HeaderTimeoutSeconds) * time.Second
	if tr, ok := r.Upstream.ChatHTTP.Transport.(*http.Transport); ok {
		tr.ResponseHeaderTimeout = r.Upstream.HeaderTimeout
	}
	// 聊天 SSE 流中空闲上限（空闲监控读取）。
	r.Upstream.IdleTimeout = time.Duration(cfg.Upstream.IdleTimeoutSeconds) * time.Second
	r.Upstream.SanitizeFingerprints = cfg.Features.SanitizeBlacklistFingerprints
	r.Upstream.UserAgent = cfg.Upstream.UserAgent
	r.Upstream.ClientVersion = cfg.Upstream.ClientVersion
	r.Upstream.CliVersion = cfg.Upstream.CliVersion
	r.Upstream.ClientName = cfg.Upstream.ClientName
	r.Upstream.DeviceToken = cfg.Upstream.DeviceToken
	r.Upstream.DeviceTokenFile = cfg.Upstream.DeviceTokenFile
	r.Upstream.PassthroughIP = cfg.Upstream.PassthroughIP
	// global realm 路由：上游侧开关（第一道闸）+ base 覆盖；auth 侧开关是第二道闸。
	r.Upstream.GlobalEnabled = cfg.Global.Enabled
	r.Upstream.ChatBaseGlobal = cfg.Global.ChatBase
	r.Upstream.BillingBaseGlobal = cfg.Global.BillingBase
	auth.SetGlobalEnabled(cfg.Global.Enabled)
	// model.json 本地缓存接线（context_length/max_output_tokens 四级查找链第 3 级）。
	upstream.SetModelCatalogPath(stateSibling(cfg.StateFile, "model.json"))

	r.Scheduler = scheduler.New(scheduler.Config{
		Pool:               r.Pool,
		Upstream:           r.Upstream,
		CheckinHours:       cfg.Schedule.CheckinHours,
		TravelHours:        cfg.Schedule.TravelHours,
		ActivityHours:      cfg.Schedule.ActivityHours,
		KeepaliveHours:     cfg.Schedule.KeepaliveHours,
		BlackcatHours:      cfg.Schedule.BlackcatHours,
		ExpiringSoonWindow: cfg.ExpiringSoonDur,
		CheckinDisabled:    !cfg.Schedule.CheckinEnabled,
		TravelDisabled:     !cfg.Schedule.TravelEnabled,
		ActivityDisabled:   !cfg.Schedule.ActivityEnabled,
		KeepaliveDisabled:  !cfg.Schedule.KeepaliveEnabled,
		BlackcatDisabled:   !cfg.Schedule.BlackcatEnabled,
	})
	r.logScheduleConfig(cfg)

	// livecfg 承载可热改字段（api_key/soft_rate/脱敏开关），面板保存配置时在线替换。
	r.live = livecfg.New(livecfg.Snapshot{
		APIKey:               cfg.APIKey,
		SoftCooldown:         cfg.SoftRateDur,
		SanitizeFingerprints: cfg.Features.SanitizeBlacklistFingerprints,
	})

	// 用量记录器：与 state 文件同目录，随 state_file 配置一起搬移。
	usagePath := usagePathFor(cfg.StateFile)
	r.Usage = usage.New(usagePath)
	r.Usage.Start()
	log.Printf("[usage] 逐请求用量记录已启用: %s (%s)", usagePath, r.Usage.Describe())

	// 面板配置页的读写闭包：与命令行入口共用同一套 Load/saveConfig，
	// 保证「面板保存」与「启动加载」永远走同一份校验逻辑。
	cfgPath := r.ConfigPath
	r.Panel = panel.New(panel.Config{
		Pool:        r.Pool,
		Usage:       r.Usage,
		Upstream:    r.Upstream,
		Scheduler:   r.Scheduler,
		AuthDir:     cfg.AuthDir,
		APIKey:      cfg.APIKey,
		RedisMode:   r.RedisMode,
		StickyCount: sessCount,
		Version:     AppVersion,
		Live:        r.live,
		ProbeFile:   stateSibling(cfg.StateFile, "output_probes.json"),
		ConfigPath:  cfgPath,
		// 配置页「请求地址」用：实际监听地址（非配置里的 listen，桌面版可能已回退端口），
		// 以及桌面/局域网开放状态。
		ListenAddr: r.Addr,
		AllowLAN:   func() bool { return cfg.Desktop && cfg.DesktopAllowLAN },
		Desktop:    cfg.Desktop,
		LoadConfig: func() (any, error) {
			return Load(cfgPath)
		},
		SaveConfig: func(raw []byte) ([]string, error) {
			// 传 r.Config（运行中配置）作比较基线：saveConfig 用它判定装配期字段
			// 是否真的变了，只有真变了才返回非空的重启字段列表。
			return saveConfig(raw, cfgPath, r.Config, r.live, r.Pool, r.Upstream, r.Scheduler)
		},
		// 桌面壳注入 RestartProcess（不退出应用地重建内核）；服务端入口为 nil，
		// 面板只提示「需重启」，由用户自行重启进程/容器。
		RestartProcess: entry.OnConfigNeedsRestart,
		// 密钥重置：落盘 + 热生效，无需重启（api_key 在 hotAppliedFields 里）。
		// 服务端部署同样可用（没有 UI 交付问题，用户可以自己读响应）。
		RotateAPIKey: r.RotateAPIKey,
	})

	// 日志镜像：标准 log（stderr）与 chat 表格日志（stdout）双路复制进面板环形缓冲。
	// 控制台输出行为完全不变；entry.LogWriter 非空时（桌面模式）额外落盘一份。
	//
	// 用 logfmt.Tee 而不是 io.MultiWriter：MultiWriter 遇错即停，而桌面版是 GUI 子系统
	// （-H windowsgui，无控制台），os.Stderr 写入会失败——排在其后的落盘日志与面板环形
	// 缓冲会一个字节都收不到（应用照常运行，只是日志从启动中段起整段消失）。
	// 详见 internal/logfmt/writer.go 的类型注释。
	logSinks := []io.Writer{os.Stderr, r.Panel.Logs()}
	chatSinks := []io.Writer{os.Stdout, r.Panel.Logs()}
	if entry.LogWriter != nil {
		logSinks = append(logSinks, entry.LogWriter)
		chatSinks = append(chatSinks, entry.LogWriter)
	}
	log.SetOutput(logfmt.Tee(logSinks...))
	server.SetChatLogOutput(logfmt.Tee(chatSinks...))

	r.handler = server.NewHandler(server.Config{
		Pool:          r.Pool,
		Upstream:      r.Upstream,
		APIKey:        cfg.APIKey,
		Session:       r.Session,
		StickyCount:   sessCount,
		RedisMode:     r.RedisMode,
		SoftCooldown:  cfg.SoftRateDur,
		Panel:         r.Panel,
		Live:          r.live,
		Usage:         r.Usage,
		PromptMode:    cfg.Prompt.Mode,
		PromptText:    cfg.PromptText,
		GlobalEnabled: cfg.Global.Enabled,
	})
	return nil
}

// Handler 返回对外 HTTP handler（含 /v1、/status、/healthz、/panel/）。
func (r *Runtime) Handler() http.Handler { return r.handler }

// Start 开始监听并启动调度器。返回后服务在后台运行，用 Close 收尾。
func (r *Runtime) Start() error {
	if r.started {
		return errors.New("Runtime 已启动")
	}
	target := r.listenTarget()
	ln, err := net.Listen("tcp", target)
	if err != nil {
		return fmt.Errorf("监听 %s 失败（端口被占用？）: %w", target, err)
	}
	r.listener = ln
	r.started = true

	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	go r.Scheduler.Run(ctx)
	r.Scheduler.StartBalanceRefresh(ctx, r.Config.BalanceRefreshInterval)

	r.srv = &http.Server{
		Handler:           r.handler,
		ReadHeaderTimeout: 30 * time.Second,
		// ReadTimeout 覆盖整个请求读取（含 body）：防慢速 body 拖死连接。
		// 请求体已无网关侧上限，60s 按常规带宽的数十 MB 上传余量取值。
		ReadTimeout: 60 * time.Second,
		// IdleTimeout keep-alive 空闲连接回收。
		// 不设全局 WriteTimeout（长流式生成合法时长可达数分钟，会误杀在途 SSE）。
		IdleTimeout: 120 * time.Second,
	}
	go func() {
		if err := r.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("http 服务退出: %v", err)
		}
	}()
	if r.listenAddr != "" && r.listenAddr != r.Config.Listen {
		// 如实描述实际绑定范围：绑到未指定地址（0.0.0.0/[::]）＝已开放局域网，
		// 与"只绑回环"是两种完全不同的暴露面，日志里不能都说成"回环"。
		scope := "回环+动态端口"
		if isUnspecifiedAddr(r.listenAddr) {
			scope = "所有网卡（已开启局域网访问）"
		}
		log.Printf("workbuddy2api listening on %s（配置写的 listen=%s，桌面模式改用%s）(api_key=%v)，管理面板 %s/panel/",
			r.Addr(), r.Config.Listen, scope, r.Config.APIKey != "", r.BaseURL())
	} else {
		log.Printf("workbuddy2api listening on %s (api_key=%v)，管理面板 %s/panel/",
			r.Addr(), r.Config.APIKey != "", r.BaseURL())
	}
	return nil
}

// Addr 实际监听地址（":" + 端口）。
func (r *Runtime) Addr() string {
	if r.listener != nil {
		return r.listener.Addr().String()
	}
	return r.listenTarget()
}

// BaseURL 本机访问地址，例如 http://127.0.0.1:7863。
func (r *Runtime) BaseURL() string {
	return "http://127.0.0.1" + panelListenPath(r.Addr())
}

// PanelURL 面板完整地址。
func (r *Runtime) PanelURL() string { return r.BaseURL() + "/panel/" }

// StopHTTP 只停 HTTP 与调度（保留组件状态），用于换端口重启等场景。
func (r *Runtime) StopHTTP() {
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	if r.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = r.srv.Shutdown(ctx)
		r.srv = nil
	}
}

// Close 释放全部资源（幂等）。停机序：先停 HTTP/调度 → 落盘 → 关 Redis 连接。
func (r *Runtime) Close() {
	r.StopHTTP()
	if r.Session != nil {
		r.Session.StopGC()
	}
	if r.Usage != nil {
		r.Usage.Stop()
	}
	if r.Pool != nil {
		// 最后一次 Flush → SaveState 已提交到 store，再关 store 才不会丢最后一笔镜像。
		r.Pool.Close()
	}
	if r.store != nil {
		_ = r.store.Close()
	}
}

// logScheduleConfig 启动期把排程与开关打进日志（用户排查「为什么没签到」的第一站）。
func (r *Runtime) logScheduleConfig(cfg *Config) {
	s := cfg.Schedule
	switch {
	case !s.CheckinEnabled:
		log.Printf("签到已禁用（schedule.checkin_enabled=false）")
	default:
		log.Printf("签到已启用：%v 点（签到 + 余额查询解冻）", s.CheckinHours)
	}
	switch {
	case !s.TravelEnabled:
		log.Printf("猫猫旅行已禁用（schedule.travel_enabled=false）")
	default:
		log.Printf("猫猫旅行已启用：%v 点（独立排程：领养 / 派出 / 领奖）", s.TravelHours)
	}
	switch {
	case !s.ActivityEnabled:
		log.Printf("活跃上报已禁用（schedule.activity_enabled=false）")
	default:
		log.Printf("活跃上报已启用：%v 点（每日 1 次，点亮连登 + 解锁 first_buddy）", s.ActivityHours)
	}
	if !s.KeepaliveEnabled {
		log.Printf("token 保活已禁用（schedule.keepalive_enabled=false）")
	} else {
		log.Printf("token 保活已启用：%v 点", s.KeepaliveHours)
	}
	switch {
	case !s.BlackcatEnabled:
		log.Printf("夜猫子已禁用（schedule.blackcat_enabled=false）")
	default:
		log.Printf("夜猫子已启用：%v 点（23:00–08:00 窗口 glm-5.2 对话补足）", s.BlackcatHours)
	}
	switch {
	case !s.BalanceRefreshEnabled:
		log.Printf("余额后台刷新已禁用（schedule.balance_refresh_enabled=false）")
	case cfg.BalanceRefreshInterval > 0:
		log.Printf("余额后台刷新：每 %s（签到时点照常额外刷新）", cfg.BalanceRefreshInterval)
	}
}

// usagePathFor 由 state 文件路径推出用量文件路径：同目录、文件名 usage.json。
// 这样 config 里改 state_file 时用量数据跟着走，不需要额外配置项。
func usagePathFor(stateFile string) string { return stateSibling(stateFile, "usage.json") }

// stateSibling 返回与 state 文件同目录的指定文件名路径（相对路径场景回落当前目录）。
func stateSibling(stateFile, name string) string {
	dir := filepath.Dir(stateFile)
	if dir == "" || dir == "." {
		return name
	}
	return filepath.Join(dir, name)
}

// isUnspecifiedAddr 判断监听地址是否绑到"所有网卡"（0.0.0.0 或 [::]）。
//
// 判断要基于**解析后的 IP** 而不是字符串比较：Go 对 "0.0.0.0:7863" 会返回
// "[::]:7863"（监听双栈），字面量比较必然漏判，于是日志把"已开放局域网"错说成"回环"。
func isUnspecifiedAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsUnspecified()
}

func panelListenPath(listen string) string {
	for i := len(listen) - 1; i >= 0; i-- {
		if listen[i] == ':' {
			return listen[i:]
		}
	}
	return listen
}

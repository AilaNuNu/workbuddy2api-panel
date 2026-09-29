// desktop 是**独立 Go 模块**，不是根模块的一部分。
//
// 原因：Wails v3 要求 go >= 1.25，而根模块（服务端/CLI）保持 go 1.22.5，
// Dockerfile 用 golang:1.23-alpine 构建。若把 Wails 加进根模块，go.mod 会被
// 强行抬到 1.25，现有 Docker 构建与 Go 1.22/1.23 的构建机全部失效。
//
// 独立模块后：
//   - 根模块 go build ./... 不会看到 desktop/（嵌套模块被排除）→ 服务端链路零影响
//   - 桌面版单独构建：cd desktop && go build
//   - 通过 replace 直接引用根模块源码，改动即时生效，无需发版本
module github.com/linguo2625469/workbuddy2api-panel/desktop

go 1.25.0

require (
	github.com/linguo2625469/workbuddy2api-panel v0.0.0
	github.com/wailsapp/wails/v3 v3.0.0-beta.25
)

require (
	github.com/adrg/xdg v0.5.3 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/coder/websocket v1.8.14 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/go-ole/go-ole v1.3.0 // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/redis/go-redis/v9 v9.18.0 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
)

replace github.com/linguo2625469/workbuddy2api-panel => ../

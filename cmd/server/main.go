// main.go workbuddy2api 命令行入口：加载配置 → 装配 → 起 HTTP 服务与调度器 →
// 等待退出信号并优雅停机。
//
// 装配逻辑（配置、账号池、上游客户端、调度器、面板、日志镜像）全部在
// internal/appcore，本文件只负责进程级关注点：命令行参数、信号、退出码。
// 桌面壳（cmd/desktop）复用同一个 appcore，避免两套装配漂移。
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/linguo2625469/workbuddy2api-panel/internal/appcore"
)

func main() {
	cfgPath := flag.String("config", "config.json", "配置文件路径（默认当前目录 config.json；不存在时自动生成推荐配置）")
	flag.Parse()

	app, err := appcore.New(appcore.EntryConfig{ConfigPath: *cfgPath})
	if err != nil {
		log.Fatalf("%v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := app.Start(); err != nil {
		app.Close()
		log.Fatalf("%v", err)
	}

	<-ctx.Done()
	log.Printf("收到退出信号，正在停机…")
	app.Close() // 先落盘（Flush → SaveState 已提交到 store）再关连接
	log.Printf("bye")
}

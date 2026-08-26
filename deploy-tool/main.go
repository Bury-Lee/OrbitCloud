// main.go —— OrbitCloud 部署运维工具入口(Wails 单窗口)。
// 职责: 加载工具配置 → 构建 App 绑定对象 → 启动窗口。
package main

import (
	"context"
	"embed"

	"deploy-tool/internal/config"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

// appInstance 当前 App 单例(绑定对象)。
var appInstance *App

func main() {
	// 1. 读取工具配置(config.json,不存在则自动生成默认值)
	cfg := config.Load()

	// 2. 构建远程执行器;默认原生 SSH(独立工具),失败不致命,设置页可修复
	exe, err := newExecer(cfg)
	if err != nil {
		exe = nil // 设置页"测试连接/保存"时会重建
	}

	// 3. 组装绑定对象
	appInstance = NewApp(cfg, exe)

	// 4. 启动窗口
	err = wails.Run(&options.App{
		Title:  "OrbitCloud 部署运维工具",
		Width:  1100,
		Height: 750,
		MinWidth:  900,
		MinHeight: 600,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 245, G: 246, B: 250, A: 1},
		OnStartup: func(ctx context.Context) {
			appInstance.startup(ctx)
		},
		Bind: []interface{}{
			appInstance,
		},
	})
	if err != nil {
		println("Error:", err.Error())
	}
}

package main

// main 应用入口:加载配置 → 初始化(appinit 包)→ 启动 HTTP 服务,
// 服务启动失败或收到退出信号时优雅停机。

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"orbitcloud/api"
	"orbitcloud/appinit"
	"orbitcloud/core"
	"orbitcloud/cron"
	"orbitcloud/flag"
	"orbitcloud/log"
	"orbitcloud/utils"
)

func main() {
	defer func() { // 发生 panic 时打印堆栈后崩溃,不恢复
		fmt.Printf("\norbitcloud panic stack:\n%s\n", string(utils.Stack(1)))
	}()
	// 0. 先处理无需初始化的指令(flag.RunPreInit):-initConfig 用内置默认配置强制覆盖
	//    生成 ./config.yaml 后退出;必须在 appinit 初始化之前执行。
	if err := flag.RunPreInit(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrExit) {
			os.Exit(0)
		}
		fmt.Printf("main: %v", err)
	}

	// 1. 加载配置并初始化全局基础设施(见 appinit 包):配置 → 日志 → 数据库
	//    (自动建表+重连)→ [日志入库] → JWT → 对象存储 → 协程池,结果存入 core 全局变量。
	cfg, err := appinit.InitConfig()
	if err != nil {
		log.Fatalf("main: load config: %v", err)
	}
	core.GlobalConfig = cfg

	appinit.InitLog(cfg)

	db, err := appinit.InitDB(cfg)
	if err != nil {
		log.Fatalf("main: init database: %v", err)
	}
	core.DB = db
	_ = appinit.AttachLogDBWriter(cfg) // 可选:日志入库(config.Log.DBLevel 非空时,实现见 log/dbwriter.go)

	core.JWT = appinit.InitJWT(cfg)

	storage, err := appinit.InitStorage(cfg)
	if err != nil {
		// 与 InitDB 对齐:对象存储初始化失败直接终止启动,避免运行期 panic
		log.Fatalf("main: init object storage: %v", err)
	}
	core.Storage = storage

	core.Pool = appinit.InitPool(cfg)
	log.Infof("orbitcloud core initialized (version %s, env %s)", cfg.App.Version, cfg.App.Environment)

	// 2. 执行命令行指令(flag.Run):需 DB 已就绪,故放在初始化之后;
	//    命中指令则执行后以退出码 0 退出,不启动服务。
	if err := flag.Run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrExit) {
			os.Exit(0)
		}
		log.Fatalf("main: %v", err)
	}

	// 3. 组装 HTTP 服务并启动(协程,不阻塞主流程)
	httpServer := &http.Server{
		Addr:         fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port), // 默认 0.0.0.0:8080
		Handler:      api.Router(),                                           // 框架:Gin;*gin.Engine 实现 http.Handler
		ReadTimeout:  time.Duration(cfg.Server.ReadTimeout) * time.Minute,    // 0=不限
		WriteTimeout: time.Duration(cfg.Server.WriteTimeout) * time.Minute,   // 0=不限,下载流式返回必需
		IdleTimeout:  time.Duration(cfg.Server.IdleTimeout) * time.Minute,    // 0=不限
	}
	if cfg.Server.MaxHeaderBytes > 0 {
		httpServer.MaxHeaderBytes = cfg.Server.MaxHeaderBytes
	}

	go func() {
		log.Infof("orbitcloud HTTP server listening on %s", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Errorf("main: http server error: %v", err)
		}
	}()

	// 4. 启动后台任务:删除/复制任务启动时续跑一次,并定时清理过期日志/令牌/分享
	if err := cron.ResumeDeleteTasks(context.Background()); err != nil {
		log.Errorf("main: resume delete tasks: %v", err)
	}
	if err := cron.ResumeCopyTasks(context.Background()); err != nil {
		log.Errorf("main: resume copy tasks: %v", err)
	}
	cron.Start(cfg, core.DB)

	// 5. 优雅停机:监听 SIGINT/SIGTERM,收到信号后关闭 HTTP 服务(10s 超时)、
	//    DB 连接与协程池,然后退出。
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh
	log.Info("main: shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Errorf("main: http shutdown: %v", err)
	}
	if core.Pool != nil {
		core.Pool.Close()
	}
	if sqlDB, err := core.DB.DB(); err == nil {
		_ = sqlDB.Close()
	}
	log.Info("main: bye")
}

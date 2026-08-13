package appinit

// 日志初始化:输出目标/级别(InitLog),以及可选的数据落库(AttachLogDBWriter)。

import (
	"fmt"
	"io"
	"os"
	"strings"

	"orbitcloud/config"
	"orbitcloud/log"
)

// InitLog 初始化日志(输出目标/级别/轮转)。在 DB 就绪前调用(日志不依赖 DB)。
func InitLog(cfg *config.Config) {
	l := cfg.Log
	// 级别映射:非法 → INFO
	level := log.INFO
	switch strings.ToLower(l.Level) {
	case "debug":
		level = log.DEBUG
	case "info":
		level = log.INFO
	case "warn":
		level = log.WARN
	case "error":
		level = log.ERROR
	}

	// 输出目标:OutputPath 为空 → stdout;非空 → 轮转文件 writer
	// (按日期建子文件夹 {dir}/{YYYY-MM-DD}/、文件名 {prefix}-{HH-MM-SS}.log,
	// 单文件超过 MaxSize(KB)或跨天自动切换,见 log.NewRotateWriter)
	var out io.Writer = os.Stdout
	if strings.TrimSpace(l.OutputPath) != "" {
		rw, err := log.NewRotateWriter(l.OutputPath, int64(l.MaxSize))
		if err != nil {
			// 日志尚未初始化,直接落 stdout 并提示
			fmt.Fprintf(os.Stderr, "log: init rotate writer failed (%v), fallback to stdout\n", err)
		} else {
			out = rw
		}
	}

	log.Init(out, level)
}

// AttachLogDBWriter 可选:日志入库(config.Log.DBLevel 非空时启用;main 在 DB 就绪后调用)。
// 写库实现(logs 表 INSERT、失败重试、本地补记)在 log 包(见 log/dbwriter.go)——
// log 包直接读取 core 全局变量判断并注册内部 sink,本函数为薄封装,便于 main 按序调用。
func AttachLogDBWriter(_ *config.Config) error {
	log.EnableLogDBWriter()
	return nil
}

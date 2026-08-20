// pool.go — agilePool / AdmissionPool / ExecPool 初始化。
package appinit

import (
	agilepool "github.com/Yiming1997/agilePool/v2"

	"orbitcloud/config"
	"orbitcloud/core"
)

// InitPool 构造全局协程池(agilePool),供后台任务(Delete/Copy 任务)使用。
func InitPool(cfg *config.Config) *agilepool.Pool {
	maxWorkers, queueSize := cfg.Pool.MaxWorkers, cfg.Pool.QueueSize
	if maxWorkers <= 0 {
		maxWorkers = 64
	}
	if queueSize <= 0 {
		queueSize = 1024
	}
	return agilepool.NewPool(agilepool.NewConfig(
		agilepool.WithWorkerNumCapacity(int64(maxWorkers)),
		agilepool.WithTaskQueueSize(int64(queueSize)),
	))
}

// InitExecPool 构造全局请求执行池(agilePool),供非流式 API handler 使用。
func InitExecPool(cfg *config.Config) *agilepool.Pool {
	return agilepool.NewPool(agilepool.NewConfig(
		agilepool.WithWorkerNumCapacity(int64(cfg.ExecPool.MaxWorkers)),
		agilepool.WithTaskQueueSize(int64(cfg.ExecPool.QueueSize)),
	))
}

// InitStreamExecPool 构造流式请求执行池(agilePool),供下载/预览/流媒体 handler 使用。
func InitStreamExecPool(cfg *config.Config) *agilepool.Pool {
	return agilepool.NewPool(agilepool.NewConfig(
		agilepool.WithWorkerNumCapacity(int64(cfg.ExecPool.StreamWorkers)),
		agilepool.WithTaskQueueSize(int64(cfg.ExecPool.StreamQueue)),
	))
}

// InitAdmissionPool 构造 AdmissionPool(请求准入池,供 StreamingPoolMiddleware 使用)。
func InitAdmissionPool(cfg *config.Config) *core.AdmissionPool {
	return core.NewAdmissionPool(
		cfg.RequestPool.StreamingMax,
		0, // 流式不排队
		core.AdmissionModeBlock,
	)
}
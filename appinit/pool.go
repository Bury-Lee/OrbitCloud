package appinit

// agilePool 协程池初始化(由 main 赋值给 core.Pool)。

import (
	agilepool "github.com/Yiming1997/agilePool/v2"

	"orbitcloud/config"
)

// InitPool 构造全局协程池(agilePool;约定:所有并发必须经 agilePool,禁止裸起协程逃逸)。
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

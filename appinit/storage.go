package appinit

// 对象存储驱动初始化(接口与实现仍在 core;由 main 赋值给 core.Storage)。

import (
	"context"
	"fmt"
	"time"

	"orbitcloud/config"
	"orbitcloud/core"
	"orbitcloud/log"
)

// 对象存储连通性自检参数(与数据库初始化对齐:失败重试,进程退出前不放弃)。
// 50 次 × 5s ≈ 250s(约 4 分钟),覆盖存储集群滚动重启/网络抖动窗口。
const (
	storageRetryTimes    = 50
	storageRetryInterval = 5 * time.Second
)

// InitStorage 构造对象存储驱动并做连通性自检。
//   - 驱动不合法(配置错误):直接返回错误,不重试;
//   - 驱动合法但 endpoint 不可达/凭据错误:Ping 自检失败,重试 storageRetryTimes 次,
//     每次间隔 storageRetryInterval;耗尽后返回最终错误(由 main 终止启动,避免
//     core.Storage 为 nil 时运行期 panic)。
func InitStorage(cfg *config.Config) (core.ObjectStorage, error) {
	driver := cfg.Storage.Driver
	if driver == "" {
		driver = "s3"
	}

	storage, err := core.NewObjectStorage(driver, &cfg.Storage)
	if err != nil {
		// 配置错误(如 driver 不支持 / endpoint 缺失):无重试必要,直接返回
		return nil, err
	}

	ctx := context.Background()
	var pingErr error // 保留最近一次 Ping 错误(避免 if 作用域遮蔽)
	for attempt := 1; ; attempt++ {
		pingErr = storage.Ping(ctx)
		if pingErr == nil {
			return storage, nil
		}
		if attempt >= storageRetryTimes {
			return nil, fmt.Errorf("storage: init failed after %d attempts (driver %q): %w",
				storageRetryTimes, driver, pingErr)
		}
		log.Warnf("storage: ping failed (attempt %d/%d), retrying in %v: %v",
			attempt, storageRetryTimes, storageRetryInterval, pingErr)
		time.Sleep(storageRetryInterval)
	}
}

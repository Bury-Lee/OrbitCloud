// Package core 的最底层基础设施之一:进程实例标识。
// 删除/复制任务锁(LockOwner)由 server 与 cron 共用,故实例标识放 core,避免 server→cron 循环依赖。
package core

import (
	"fmt"
	"os"
)

// InstanceID 返回进程实例标识(hostname-pid),作为删除/复制任务锁持有者。
// 条件更新抢占 + 持有者条件释放,保证多实例并发下同一任务仅一方处理;
// 进程崩溃后锁随过期时间被其他实例接管。
func InstanceID() string {
	host, _ := os.Hostname()
	if host == "" {
		host = "unknown"
	}
	return fmt.Sprintf("%s-%d", host, os.Getpid())
}

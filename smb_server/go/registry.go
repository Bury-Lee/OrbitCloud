// registry.go —— 远程句柄表与便捷编解码工具。
//
// HandleRegistry:句柄 ID 分配、查找、过期回收(跨连接共享,带锁)。
// 句柄是 SMB CREATE 在 Go 侧的运行时代表(见 file_ops.go remoteHandle),
// Rust 侧仅持有句柄 ID,后续 Read/Write/Stat/Close 等操作凭 ID 定位。
package smbgateway

import (
	"encoding/json" // JSON 编解码(便捷封装)
	"orbitcloud/log"           // 服务日志(句柄生命周期记录)
	"sync"          // 读写锁(句柄表保护)
	"time"          // TTL 类型
)

// HandleRegistry 远程句柄表:句柄 ID 分配、查找、过期回收。
// 线程安全:内部 RWMutex(读多写少,读循环高频 Get)。
type HandleRegistry struct {
	// mu 保护 handles 映射。
	mu sync.RWMutex
	// handles 句柄 ID → 远程句柄(remoteHandle 见 file_ops.go)。
	handles map[uint64]*remoteHandle
	// nextID 句柄 ID 自增器(1 起)。
	nextID uint64
	// ttl 句柄空闲 TTL(超过即回收;默认 30min)。
	ttl time.Duration
}

// NewHandleRegistry 构造空句柄表(句柄 ID 从 1 开始,0 保留非法)。
func NewHandleRegistry() *HandleRegistry {
	return &HandleRegistry{
		handles: make(map[uint64]*remoteHandle), // 空句柄表
		nextID:  1, // ID 从 1 起(0 为非法哨兵)
		ttl:     30 * time.Minute, // 默认空闲 TTL 30 分钟
	}
}

// Alloc 分配新句柄 ID 并登记。
// 参数:h 待登记的远程句柄(内部回填 handleID)。
// 返回值:分配到的句柄 ID(全局唯一,多连接不冲突)。
func (r *HandleRegistry) Alloc(h *remoteHandle) uint64 {
	r.mu.Lock() // 写锁
	defer r.mu.Unlock() // 解锁
	id := r.nextID // 取当前序号
	r.nextID++ // 自增(单调,永不回用)
	h.handleID = id // 回填句柄 ID
	r.handles[id] = h // 登记
	log.Infof("smb_gateway: 分配句柄 id=%d 路径=%q 目录=%v 连接=%d(共=%d)", id, h.smbPath, h.isDir, h.connID, len(r.handles)) // 记录句柄分配(ID/路径/类型/连接/总数)
	return id // 返回新 ID
}

// Get 按句柄 ID 查找句柄,不存在返回 nil。
// 用途:Read/Write/Stat/Flush/Truncate/Close 等操作的定位入口。
func (r *HandleRegistry) Get(id uint64) *remoteHandle {
	r.mu.RLock() // 读锁(高频读可并发)
	defer r.mu.RUnlock() // 解锁
	return r.handles[id] // 查表(nil = 不存在)
}

// Delete 注销句柄(Close 后调用;防 ID 复用与句柄泄漏)。
func (r *HandleRegistry) Delete(id uint64) {
	r.mu.Lock() // 写锁
	defer r.mu.Unlock() // 解锁
	delete(r.handles, id) // 移除登记
	log.Infof("smb_gateway: 注销句柄 id=%d(剩余=%d)", id, len(r.handles)) // 记录句柄注销(剩余总数)
}

// All 返回全部句柄快照(断连批量回收用,见 file_dir.go CloseAllByConn)。
func (r *HandleRegistry) All() []*remoteHandle {
	r.mu.RLock() // 读锁
	defer r.mu.RUnlock() // 解锁
	out := make([]*remoteHandle, 0, len(r.handles)) // 预分配
	for _, h := range r.handles { // 遍历拷贝
		out = append(out, h)
	}
	return out // 返回快照
}

// ReapExpired 回收超时未活动的句柄(定时任务调用;当前为空实现,
// 句柄回收由连接生命周期与 Close 保证,TTL 扫描为后续增强)。
func (r *HandleRegistry) ReapExpired(now time.Time) {
	_ = now // 占位:当前不做 TTL 扫描(连接关闭时统一回收)
}

// encodeJSON 便捷:结构体 → JSON(帧 body 统一入口)。
func encodeJSON(v any) ([]byte, error) {
	return json.Marshal(v) // 委托标准库
}

// decodeJSON 便捷:JSON → 结构体(帧 body 解码)。
func decodeJSON(body []byte, v any) error {
	return json.Unmarshal(body, v) // 委托标准库
}

// write_cache.go —— 写回缓存:解决 S3 不支持随机写(可行性报告 §4.2)。
//
// 原理:SMB WRITE 是"按偏移写"语义,而 minio Put 只能整体覆盖对象。
// 方案:写入块先落在内存段表,close/flush 时把"原对象未覆盖区间 + 缓存区间"
// 拼成完整新对象整体上传(Put)。local 驱动天然支持随机写,可先验证全链路。
//
// 职责边界(重构后):
//   - 本缓存只做**数据缓冲**:段表(排序/合并,后写覆盖)+ 脏标记 + 上限;
//   - 文件级状态(逻辑 EOF / 截断点)属于 remoteHandle(见 file_ops.go),
//     冲刷前在句柄锁内一次性取为 writeSpec 快照,拼装阶段无锁;
//   - 截断语义:truncateAt = 对象旧数据"可信上界"(作废 [truncateAt, ∞) 的
//     旧对象数据,拼装时零填充),杜绝"截断后越界写导致旧数据复活"。
package smbgateway

import (
	"bytes"    // 拼装缓冲(组装完整对象)
	"context"  // 上下文
	"errors"   // errors.Is(对象不存在)
	"io"       // 流复制(限长)
	"orbitcloud/log"      // 服务日志(整体冲刷记录)
	"sort"     // 段排序
	"sync"     // 互斥锁(段表保护)

	"orbitcloud/core" // 对象存储抽象
)

// writeRange 一段已写缓存(半开区间 [off, end))。
type writeRange struct {
	off  int64 // 起始偏移
	end  int64 // 结束偏移(开区间)
	data []byte // 段数据
}

// writeSpec 一次冲刷的完整状态快照(句柄锁内一次性取出,拼装阶段无锁、无 race)。
type writeSpec struct {
	// size 逻辑 EOF(remoteHandle.size 权威值:截断覆盖、写扩展均反映于此;
	// 拼装 total 以此为唯一依据,段尾超出部分裁剪)。
	size int64
	// truncateAt 对象旧数据可信上界(-1 = 无截断,对象全量可读;
	// >= 0 = 截断点,对象 [truncateAt, ∞) 的旧数据作废,拼装时零填充)。
	truncateAt int64
	// segments 未冲刷写段快照(排序,由段表拷贝)。
	segments []writeRange
	// dirty 是否有需落盘的变化(写段或截断待固化)。
	dirty bool
}

// WriteBackCache 写回缓存:纯数据缓冲(段表 + 合并 + 整体冲刷)。
type WriteBackCache struct {
	// mu 保护段表与 dirty 标记。
	mu sync.Mutex
	// ranges 已写入段(排序,合并相邻/重叠段)。
	ranges []writeRange
	// maxSize 缓存总字节上限(超限触发提前 Flush,默认 4 MiB)。
	maxSize int64
	// dirty 是否有未冲刷写入。
	dirty bool
}

// NewWriteBackCache 构造空写回缓存(默认上限 4 MiB)。
func NewWriteBackCache() *WriteBackCache {
	return &WriteBackCache{
		maxSize: 4 << 20, // 上限 4 MiB
	}
}

// ApplyWrite 写入一段(合并/覆盖段表)。
// 参数:offset 偏移;data 数据(内部拷贝,调用方可复用缓冲区)。
// 返回值:当前缓存总量(供调用方判断是否提前 Flush)。
func (c *WriteBackCache) ApplyWrite(offset int64, data []byte) int64 {
	c.mu.Lock() // 锁段表
	defer c.mu.Unlock() // 解锁
	seg := writeRange{off: offset, end: offset + int64(len(data)), data: append([]byte(nil), data...)} // 深拷贝数据成段(防调用方复用缓冲区)
	// 插入并合并相邻/重叠段。
	c.ranges = append(c.ranges, seg) // 追加段
	c.mergeLocked() // 排序合并
	c.dirty = true // 标记脏
	return c.sizeLocked() // 返回当前总量
}

// Ranges 返回排序后的段副本(读路径使用)。
func (c *WriteBackCache) Ranges() []writeRange {
	c.mu.Lock() // 锁段表
	defer c.mu.Unlock() // 解锁
	return c.snapshotLocked() // 返回拷贝
}

// ReadRange 读一段(优先命中缓存,未覆盖区间返回 nil 供调用方走 GetRange)。
// 参数:offset 起始偏移;length 长度。
// 返回值:缓存命中部分;nil 表示该区间完全未缓存(含部分命中缺口的语义)。
func (c *WriteBackCache) ReadRange(offset, length int64) []byte {
	c.mu.Lock() // 锁段表
	defer c.mu.Unlock() // 解锁
	segments := c.snapshotLocked() // 段快照
	out := make([]byte, 0, length) // 输出缓冲
	cursor := offset // 拼装游标
	end := offset + length // 读取终点
	for _, s := range segments { // 遍历段(有序)
		if s.end <= cursor { // 段在游标前?
			continue // 跳过
		}
		if s.off >= end { // 段起点越过终点?
			break // 结束
		}
		from := max64(cursor, s.off) // 重叠起点
		to := min64(s.end, end) // 重叠终点
		if to > from { // 有重叠?
			out = append(out, s.data[from-s.off:to-s.off]...) // 取段内切片拼装
			cursor = to // 游标前移
		}
	}
	if int64(len(out)) == length { // 完全命中?
		return out // 返回缓存数据
	}
	return nil // 有缺口:调用方走对象存储
}

// FlushToStorage 按冲刷快照 spec 把缓存整体写回对象存储(拼装阶段无锁)。
//
// 参数:ctx/st/bucket/key 对象存储上下文;spec 冲刷快照(句柄锁内取出)。
// 返回值:错误。
//
// 实现步骤:
//  1. 非 dirty(无写段且无截断待固化)直接返回;
//  2. total = spec.size(逻辑 EOF 权威;段尾超出部分裁剪,截断覆盖部分丢弃);
//  3. 对象读取上界 objLimit = spec.truncateAt(-1 = 无截断 → 全量可读);
//     间隙 [from, min(to, objLimit)) 从对象读,其余零填充(截断区作废/扩展区补零);
//  4. 整体 Put 上传;成功后清段表与脏标记。
func (c *WriteBackCache) FlushToStorage(ctx context.Context, st core.ObjectStorage, bucket, key string, spec writeSpec) error {
	if !spec.dirty { // 无脏数据?
		return nil // 无需上传
	}
	segments := spec.segments // 段快照(排序)

	// 目标总长:逻辑 EOF 唯一权威(截断覆盖/写扩展均已反映在 spec.size)
	total := spec.size

	// 对象旧数据可读上界:截断点优先,无截断读全量
	objLimit := total
	if spec.truncateAt >= 0 && spec.truncateAt < objLimit {
		objLimit = spec.truncateAt
	}

	// 按序拼装:间隙从原对象读(不超过 objLimit,超出零填充),覆盖段用缓存。
	buf := &bytes.Buffer{} // 新对象缓冲
	cursor := int64(0) // 拼装游标
	for _, s := range segments { // 遍历段
		if s.off >= total { // 段起点已超目标长?
			break // 截断:忽略后续段
		}
		if s.off > cursor { // 段前有间隙?
			if err := c.copyFromObject(ctx, st, bucket, key, buf, cursor, min64(s.off, total), objLimit); err != nil { // 从原对象补洞(截断上界内)
				return err // 失败上抛
			}
			cursor = min64(s.off, total) // 游标前移
		}
		to := min64(s.end, total) // 段在目标长内的终点
		if to > cursor { // 有可写部分?
			buf.Write(s.data[cursor-s.off : to-s.off]) // 写缓存数据
			cursor = to // 游标前移
		}
	}
	if cursor < total { // 尾部仍有不足?
		if err := c.copyFromObject(ctx, st, bucket, key, buf, cursor, total, objLimit); err != nil { // 补洞到目标长(截断上界内)
			return err // 失败上抛
		}
	}

	// 整体上传。
	if err := st.Put(ctx, bucket, key, bytes.NewReader(buf.Bytes()), int64(buf.Len())); err != nil { // 整体覆盖对象
		return err // 失败上抛(段表保留,可重试)
	}
	log.Debugf("smb_gateway: 写回冲刷 桶=%s 键=%s 共=%d 字节(%d 段,截断=%d)", bucket, key, buf.Len(), len(segments), spec.truncateAt) // 记录整体冲刷(桶/键/大小/段数/截断点)
	// 成功:清段表与脏标记。
	c.mu.Lock() // 锁段表
	c.ranges = nil // 清段表
	c.dirty = false // 清脏标记
	c.mu.Unlock() // 解锁
	return nil
}

// copyFromObject 把对象 [from, to) 区间复制到 buf;对象不存在/不足 → 零填充。
// 截断上界 limit:仅 [from, min(to, limit)) 从对象读,其余零填充
// (truncate 作废的旧数据不复活,扩展区补零)。
// 说明:这是组装完整对象时的"补洞"手段,与读路径(绝不零填充)语义不同。
func (c *WriteBackCache) copyFromObject(ctx context.Context, st core.ObjectStorage, bucket, key string, buf *bytes.Buffer, from, to, limit int64) error {
	if to <= from { // 空区间?
		return nil // 无事可做
	}
	rdTo := min64(to, limit) // 对象实际可读终点(截断上界内)
	if rdTo > from { // 有可读部分?
		rc, err := st.GetRange(ctx, bucket, key, from, rdTo-1) // 范围读原对象
		if err != nil { // 读失败?
			if errors.Is(err, core.ErrObjectNotFound) { // 对象不存在(新文件)?
				buf.Write(make([]byte, to-from)) // 全零填充
				return nil
			}
			return err // 其它错误上抛
		}
		defer rc.Close() // 关闭读流
		n, err := io.CopyN(buf, rc, rdTo-from) // 限长复制
		if err == io.EOF || err == io.ErrUnexpectedEOF { // 对象比预期短?
			buf.Write(make([]byte, (rdTo-from)-n)) // 补足差额零
			return nil
		}
		if err != nil { // 其它读错误?
			return err // 上抛
		}
	}
	if to > rdTo { // 截断作废区/扩展区?
		buf.Write(make([]byte, to-rdTo)) // 零填充
	}
	return nil
}

// mergeLocked 合并相邻/重叠段(须持锁)。
// 规则:按 off 排序;重叠部分以新数据为准(后写覆盖)。
func (c *WriteBackCache) mergeLocked() {
	if len(c.ranges) < 2 { // 段不足 2?
		return // 无需合并
	}
	sort.Slice(c.ranges, func(i, j int) bool { return c.ranges[i].off < c.ranges[j].off }) // 按偏移排序
	merged := make([]writeRange, 0, len(c.ranges)) // 合并结果
	for _, s := range c.ranges { // 逐个处理
		if len(merged) == 0 { // 首个段?
			merged = append(merged, s) // 直接入结果
			continue
		}
		last := &merged[len(merged)-1] // 结果末段(可能被扩展)
		if s.off <= last.end { // 相邻或重叠?
			// 相邻或重叠:合并。
			if s.end <= last.end { // 完全被覆盖?
				continue // 丢弃新段(旧段已含该区间)
			}
			// 重叠部分以新数据为准(后写覆盖)。
			keep := last.data // 旧段数据(保留非重叠前缀)
			keepLen := s.off - last.off // 应保留长度
			if keepLen > int64(len(keep)) { // 越界保护?
				keepLen = int64(len(keep)) // 取实际长度
			}
			last.data = append(append([]byte(nil), keep[:keepLen]...), s.data...) // 旧前缀 + 新数据(后写覆盖)
			last.end = s.end // 扩展末段终点
		} else { // 独立段
			merged = append(merged, s) // 直接入结果
		}
	}
	c.ranges = merged // 写回合并结果
}

// sizeLocked 当前缓存总量(须持锁)。
func (c *WriteBackCache) sizeLocked() int64 {
	var total int64 // 累计值
	for _, s := range c.ranges { // 遍历段
		total += int64(len(s.data)) // 累加数据长度
	}
	return total // 返回总量
}

// snapshotLocked 段表快照(须持锁;返回拷贝,调用方可安全使用)。
func (c *WriteBackCache) snapshotLocked() []writeRange {
	out := make([]writeRange, len(c.ranges)) // 预分配
	copy(out, c.ranges) // 拷贝段表
	return out // 返回副本
}

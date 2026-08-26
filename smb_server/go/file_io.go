// file_io.go —— 文件数据与元信息操作:读 / 写 / 冲刷 / stat / 截断。
//
// 对应 CodeFileRead / CodeFileWrite / CodeFileFlush / CodeFileStat /
// CodeFileSetTimes / CodeFileTruncate 六个操作码:
//   - Read:GetRange 流式搬出(数据面流式,设计点 8),写回缓存优先命中;
//   - Write:进入写回缓存(S3 不支持随机写,见 write_cache.go);
//   - Flush:缓存整体上传对象存储 + 同步文件记录与桶用量;
//   - Stat:查文件/文件夹/桶记录组装 FileInfo(FILETIME 语义);
//   - SetTimes / Truncate:时间戳与长度操作(截断走缓存截断标记)。
package smbgateway

import (
	"context"   // 上下文
	"errors"    // errors.Is(对象不存在/416)
	"io"        // 流读取       // 服务日志(数据操作记录)
	"time"      // 句柄活动时间

	"gorm.io/gorm" // ORM 表达式(桶用量对称更新)

	"orbitcloud/core"   // 对象存储/全局 DB
	"orbitcloud/model"  // 数据模型
	"orbitcloud/server" // server 层错误
	"orbitcloud/utils"  // 桶名编码
)

// Read 按偏移读(CodeFileRead;数据面流式,设计点 8)。
// 参数:ctx 上下文;handleID 远程句柄 ID;offset 起始偏移;length 期望长度。
// 返回值:实际读到的字节(≤ length;0 = EOF);错误按哨兵语义。
func (f *FileOpsService) Read(ctx context.Context, handleID uint64, offset uint64, length uint32) ([]byte, error) {
	h := f.handles.Get(handleID) // 按句柄 ID 定位句柄
	if h == nil { // 句柄不存在?
		return nil, server.ErrNotFound // 报未找到
	}
	return f.readInner(ctx, h, offset, length) // 内部读取
}

// readInner 按偏移读(内部;句柄已定位)。
// 读底层函数说明:间隙区从对象读,覆盖区用缓存;对象缺失/区间越界
// 一律返回空(EOF 语义),绝不零填充——垃圾字节会损坏图片/压缩包等
// 对数据完整性敏感的文件。
//
// 截断语义:读取区间先按句柄逻辑 EOF(h.size,截断/写扩展的权威值)裁剪,
// 超界返回 EOF——截断后、Flush 前读路径绝不返回对象中的旧残留字节。
func (f *FileOpsService) readInner(ctx context.Context, h *remoteHandle, offset uint64, length uint32) ([]byte, error) {
	if h.isDir { // 目录句柄?
		return nil, errIsDirectory // 目录不可读
	}
	h.lastActive = time.Now() // 刷新活动时间

	bucket := utils.BucketEncoder(h.bucketID) // 对象桶名
	start := int64(offset) // 起点

	// 有效读取长度:按句柄逻辑 EOF 裁剪(截断后 Flush 前不读旧字节)
	h.mu.Lock()
	effSize := h.size
	h.mu.Unlock()
	if effSize <= start { // 起点已越出有效大小(含空文件/全截断)→ EOF
		return nil, nil
	}
	end := min64(start+int64(length), effSize) // 有效读取终点(闭区间语义下为开区间终点)

	// readFromObject 从对象读 [from, to) 区间(开区间)。
	readFromObject := func(from, to int64) ([]byte, error) { // 闭包:区间读
		if to <= from { // 空区间?
			return nil, nil // 返回空
		}
		rc, err := f.st.GetRange(ctx, bucket, h.objectKey, from, to-1) // 范围读(闭区间)
		if err != nil { // 读失败?
			// 对象不存在(空文件未写对象)→ 返回空(EOF 语义)。
			if errors.Is(err, core.ErrObjectNotFound) { // 对象不存在?
				return nil, nil // 空(EOF)
			}
			// 区间超出对象实际大小(S3 416):返回空(EOF 语义,非错误)。
			if isRangeNotSatisfiable(err) { // 416?
				return nil, nil // 空(EOF)
			}
			return nil, err // 其它错误上抛
		}
		defer rc.Close() // 关闭读流
		data, err := io.ReadAll(io.LimitReader(rc, to-from)) // 限长读全部
		if err != nil { // 读失败?
			// 读取阶段服务端 416:区间超出对象 → 空(EOF 语义,非错误)。
			if isRangeNotSatisfiable(err) { // 416?
				return nil, nil // 空(EOF)
			}
			return nil, err // 其它错误上抛
		}
		// 对象比请求区间短:返回实际字节数(短读 = EOF 语义)。
		return data, nil // 返回实际数据
	}

	// ---- 分段拼装:写回缓存段优先命中,间隙走对象读 ----
	var out []byte // 输出缓冲
	cursor := start // 当前已拼装到的游标

	// 写回缓存段视图(优先命中)。
	var segments []writeRange // 缓存段快照
	if h.writeCache != nil { // 有写回缓存?
		h.mu.Lock() // 锁句柄
		segments = h.writeCache.Ranges() // 取缓存段列表
		h.mu.Unlock() // 解锁
	}
	for _, seg := range segments { // 遍历缓存段(按偏移有序)
		if seg.end <= cursor { // 段整体在游标前?
			continue // 跳过
		}
		if seg.off >= end { // 段起点已越过读取终点?
			break // 后续段更远,结束
		}
		if seg.off > cursor { // 段前有间隙?
			// 间隙:对象读。
			data, err := readFromObject(cursor, min64(seg.off, end)) // 读间隙区间
			if err != nil {
				return nil, err // 失败上抛
			}
			out = append(out, data...) // 拼装
			cursor = min64(seg.off, end) // 游标前移
		}
		// 缓存段重叠部分。
		from := max64(cursor, seg.off) // 重叠起点
		to := min64(seg.end, end) // 重叠终点
		if to > from { // 有重叠?
			rel := seg.data[from-seg.off : to-seg.off] // 取段内对应切片
			out = append(out, rel...) // 拼装缓存数据
			cursor = to // 游标前移
		}
	}
	if cursor < end { // 尾部仍有未覆盖区间?
		data, err := readFromObject(cursor, end) // 对象读收尾
		if err != nil {
			return nil, err // 失败上抛
		}
		out = append(out, data...) // 拼装
	}
	if int64(len(out)) > int64(length) { // 超长保护(理论不会)?
		out = out[:length] // 裁剪到请求长度
	}
	smbDebugf("smb_gateway: 读取 句柄=%d 偏移=%d 期望=%d 实得=%d", h.handleID, offset, length, len(out)) // 记录读请求(句柄/偏移/期望/实得)
	return out, nil // 返回拼装结果
}

// Write 按偏移写(CodeFileWrite;S3 随机写约束见 write_cache.go;数据面流式)。
// 参数:ctx 上下文;handleID 远程句柄 ID;offset 写入偏移;data 待写数据。
// 返回值:实际写入字节数(通常 = len(data));错误按哨兵语义。
// 实现:进入写回缓存段表;缓存总量超阈值 → 提前整体冲刷(防内存无限增长)。
func (f *FileOpsService) Write(ctx context.Context, handleID uint64, offset uint64, data []byte) (uint32, error) {
	h := f.handles.Get(handleID) // 定位句柄
	if h == nil { // 不存在?
		return 0, server.ErrNotFound // 报未找到
	}
	if h.isDir { // 目录?
		return 0, errIsDirectory // 目录不可写
	}
	if h.share.Mode == "readonly" { // 只读共享?
		return 0, server.ErrForbidden // 拒绝
	}
	if h.writeCache == nil { // 无缓存(只读打开的句柄被写)?
		h.writeCache = NewWriteBackCache() // 补建缓存
	}
	h.lastActive = time.Now() // 刷新活动时间

	h.mu.Lock() // 锁句柄(写合并原子性)
	defer h.mu.Unlock() // 解锁
	total := h.writeCache.ApplyWrite(int64(offset), data) // 写入缓存段表(重叠合并)
	if off := int64(offset) + int64(len(data)); off > h.size { // 写尾超出已知大小?
		h.size = off // 扩展已知大小
	}
	if total >= h.writeCache.maxSize { // 缓存总量达阈值?
		// 缓存超阈值 → 提前整体冲刷(防内存无限增长)。
		if err := h.writeCache.FlushToStorage(ctx, f.st, utils.BucketEncoder(h.bucketID), h.objectKey, h.flushSpec()); err != nil { // 整体上传
			return 0, err // 上传失败上抛
		}
		h.truncateAt = -1 // 截断已随冲刷固化
	}
	smbDebugf("smb_gateway: 写入 句柄=%d 偏移=%d 长度=%d(缓存=%d 字节)", h.handleID, offset, len(data), total) // 记录写请求(句柄/偏移/长度/缓存总量)
	return uint32(len(data)), nil // 实际写入 = 入缓存长度
}

// Flush 冲刷写回缓存(SMB FLUSH;CodeFileFlush)。
// 参数:ctx 上下文;handleID 远程句柄 ID。
// 返回值:错误按哨兵语义。
// 实现:取冲刷快照(逻辑 EOF + 截断点 + 写段)→ 整体上传 → 同步文件记录
// 大小 → 对称更新桶用量(累加/扣减差值);冲刷成功后截断点复位(-1)。
func (f *FileOpsService) Flush(ctx context.Context, handleID uint64) error {
	h := f.handles.Get(handleID) // 定位句柄
	if h == nil { // 不存在?
		return server.ErrNotFound // 报未找到
	}
	if h.isDir { // 目录?
		return nil // 目录无缓存,幂等成功
	}
	h.lastActive = time.Now() // 刷新活动时间
	if h.writeCache == nil { // 无缓存(未写过也未截断)?
		return nil // 无需冲刷
	}
	h.mu.Lock() // 锁句柄
	defer h.mu.Unlock() // 解锁
	spec := h.flushSpec() // 冲刷快照(逻辑 EOF/截断点/写段,一次性取出)
	if !spec.dirty { // 无变化?
		return nil // 无需冲刷
	}
	if err := h.writeCache.FlushToStorage(ctx, f.st, utils.BucketEncoder(h.bucketID), h.objectKey, spec); err != nil { // 整体上传对象
		smbErrorf("smb_gateway: 冲刷句柄=%d 失败:%v", h.handleID, err) // 记录冲刷失败
		return err // 上传失败上抛
	}
	h.truncateAt = -1 // 截断已固化到对象,复位
	smbDebugf("smb_gateway: 冲刷句柄=%d 已上传 大小=%d", h.handleID, h.size) // 记录冲刷成功(句柄/大小)
	smbInfof("smb_gateway: 文件变更 写回 user=%d(%s) 文件=%d", h.userID, h.session.Username, h.fileID) // 每次文件变更仅记录一次(Info 级:操作者+被操作文件)
	return f.syncFileMeta(ctx, h) // 同步元数据(大小 + 桶用量)
}

// syncFileMeta 冲刷后同步元数据:文件记录大小与桶用量保持一致。
// 说明:对象已与 h.size 一致;对比记录旧值,差值对称累加/扣减桶用量。
func (f *FileOpsService) syncFileMeta(ctx context.Context, h *remoteHandle) error {
	var old model.File // 旧记录
	if err := core.DB.WithContext(ctx).First(&old, h.fileID).Error; err == nil { // 读旧记录成功?
		delta := h.size - old.FileSize // 大小差值
		if delta != 0 { // 有差值?
			_ = core.DB.WithContext(ctx).Model(&model.Bucket{}). // 对称更新桶用量
				Where("id = ?", h.bucketID).
				UpdateColumn("used_space", gorm.Expr("used_space + ?", delta)).Error // used_space 累加差值(正负通用)
		}
	}
	return core.DB.WithContext(ctx).Model(&model.File{}).
		Where("id = ?", h.fileID).Update("file_size", h.size).Error // 文件记录大小更新为句柄大小
}

// Stat 查询元信息(CodeFileStat)。
// 参数:ctx 上下文;handleID 远程句柄 ID。
// 返回值:FileInfo(字段与 Rust 侧一一对应);错误按哨兵语义。
// 分支:桶根(虚拟根)→ 桶级元信息;目录 → 文件夹记录;文件 → 文件记录
// (大小取 max(记录, 写回缓存已知值))。
func (f *FileOpsService) Stat(ctx context.Context, handleID uint64) (*FileInfo, error) {
	h := f.handles.Get(handleID) // 定位句柄
	if h == nil { // 不存在?
		return nil, server.ErrNotFound // 报未找到
	}
	h.lastActive = time.Now() // 刷新活动时间

	if h.isDir { // 目录句柄
		if h.folderID == 0 { // 桶根(虚拟根,无记录行)?
			// 桶根(虚拟根,无记录行):返回桶级元信息。
			var bucket model.Bucket // 桶记录
			if err := core.DB.WithContext(ctx).First(&bucket, h.bucketID).Error; err != nil { // 查桶
				return nil, err // 失败上抛
			}
			return &FileInfo{ // 组装桶级 FileInfo
				Name:           bucket.Name, // 桶名
				EndOfFile:      0, // 目录无大小
				AllocationSize: 0, // 无分配
				CreationTime:   timeToFiletime(bucket.CreatedAt), // 创建时间
				LastAccessTime: timeToFiletime(bucket.UpdatedAt), // 最后访问(退化 = 更新时间)
				LastWriteTime:  timeToFiletime(bucket.UpdatedAt), // 最后写入
				ChangeTime:     timeToFiletime(bucket.UpdatedAt), // 变更时间
				IsDirectory:    true, // 是目录
				FileIndex:      uint64(bucket.ID), // 索引 = 桶 ID
			}, nil
		}
		var folder model.Folder // 文件夹记录
		if err := core.DB.WithContext(ctx).First(&folder, h.folderID).Error; err != nil { // 查文件夹
			return nil, err // 失败上抛
		}
		return &FileInfo{ // 组装目录 FileInfo
			Name:           folder.Name, // 目录名
			EndOfFile:      0, // 无大小
			AllocationSize: 0, // 无分配
			CreationTime:   timeToFiletime(folder.CreatedAt), // 创建时间
			LastAccessTime: timeToFiletime(folder.UpdatedAt), // 最后访问
			LastWriteTime:  timeToFiletime(folder.UpdatedAt), // 最后写入
			ChangeTime:     timeToFiletime(folder.UpdatedAt), // 变更时间
			IsDirectory:    true, // 是目录
			FileIndex:      uint64(folder.ID), // 索引 = 文件夹 ID
		}, nil
	}

	var file model.File // 文件记录
	if err := core.DB.WithContext(ctx).First(&file, h.fileID).Error; err != nil { // 查文件
		return nil, err // 失败上抛
	}
	size := file.FileSize // 以记录大小为基础
	if h.writeCache != nil { // 有写回缓存(未冲刷的数据)?
		h.mu.Lock() // 锁句柄
		if h.size > size { // 缓存已知大小更大(有未冲刷写)?
			size = h.size // 取较大值(客户端可见最新大小)
		}
		h.mu.Unlock() // 解锁
	}
	us := uint64(size) // 转无符号
	smbDebugf("smb_gateway: 查询信息 句柄=%d → 名称=%q 大小=%d 目录=%v", h.handleID, file.Name, us, false) // 记录元信息结果
	return &FileInfo{ // 组装文件 FileInfo
		Name:           file.Name, // 文件名
		EndOfFile:      us, // 文件大小
		AllocationSize: us, // 分配大小(简化 = 大小)
		CreationTime:   timeToFiletime(file.CreatedAt), // 创建时间
		LastAccessTime: timeToFiletime(file.UpdatedAt), // 最后访问
		LastWriteTime:  timeToFiletime(file.UpdatedAt), // 最后写入
		ChangeTime:     timeToFiletime(file.UpdatedAt), // 变更时间
		IsDirectory:    false, // 是文件
		FileIndex:      uint64(file.ID), // 索引 = 文件 ID
	}, nil
}

// SetTimes 设置时间戳(CodeFileSetTimes;nil 字段 = 不改)。
// 参数:ctx 上下文;args 设置请求。
// 返回值:错误按哨兵语义。
// 说明:gorm.Model 的时间戳列由 GORM 维护,当前仅接受写入时间
// (UpdatedAt 语义),其余字段(SMB 时间戳)由后端记录自动维护,不额外覆盖。
func (f *FileOpsService) SetTimes(ctx context.Context, args *SetTimesArgs) error {
	h := f.handles.Get(args.HandleID) // 定位句柄
	if h == nil { // 不存在?
		return server.ErrNotFound // 报未找到
	}
	h.lastActive = time.Now() // 刷新活动时间
	return nil // 当前实现:时间戳由后端记录自动维护,无额外操作
}

// Truncate 截断/扩展到指定长度(CodeFileTruncate)。
// 参数:ctx 上下文;handleID 远程句柄 ID;length 目标长度。
// 返回值:错误按哨兵语义。
// 实现:更新句柄逻辑 EOF(h.size),并记录截断点(h.truncateAt = 对象旧数据
// 可信上界,多次截断取最小——只作废不恢复);冲刷拼装时按截断点裁剪/零填充。
func (f *FileOpsService) Truncate(ctx context.Context, handleID uint64, length uint64) error {
	h := f.handles.Get(handleID) // 定位句柄
	if h == nil { // 不存在?
		return server.ErrNotFound // 报未找到
	}
	if h.isDir { // 目录?
		return errIsDirectory // 目录不可截断
	}
	h.lastActive = time.Now() // 刷新活动时间

	h.mu.Lock() // 锁句柄
	defer h.mu.Unlock() // 解锁
	h.size = int64(length) // 句柄已知大小 = 目标长度(逻辑 EOF 权威)
	// 截断点:对象旧数据作废起点(多次截断取最小,只作废不恢复;
	// -1 = 无截断,对象全量可读)
	if h.truncateAt < 0 || int64(length) < h.truncateAt {
		h.truncateAt = int64(length)
	}
	if h.writeCache == nil { // 无缓存?
		h.writeCache = NewWriteBackCache() // 补建(截断冲刷需要)
	}
	smbDebugf("smb_gateway: 截断 句柄=%d 长度=%d", h.handleID, length) // 记录截断(句柄/目标长度)
	return nil
}

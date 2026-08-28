// file_lookup.go —— 路径解析、记录查询与通用工具。
//
// 承载文件操作服务共用的内部设施:
//   - resolveDir:路径 → 父目录 ID(逐段沿 parent 链;写 intent 自动建父链);
//   - lookupFolder / lookupFile:按 (bucket, parent, name) 查记录;
//   - createEmptyFile:新建 0 字节文件记录(对象在首次 Flush 时写入);
//   - SMB 路径工具(clean/split/last/join)与 FILETIME 换算;
//   - resolveSharePath:整条路径的 (folderID, fileID, isDir) 解析。
package smbgateway

import (
	"context" // 上下文
	"errors"  // errors.Is(gorm.ErrRecordNotFound)
	"strings" // 字符串处理(拆分/小写)
	"time"    // 时间(FILETIME 换算)

	"gorm.io/gorm" // ORM(记录查询/表达式)

	"orbitcloud/core"   // 全局 DB
	"orbitcloud/model"  // 数据模型
	"orbitcloud/server" // server 层服务(CreateDir)
)

// resolveSharePath 把 SMB 相对路径解析为 (folderID, fileID, 存在性)。
// 参数:share 共享上下文(桶 ID);smbPath 相对路径("/" 分隔组件)。
// 返回值:folderID 末级目录 ID(0 = 桶根);fileID 文件 ID(不存在 = 0);
// isDir 目标是否为目录;err 解析错误。
func (f *FileOpsService) resolveSharePath(share ShareInfo, smbPath string) (folderID, fileID uint, isDir bool, err error) {
	p := cleanSmbPath(smbPath) // 归一化路径
	segs := splitSmbPath(p)    // 拆路径段
	if len(segs) == 0 {        // 空路径 = 桶根?
		return 0, 0, false, nil // 桶根
	}
	ctx := context.Background()                                                                        // 独立后台上下文(内部工具)
	folderID, err = f.resolveDir(ctx, share.BucketID, strings.Join(segs[:len(segs)-1], "/"), false, 0) // 解析末级父目录(不建)
	if err != nil {
		return 0, 0, false, err // 父链失败上抛
	}
	name := segs[len(segs)-1]                                                                       // 末段 = 目标名
	if _, ferr := f.lookupFolder(ctx, share.BucketID, folderID, name, "isable <> 0"); ferr == nil { // 是文件夹?
		return folderID, 0, true, nil // 目录命中
	}
	fileID, _, exists, err := f.lookupFile(ctx, share.BucketID, folderID, name) // 查文件
	if err != nil || !exists {                                                  // 查错或不存在?
		return folderID, 0, false, nil // 视为不存在
	}
	return folderID, fileID, false, nil // 文件命中
}

// resolveDir 解析目录路径(逐段沿 parent 链;mkDir = 写 intent 时自动建父链)。
// 注意:路径末段是文件名,只解析到倒数第二段;单段路径 → 桶根(0)。
// userID 为 mkDir 建链时的操作者(真实身份;0 = 不建链场景可忽略)。
func (f *FileOpsService) resolveDir(ctx context.Context, bucketID uint, path string, mkDir bool, userID uint) (uint, error) {
	segs := splitSmbPath(path)         // 拆路径段
	folderID := uint(0)                // 起始 = 桶根(0)
	for i := 0; i < len(segs)-1; i++ { // 逐段向下(不含末段)
		seg := segs[i]                                                          // 当前段名
		fid, err := f.lookupFolder(ctx, bucketID, folderID, seg, "isable <> 0") // 查子文件夹
		if err != nil {                                                         // 未查到
			if mkDir && errors.Is(err, gorm.ErrRecordNotFound) { // 写 intent 且确实不存在?
				// 写 intent:自动补建父目录链(以真实操作者身份创建,
				// 修复 UserID=0 导致两级以上建链失败的缺陷)。
				dir, cerr := server.CreateDir(ctx, server.CreateDirArg{ // 建缺失目录(幂等)
					UserID:   userID,                              // 真实操作者
					BucketID: bucketID,                            // 桶
					DirPath:  "/" + strings.Join(segs[:i+1], "/"), // 已走过的前缀路径
				})
				if cerr != nil {
					return 0, cerr // 建失败上抛
				}
				fid = dir.ID // 用新建目录 ID 继续
			} else { // 非写 intent 或其它错误
				return 0, err // 直接上抛(父链缺失 = 目标不存在)
			}
		}
		folderID = fid // 下移一层
	}
	return folderID, nil // 返回末级父目录 ID
}

// lookupFolder 按 (bucket, parent, name) 查文件夹;cond 附加条件(如 isable)。
// 未找到返回 gorm.ErrRecordNotFound(调用方按语义处理)。
func (f *FileOpsService) lookupFolder(ctx context.Context, bucketID, parentID uint, name string, cond string) (uint, error) {
	q := core.DB.WithContext(ctx).Model(&model.Folder{}).
		Where("bucket_id = ? AND parent_id = ? AND name_lower = ?", bucketID, parentID, strings.ToLower(name)) // 桶+父+小写名精确匹配
	if cond != "" { // 有附加条件?
		q = q.Where(cond) // 追加(如启用过滤)
	}
	var folder model.Folder                        // 查询目标行
	if err := q.First(&folder).Error; err != nil { // 取首行
		return 0, err // 未找到/数据库错误
	}
	return folder.ID, nil // 返回文件夹主键
}

// lookupFile 按 (bucket, folder, name) 查文件;返回 (id, size, exists, err)。
// 约定:未找到时 exists=false、err=nil(区别于数据库错误)。
func (f *FileOpsService) lookupFile(ctx context.Context, bucketID, folderID uint, name string) (uint, int64, bool, error) {
	var file model.File // 查询目标行
	err := core.DB.WithContext(ctx).
		Where("bucket_id = ? AND folder_id = ? AND name_lower = ?", bucketID, folderID, strings.ToLower(name)). // 桶+父+小写名
		First(&file).Error                                                                                      // 取首行
	if errors.Is(err, gorm.ErrRecordNotFound) { // 记录不存在?
		return 0, 0, false, nil // exists=false,err=nil(不是错误)
	}
	if err != nil { // 其它数据库错误
		return 0, 0, false, err // 上抛
	}
	return file.ID, file.FileSize, true, nil // 返回 (ID, 大小, 存在)
}

// loadFileRecord 按 (bucket, folder, name) 加载完整文件记录(含 VisibleToGroups,
// 条目级可见性判定用);未找到 → (nil, gorm.ErrRecordNotFound)。
func (f *FileOpsService) loadFileRecord(ctx context.Context, bucketID, folderID uint, name string) (*model.File, error) {
	var file model.File
	err := core.DB.WithContext(ctx).
		Where("bucket_id = ? AND folder_id = ? AND name_lower = ?", bucketID, folderID, strings.ToLower(name)).
		First(&file).Error
	if err != nil {
		return nil, err
	}
	return &file, nil
}

// createEmptyFile 新建 0 字节文件记录(对象在首次 Flush 时写入)。
// 返回值:新建文件记录 ID;err 数据库错误。
func (f *FileOpsService) createEmptyFile(ctx context.Context, userID, bucketID, folderID uint, name string) (uint, error) {
	file := model.File{ // 组装空文件记录
		BucketID:   bucketID,                   // 桶
		FolderID:   folderID,                   // 父目录
		Name:       name,                       // 文件名
		FileSize:   0,                          // 0 字节
		FileType:   "application/octet-stream", // 通用类型(对象存储无 MIME 约束)
		UploadedBy: userID,                     // 创建者
	}
	if err := core.DB.WithContext(ctx).Create(&file).Error; err != nil { // 插入记录
		return 0, err // 失败上抛
	}
	return file.ID, nil // 返回自增主键(即对象 key)
}

// ============================================================================
// SMB 路径工具
// ============================================================================

// cleanSmbPath 归一化 SMB 相对路径(去首尾 "/"、去 "." 段、"\" 转 "/")。
func cleanSmbPath(p string) string {
	return strings.Join(splitSmbPath(strings.ReplaceAll(p, "\\", "/")), "/") // 反斜杠转正斜杠后拆分再拼接
}

// splitSmbPath 拆分路径段(过滤空段与 ".";去空格)。
func splitSmbPath(p string) []string {
	raw := strings.Split(p, "/")       // 按 "/" 拆分
	out := make([]string, 0, len(raw)) // 预分配输出
	for _, s := range raw {            // 逐段过滤
		s = strings.TrimSpace(s) // 去首尾空格
		if s == "" || s == "." { // 空段或当前目录?
			continue // 丢弃
		}
		out = append(out, s) // 保留有效段
	}
	return out // 返回段列表
}

// lastSegment 取路径末段(空路径 → "")。
func lastSegment(p string) string {
	segs := splitSmbPath(p) // 拆段
	if len(segs) == 0 {     // 空路径?
		return "" // 返回空
	}
	return segs[len(segs)-1] // 返回末段
}

// joinSmbPath 段列表 → "/" 路径。
func joinSmbPath(segs []string) string { return strings.Join(segs, "/") } // 拼接

// ============================================================================
// FILETIME 工具(1601-01-01 起 100ns 刻度,与 ixr-smb-server 对齐)
// ============================================================================

// filetimeEpochOffset 1601-01-01 与 1970-01-01 的秒差。
const filetimeEpochOffset = 11644473600 // 两者 Unix 时间差(秒)

// timeToFiletime UTC 时间 → FILETIME(100ns 刻度)。
func timeToFiletime(t time.Time) uint64 {
	sec := t.Unix() + filetimeEpochOffset                      // 秒部分平移 1601 纪元
	return uint64(sec)*10_000_000 + uint64(t.Nanosecond())/100 // 秒→100ns + 纳秒余量→100ns
}

// filetimeToTime FILETIME → UTC 时间。
func filetimeToTime(ft uint64) time.Time {
	sec := int64(ft / 10_000_000)                       // 取 100ns 数中的秒
	ns := int64(ft%10_000_000) * 100                    // 余数换算纳秒
	return time.Unix(sec-filetimeEpochOffset, ns).UTC() // 平移回 1970 纪元并转 UTC
}

// ============================================================================
// 数值工具
// ============================================================================

// min64 两 int64 取小。
func min64(a, b int64) int64 {
	if a < b { // a 更小?
		return a // 取 a
	}
	return b // 取 b
}

// max64 两 int64 取大。
func max64(a, b int64) int64 {
	if a > b { // a 更大?
		return a // 取 a
	}
	return b // 取 b
}

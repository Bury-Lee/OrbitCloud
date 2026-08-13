// file_list.go —— 桶内条目列表:双表 UNION ALL 合并分页(文件 + 文件夹混排)。
//
// 桶级权限由 api 层预检;目标目录经 ResolveDirPathStrict 内部解析,
// 其祖先链 ACL 与条目级可见性过滤(checkAncestorsAccessTree / visibilitySQL)
// 属查询塑形,保留在本层。
package server

import (
	"context"
	"fmt"
	"time"

	"orbitcloud/common"
	"orbitcloud/core"
	"orbitcloud/log"
	"orbitcloud/model"
)

// unionRow 双表合并分页的投影行(SQL 层统一 id/created_at/kind)。
type unionRow struct {
	ID        uint
	CreatedAt time.Time
	Kind      string
}

// listBase 列表前置公共步骤(ListFiles / ListFilesCursor 共用):
// 桶可用 → 目录规范化 → 严格解析(只读不建链)→ 祖先链可见性 → 条目级可见性谓词。
// 返回目标目录 folderID 与可见性过滤 SQL 及其参数。
func listBase(ctx context.Context, userID, bucketID uint, dirPath string) (uint, string, []any, error) {
	// 桶对象状态(存在 + Status==1)
	if _, err := CheckBucketUsable(ctx, CheckBucketUsableArg{BucketID: bucketID}); err != nil {
		return 0, "", nil, err
	}

	// 目录规范化(空串/"/" → "/")
	dir, err := common.NormalizeDirPath(dirPath)
	if err != nil {
		return 0, "", nil, err
	}

	// 严格解析(只读不建链):目录不存在 → 404,禁止幽灵重建
	folderID, err := common.ResolveDirPathStrict(ctx, bucketID, dir)
	if err != nil {
		return 0, "", nil, err
	}

	// 祖先链条目级可见性 + Isable
	if err := checkAncestorsAccessTree(ctx, userID, bucketID, folderID); err != nil {
		return 0, "", nil, err
	}

	// 条目级可见性过滤:管理员不过滤,其余用户注入"空限制 OR 创建者 OR 可见组内成员"谓词
	visSQL, visArgs, err := listVisibilityPredicate(ctx, userID)
	if err != nil {
		return 0, "", nil, err
	}
	return folderID, visSQL, visArgs, nil
}

// ListFilesArg 目录列表(offset 分页)入参。
type ListFilesArg struct {
	UserID   uint   // 操作者(可见性过滤依据)
	BucketID uint   // 所属桶
	DirPath  string // 目标目录路径(空串/"/" = 桶根)
	Page     int    // 页码(≥1)
	PageSize int    // 页大小(缺省 50,上限 500)
}

// ListFiles 分页列出目录下的条目:文件/文件夹分两个切片返回,
// created_at 倒序混排(UNION ALL),跨两类共取一页(offset 分页,供页面浏览;
// 前端递归下载用 ListFilesCursor)。
func ListFiles(ctx context.Context, arg ListFilesArg) (files []model.File, folders []model.Folder, total int64, err error) {
	userID, bucketID, dirPath, page, pageSize := arg.UserID, arg.BucketID, arg.DirPath, arg.Page, arg.PageSize
	// 公共前置(桶/目录/Isable/可见性)
	folderID, visSQL, visArgs, err := listBase(ctx, userID, bucketID, dirPath)
	if err != nil {
		return nil, nil, 0, err
	}

	// 分页归一(page>=1,pageSize 缺省 50)
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 500 {
		pageSize = 500 // 防御上限
	}

	// 双表合并分页(UNION ALL,created_at DESC);可见性谓词分别注入两分支
	visCond := ""
	if visSQL != "" {
		visCond = " AND " + visSQL
	}
	unionSQL := `SELECT id, created_at, kind FROM (
		SELECT id, created_at, 'file' AS kind FROM files   WHERE bucket_id = ? AND folder_id = ?` + visCond + `
		UNION ALL
		SELECT id, created_at, 'folder' AS kind FROM folders WHERE bucket_id = ? AND parent_id = ?` + visCond + `
	) t ORDER BY created_at DESC LIMIT ? OFFSET ?`
	queryArgs := []any{bucketID, folderID}
	queryArgs = append(queryArgs, visArgs...)
	queryArgs = append(queryArgs, bucketID, folderID)
	queryArgs = append(queryArgs, visArgs...)
	queryArgs = append(queryArgs, pageSize, (page-1)*pageSize)
	rows := []unionRow{}
	if err := core.DB.WithContext(ctx).Raw(unionSQL, queryArgs...).Scan(&rows).Error; err != nil {
		return nil, nil, 0, fmt.Errorf("list files: union query: %w", err)
	}

	// total = 两表计数之和(叠加可见性过滤)
	var n1, n2 int64
	countArgs := []any{bucketID, folderID}
	countArgs = append(countArgs, visArgs...)
	if err := core.DB.WithContext(ctx).
		Raw("SELECT COUNT(*) FROM files WHERE bucket_id = ? AND folder_id = ?"+visCond, countArgs...).
		Scan(&n1).Error; err != nil {
		return nil, nil, 0, fmt.Errorf("list files count (files): %w", err)
	}
	if err := core.DB.WithContext(ctx).
		Raw("SELECT COUNT(*) FROM folders WHERE bucket_id = ? AND parent_id = ?"+visCond, countArgs...).
		Scan(&n2).Error; err != nil {
		return nil, nil, 0, fmt.Errorf("list files count (folders): %w", err)
	}
	total = n1 + n2

	// 按 kind 批量加载(IN 一次取全,按投影顺序重排;并发删除的行自然跳过)
	files = make([]model.File, 0, len(rows))
	folders = make([]model.Folder, 0, len(rows))
	var fileIDs, folderIDs []uint
	for _, row := range rows {
		if row.Kind == ItemKindFile {
			fileIDs = append(fileIDs, row.ID)
		} else {
			folderIDs = append(folderIDs, row.ID)
		}
	}
	filesByID := map[uint]model.File{}
	if len(fileIDs) > 0 {
		var fs []model.File
		if err := core.DB.WithContext(ctx).Where("id IN ? AND bucket_id = ?", fileIDs, bucketID).Find(&fs).Error; err != nil {
			return nil, nil, 0, fmt.Errorf("list files: load files: %w", err)
		}
		for i := range fs {
			filesByID[fs[i].ID] = fs[i]
		}
	}
	foldersByID := map[uint]model.Folder{}
	if len(folderIDs) > 0 {
		var fds []model.Folder
		if err := core.DB.WithContext(ctx).Where("id IN ? AND bucket_id = ?", folderIDs, bucketID).Find(&fds).Error; err != nil {
			return nil, nil, 0, fmt.Errorf("list files: load folders: %w", err)
		}
		for i := range fds {
			foldersByID[fds[i].ID] = fds[i]
		}
	}
	for _, row := range rows {
		if row.Kind == ItemKindFile {
			if f, ok := filesByID[row.ID]; ok {
				files = append(files, f)
			}
		} else if f, ok := foldersByID[row.ID]; ok {
			folders = append(folders, f)
		}
	}
	return files, folders, total, nil
}

// cursorIDRow keyset 分页投影行(仅取主键;行级可见性由可见性谓词过滤)。
type cursorIDRow struct {
	ID uint
}

// ListFilesCursorArg 目录列表(游标分页)入参。
type ListFilesCursorArg struct {
	UserID       uint   // 操作者(可见性过滤依据)
	BucketID     uint   // 所属桶
	DirPath      string // 目标目录路径(空串/"/" = 桶根)
	FileCursor   string // 文件表 keyset 游标(空 = 首页)
	FolderCursor string // 文件夹表 keyset 游标(空 = 首页)
	PageSize     int    // 页大小(缺省 50,上限 500)
}

// ListFilesCursor 游标分页列出目录下的条目(前端递归下载专用):
// 文件/文件夹各自独立 keyset 分页(ORDER BY id DESC,游标 = 末条 ID),
// 每表取 pageSize+1 条判定 has_more;next 游标为空串 = 该表无更多。
func ListFilesCursor(ctx context.Context, arg ListFilesCursorArg) (files []model.File, folders []model.Folder, nextFileCursor, nextFolderCursor string, err error) {
	userID, bucketID, dirPath, fileCursor, folderCursor, pageSize := arg.UserID, arg.BucketID, arg.DirPath, arg.FileCursor, arg.FolderCursor, arg.PageSize
	// 公共前置(桶/目录/Isable/可见性)
	folderID, visSQL, visArgs, err := listBase(ctx, userID, bucketID, dirPath)
	if err != nil {
		return nil, nil, "", "", err
	}

	// pageSize 归一(游标模式不返回 total)
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 500 {
		pageSize = 500 // 防御上限
	}
	visCond := ""
	if visSQL != "" {
		visCond = " AND " + visSQL
	}

	// 双表各自 keyset 分页(各取 pageSize+1 条判定 has_more)
	fileCond, fileCondArgs := common.KeysetIDCond("", fileCursor)
	folderCond, folderCondArgs := common.KeysetIDCond("", folderCursor)

	// 文件表
	fileSQL := "SELECT id FROM files WHERE bucket_id = ? AND folder_id = ?" + visCond
	fileArgs := []any{bucketID, folderID}
	fileArgs = append(fileArgs, visArgs...)
	if fileCond != "" {
		fileSQL += " AND " + fileCond
		fileArgs = append(fileArgs, fileCondArgs...)
	}
	fileSQL += " ORDER BY id DESC LIMIT ?"
	fileArgs = append(fileArgs, pageSize+1)
	fileIDs := []cursorIDRow{}
	if err := core.DB.WithContext(ctx).Raw(fileSQL, fileArgs...).Scan(&fileIDs).Error; err != nil {
		return nil, nil, "", "", fmt.Errorf("list files cursor (files): %w", err)
	}

	// 文件夹表
	folderSQL := "SELECT id FROM folders WHERE bucket_id = ? AND parent_id = ?" + visCond
	folderArgs := []any{bucketID, folderID}
	folderArgs = append(folderArgs, visArgs...)
	if folderCond != "" {
		folderSQL += " AND " + folderCond
		folderArgs = append(folderArgs, folderCondArgs...)
	}
	folderSQL += " ORDER BY id DESC LIMIT ?"
	folderArgs = append(folderArgs, pageSize+1)
	folderIDs := []cursorIDRow{}
	if err := core.DB.WithContext(ctx).Raw(folderSQL, folderArgs...).Scan(&folderIDs).Error; err != nil {
		return nil, nil, "", "", fmt.Errorf("list files cursor (folders): %w", err)
	}

	// 组装返回(记录被并发删除则跳过;has_more 以原始 ID 行为准)
	files, nextFileCursor = loadCursorFiles(ctx, bucketID, fileIDs, pageSize)
	folders, nextFolderCursor = loadCursorFolders(ctx, bucketID, folderIDs, pageSize)
	log.Infof("list files cursor: user=%d bucket=%d dir=%q cursor=(%q,%q) → files=%d folders=%d next=(%q,%q)",
		userID, bucketID, dirPath, fileCursor, folderCursor,
		len(files), len(folders), nextFileCursor, nextFolderCursor)
	return files, folders, nextFileCursor, nextFolderCursor, nil
}

// loadCursorFiles 按投影 ID 行批量加载文件切片,并计算下一页游标。
func loadCursorFiles(ctx context.Context, bucketID uint, rows []cursorIDRow, pageSize int) ([]model.File, string) {
	hasMore := len(rows) > pageSize
	shown := rows
	if hasMore {
		shown = rows[:pageSize]
	}
	files := make([]model.File, 0, len(shown))
	if len(shown) > 0 {
		ids := make([]uint, 0, len(shown))
		for _, r := range shown {
			ids = append(ids, r.ID)
		}
		var fs []model.File
		if err := core.DB.WithContext(ctx).Where("id IN ? AND bucket_id = ?", ids, bucketID).Find(&fs).Error; err != nil {
			log.Errorf("list files cursor: load files: %v", err)
			return files, ""
		}
		byID := map[uint]model.File{}
		for i := range fs {
			byID[fs[i].ID] = fs[i]
		}
		for _, r := range shown {
			if f, ok := byID[r.ID]; ok {
				files = append(files, f)
			}
		}
	}
	next := ""
	if hasMore && len(shown) > 0 {
		next = common.EncodeKeysetID(shown[len(shown)-1].ID)
	}
	return files, next
}

// loadCursorFolders 按投影 ID 行批量加载文件夹切片,并计算下一页游标。
func loadCursorFolders(ctx context.Context, bucketID uint, rows []cursorIDRow, pageSize int) ([]model.Folder, string) {
	hasMore := len(rows) > pageSize
	shown := rows
	if hasMore {
		shown = rows[:pageSize]
	}
	folders := make([]model.Folder, 0, len(shown))
	if len(shown) > 0 {
		ids := make([]uint, 0, len(shown))
		for _, r := range shown {
			ids = append(ids, r.ID)
		}
		var fds []model.Folder
		if err := core.DB.WithContext(ctx).Where("id IN ? AND bucket_id = ?", ids, bucketID).Find(&fds).Error; err != nil {
			log.Errorf("list files cursor: load folders: %v", err)
			return folders, ""
		}
		byID := map[uint]model.Folder{}
		for i := range fds {
			byID[fds[i].ID] = fds[i]
		}
		for _, r := range shown {
			if f, ok := byID[r.ID]; ok {
				folders = append(folders, f)
			}
		}
	}
	next := ""
	if hasMore && len(shown) > 0 {
		next = common.EncodeKeysetID(shown[len(shown)-1].ID)
	}
	return folders, next
}

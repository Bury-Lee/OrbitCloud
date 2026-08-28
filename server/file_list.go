// file_list.go —— 桶内条目列表:双表 UNION ALL 合并分页(文件 + 文件夹混排)。
//
// 桶级权限由 api 层预检;目标目录经 ResolveDirPathStrict 内部解析,
// 其祖先链 ACL 与条目级可见性过滤(checkAncestorsAccessTree / visibilitySQL)
// 属查询塑形,保留在本层。
package server

import (
	"context"
	"fmt"
	"strings"
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
	Key      string // 名称前缀搜索(空 = 不过滤)
	Sort     string // 排序字段: name | time | size(空 = 默认 created_at 倒序)
	Order    string // 排序方向: asc | desc(空 = desc)
}

// listSortCols 排序字段白名单 → (files 列, folders 列);size 时文件夹无大小,填 0。
var listSortCols = map[string][2]string{
	"name": {"name_lower", "name_lower"},
	"time": {"updated_at", "updated_at"},
	"size": {"file_size", "0"},
}

// ListFiles 分页列出目录下的条目:文件/文件夹分两个切片返回,
// 默认 created_at 倒序混排(UNION ALL),支持名称前缀搜索与白名单排序,
// 跨两类共取一页(offset 分页,供页面浏览;前端递归下载用 ListFilesCursor)。
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

	// 名称前缀搜索(空 = 不追加条件)
	keyCond := ""
	keyArgs := []any{}
	if arg.Key != "" {
		keyCond = " AND name_lower LIKE ?"
		keyArgs = []any{strings.ToLower(arg.Key) + "%"}
	}

	// 可见性谓词(两分支共用)
	visCond := ""
	if visSQL != "" {
		visCond = " AND " + visSQL
	}

	// 排序:白名单字段 + 方向归一;默认 created_at DESC(与原行为一致)
	orderClause := " ORDER BY created_at DESC"
	if cols, ok := listSortCols[arg.Sort]; ok {
		dir := strings.ToUpper(strings.TrimSpace(arg.Order))
		if dir != "ASC" && dir != "DESC" {
			dir = "DESC"
		}
		orderClause = " ORDER BY sort_key " + dir + ", id DESC"
		// 双表合并分页(UNION ALL):投影 sort_key 供外层排序(Scan 忽略未映射列)
		unionSQL := `SELECT id, created_at, kind FROM (
			SELECT id, created_at, 'file' AS kind, ` + cols[0] + ` AS sort_key FROM files   WHERE bucket_id = ? AND folder_id = ? AND deleted_at IS NULL` + keyCond + visCond + `
			UNION ALL
			SELECT id, created_at, 'folder' AS kind, ` + cols[1] + ` AS sort_key FROM folders WHERE bucket_id = ? AND parent_id = ? AND isable = true AND deleted_at IS NULL` + keyCond + visCond + `
		) t` + orderClause + ` LIMIT ? OFFSET ?`
		queryArgs := []any{bucketID, folderID}
		queryArgs = append(queryArgs, keyArgs...)
		queryArgs = append(queryArgs, visArgs...)
		queryArgs = append(queryArgs, bucketID, folderID)
		queryArgs = append(queryArgs, keyArgs...)
		queryArgs = append(queryArgs, visArgs...)
		queryArgs = append(queryArgs, pageSize, (page-1)*pageSize)
		rows := []unionRow{}
		if err := core.DB.WithContext(ctx).Raw(unionSQL, queryArgs...).Scan(&rows).Error; err != nil {
			return nil, nil, 0, fmt.Errorf("list files: union query: %w", err)
		}
		// 按 kind 批量加载(IN 一次取全,按投影顺序重排;并发删除的行自然跳过)
		files, folders, err = loadListRows(ctx, bucketID, rows)
		if err != nil {
			return nil, nil, 0, err
		}
		// total = 两表计数之和(叠加搜索与可见性过滤)
		total, err = countListRows(ctx, bucketID, folderID, keyCond, keyArgs, visSQL, visArgs)
		if err != nil {
			return nil, nil, 0, err
		}
		return files, folders, total, nil
	}

	// 默认排序(created_at DESC):保留原 SQL 路径
	unionSQL := `SELECT id, created_at, kind FROM (
		SELECT id, created_at, 'file' AS kind FROM files   WHERE bucket_id = ? AND folder_id = ? AND deleted_at IS NULL` + keyCond + visCond + `
		UNION ALL
		SELECT id, created_at, 'folder' AS kind FROM folders WHERE bucket_id = ? AND parent_id = ? AND isable = true AND deleted_at IS NULL` + keyCond + visCond + `
	) t ORDER BY created_at DESC LIMIT ? OFFSET ?`
	queryArgs := []any{bucketID, folderID}
	queryArgs = append(queryArgs, keyArgs...)
	queryArgs = append(queryArgs, visArgs...)
	queryArgs = append(queryArgs, bucketID, folderID)
	queryArgs = append(queryArgs, keyArgs...)
	queryArgs = append(queryArgs, visArgs...)
	queryArgs = append(queryArgs, pageSize, (page-1)*pageSize)
	rows := []unionRow{}
	if err := core.DB.WithContext(ctx).Raw(unionSQL, queryArgs...).Scan(&rows).Error; err != nil {
		return nil, nil, 0, fmt.Errorf("list files: union query: %w", err)
	}

	// 按 kind 批量加载(IN 一次取全,按投影顺序重排;并发删除的行自然跳过)
	files, folders, err = loadListRows(ctx, bucketID, rows)
	if err != nil {
		return nil, nil, 0, err
	}
	total, err = countListRows(ctx, bucketID, folderID, keyCond, keyArgs, visSQL, visArgs)
	if err != nil {
		return nil, nil, 0, err
	}
	return files, folders, total, nil
}

// loadListRows 按投影顺序批量加载文件/文件夹行(公共步骤)。
func loadListRows(ctx context.Context, bucketID uint, rows []unionRow) ([]model.File, []model.Folder, error) {
	files := make([]model.File, 0, len(rows))
	folders := make([]model.Folder, 0, len(rows))
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
			return nil, nil, fmt.Errorf("list files: load files: %w", err)
		}
		for i := range fs {
			filesByID[fs[i].ID] = fs[i]
		}
	}
	foldersByID := map[uint]model.Folder{}
	if len(folderIDs) > 0 {
		var fds []model.Folder
		if err := core.DB.WithContext(ctx).Where("id IN ? AND bucket_id = ?", folderIDs, bucketID).Find(&fds).Error; err != nil {
			return nil, nil, fmt.Errorf("list files: load folders: %w", err)
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
	return files, folders, nil
}

// countListRows 双表计数之和(叠加搜索与可见性过滤;公共步骤)。
func countListRows(ctx context.Context, bucketID, folderID uint, keyCond string, keyArgs []any, visSQL string, visArgs []any) (int64, error) {
	visCond := ""
	if visSQL != "" {
		visCond = " AND " + visSQL
	}
	countArgs := []any{bucketID, folderID}
	countArgs = append(countArgs, keyArgs...)
	countArgs = append(countArgs, visArgs...)
	var n1, n2 int64
	if err := core.DB.WithContext(ctx).
		Raw("SELECT COUNT(*) FROM files WHERE bucket_id = ? AND folder_id = ? AND deleted_at IS NULL"+keyCond+visCond, countArgs...).
		Scan(&n1).Error; err != nil {
		return 0, fmt.Errorf("list files count (files): %w", err)
	}
	if err := core.DB.WithContext(ctx).
		Raw("SELECT COUNT(*) FROM folders WHERE bucket_id = ? AND parent_id = ? AND isable = true AND deleted_at IS NULL"+keyCond+visCond, countArgs...).
		Scan(&n2).Error; err != nil {
		return 0, fmt.Errorf("list files count (folders): %w", err)
	}
	return n1 + n2, nil
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
	fileSQL := "SELECT id FROM files WHERE bucket_id = ? AND folder_id = ? AND deleted_at IS NULL" + visCond
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
	folderSQL := "SELECT id FROM folders WHERE bucket_id = ? AND parent_id = ? AND isable = true AND deleted_at IS NULL" + visCond
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

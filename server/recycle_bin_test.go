// recycle_bin_test.go —— 回收站过期条目物理清除测试:
//   - 过期文件(软删超过 cutoff)被物理清除(记录/对象/用量/分享);
//   - 未过期软删条目保留;
//   - 过期目录整棵子树清除(含未逐条标记的子孙、目录删除前已单独软删的文件行);
//   - 未过期目录保留。
package server

import (
	"context"
	"strconv"
	"testing"
	"time"

	"orbitcloud/core"
	"orbitcloud/model"
	"orbitcloud/utils"
)

// mkShareP0 建分享行(测试用)。
func mkShareP0(t *testing.T, itemID uint, itemType string) *model.ShareLink {
	t.Helper()
	s := &model.ShareLink{BucketItemID: itemID, ItemType: itemType, CreatorID: 1, Token: "tk-" + strconv.Itoa(int(itemID))}
	if err := core.DB.Create(s).Error; err != nil {
		t.Fatalf("create share: %v", err)
	}
	return s
}

// backdateSoftDelete 把软删行的时间改到过去(模拟"删除于 N 天前")。
func backdateSoftDelete(t *testing.T, table any, id uint, past time.Time) {
	t.Helper()
	if err := core.DB.Unscoped().Model(table).Where("id = ?", id).Update("deleted_at", past).Error; err != nil {
		t.Fatalf("backdate deleted_at: %v", err)
	}
}

// objExists 对象是否存在(与 putMemObject 同口径)。
func objExists(bucketID, fileID uint) bool {
	_, ok := core.Storage.(*memStorage).objects[utils.BucketEncoder(bucketID)+"/"+strconv.FormatUint(uint64(fileID), 10)]
	return ok
}

// countRows 行数(测试用)。
func countRows(t *testing.T, table any, cond string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := core.DB.Unscoped().Model(table).Where(cond, args...).Count(&n).Error; err != nil {
		t.Fatalf("count %T %s: %v", table, cond, err)
	}
	return n
}

func TestCleanExpiredRecycleBin(t *testing.T) {
	newP0TestDB(t)
	ctx := context.Background()
	admin := mkUserP0(t, "rb-admin", model.Admin)
	b := mkBucketP0(t, "rb-bucket", model.NormalUser, admin.ID)
	setUsed(t, b.ID, 250) // 总占用 250B(与下方文件大小合计对齐)
	// 用户用量预置记账(与"软删不归还"一致:清除前全量占用)
	if err := core.DB.Model(&model.User{}).Where("id = ?", admin.ID).Update("used_space", 250).Error; err != nil {
		t.Fatalf("set user used: %v", err)
	}

	// ---- 桶根:过期文件 f1(100B)+ 未过期软删文件 f2(20B)----
	f1 := mkFileP0(t, b.ID, 0, "old.txt", 100)
	putMemObject(t, b.ID, f1.ID, string(make([]byte, 100)))
	mkShareP0(t, f1.ID, ItemKindFile) // f1 的分享应随清除级联删除
	f2 := mkFileP0(t, b.ID, 0, "fresh.txt", 20)
	putMemObject(t, b.ID, f2.ID, string(make([]byte, 20)))
	for _, f := range []*model.File{f1, f2} {
		if err := DeleteFile(ctx, DeleteFileArg{UserID: admin.ID, BucketID: b.ID, FileID: f.ID}); err != nil {
			t.Fatalf("delete file %d: %v", f.ID, err)
		}
	}
	backdateSoftDelete(t, &model.File{}, f1.ID, time.Now().AddDate(0, 0, -40)) // f1 过期
	// f2 保持最近删除(未过期)

	// ---- 过期目录 dir(sub + f3 30B + f4 50B),40 天前删除 ----
	dir := mkFolderP0(t, b.ID, 0, admin.ID, "dir")
	sub := mkFolderP0(t, b.ID, dir.ID, admin.ID, "sub")
	f3 := mkFileP0(t, b.ID, dir.ID, "a.txt", 30)
	putMemObject(t, b.ID, f3.ID, string(make([]byte, 30)))
	f4 := mkFileP0(t, b.ID, sub.ID, "b.txt", 50)
	putMemObject(t, b.ID, f4.ID, string(make([]byte, 50)))
	// dir 删除前,目录内先单独软删过 f6(40B)——其自身 deleted_at 为最近,
	// 但随整棵子树清除一并物理删除
	f6 := mkFileP0(t, b.ID, dir.ID, "del.txt", 40)
	putMemObject(t, b.ID, f6.ID, string(make([]byte, 40)))
	if err := DeleteFile(ctx, DeleteFileArg{UserID: admin.ID, BucketID: b.ID, FileID: f6.ID}); err != nil {
		t.Fatalf("delete f6: %v", err)
	}
	mkShareP0(t, dir.ID, ItemKindFolder) // dir 的分享应随清除级联删除
	if _, err := DeleteDir(ctx, DeleteDirArg{UserID: admin.ID, BucketID: b.ID, DirID: dir.ID}); err != nil {
		t.Fatalf("delete dir: %v", err)
	}
	backdateSoftDelete(t, &model.Folder{}, dir.ID, time.Now().AddDate(0, 0, -40)) // dir 过期

	// ---- 未过期目录 dir2(f5 10B),最近删除 ----
	dir2 := mkFolderP0(t, b.ID, 0, admin.ID, "dir2")
	f5 := mkFileP0(t, b.ID, dir2.ID, "c.txt", 10)
	putMemObject(t, b.ID, f5.ID, string(make([]byte, 10)))
	if _, err := DeleteDir(ctx, DeleteDirArg{UserID: admin.ID, BucketID: b.ID, DirID: dir2.ID}); err != nil {
		t.Fatalf("delete dir2: %v", err)
	}

	// 执行清理(cutoff = 30 天前)
	files, folders, err := CleanExpiredRecycleBin(ctx, CleanExpiredRecycleBinArg{Cutoff: time.Now().AddDate(0, 0, -30)})
	if err != nil {
		t.Fatalf("clean recycle bin: %v", err)
	}
	if files != 1 || folders != 1 {
		t.Fatalf("cleaned = (%d files, %d folders), want (1, 1)", files, folders)
	}

	// f1 记录/对象/分享已物理清除
	if n := countRows(t, &model.File{}, "id = ?", f1.ID); n != 0 {
		t.Fatalf("f1 rows = %d, want 0", n)
	}
	if objExists(b.ID, f1.ID) {
		t.Fatal("f1 object should be purged")
	}
	if n := countRows(t, &model.ShareLink{}, "bucket_item_id = ?", f1.ID); n != 0 {
		t.Fatalf("f1 share rows = %d, want 0", n)
	}

	// 过期目录整棵子树清除(dir/sub/f3/f4/f6 全无),对象无,dir 分享无
	for _, id := range []uint{dir.ID, sub.ID} {
		if n := countRows(t, &model.Folder{}, "id = ?", id); n != 0 {
			t.Fatalf("folder %d rows = %d, want 0", id, n)
		}
	}
	for _, id := range []uint{f3.ID, f4.ID, f6.ID} {
		if n := countRows(t, &model.File{}, "id = ?", id); n != 0 {
			t.Fatalf("file %d rows = %d, want 0", id, n)
		}
		if objExists(b.ID, id) {
			t.Fatalf("object %d should be purged", id)
		}
	}
	if n := countRows(t, &model.ShareLink{}, "bucket_item_id = ? AND item_type = 'folder'", dir.ID); n != 0 {
		t.Fatalf("dir share rows = %d, want 0", n)
	}

	// 未过期条目保留:f2 为软删行(deleted_at 非空);f5 在 dir2 子树内
	// 未逐条标记(deleted_at 空),但随 dir2 保留;dir2 为软删行
	var f2row model.File
	if err := core.DB.Unscoped().First(&f2row, f2.ID).Error; err != nil || !f2row.DeletedAt.Valid {
		t.Fatalf("f2 should be retained (soft-deleted), got %v", err)
	}
	if n := countRows(t, &model.File{}, "id = ?", f5.ID); n != 1 {
		t.Fatalf("f5 rows = %d, want 1(随 dir2 保留)", n)
	}
	var d2 model.Folder
	if err := core.DB.Unscoped().First(&d2, dir2.ID).Error; err != nil || !d2.DeletedAt.Valid {
		t.Fatalf("dir2 should be retained (soft-deleted), got %v", err)
	}

	// 用量:250 - 100 - (30+50+40) = 30(剩 f2 20 + f5 10)
	var bb model.Bucket
	if err := core.DB.First(&bb, b.ID).Error; err != nil || bb.UsedSpace != 30 {
		t.Fatalf("bucket used = %d, want 30", bb.UsedSpace)
	}
	if got := userUsed(t, admin.ID); got != 30 {
		t.Fatalf("user used = %d, want 30", got)
	}

	// 幂等:再跑一轮无更多可清条目
	if _, _, err := CleanExpiredRecycleBin(ctx, CleanExpiredRecycleBinArg{Cutoff: time.Now().AddDate(0, 0, -30)}); err != nil {
		t.Fatalf("re-clean: %v", err)
	}
}

// TestListRecycleBin 回收站查询:只返回"已删除且归属自己"的文件,
// 他人归属/未删除的文件不出现;deleted_at 倒序,common.Paginate 分页封装生效。
func TestListRecycleBin(t *testing.T) {
	newP0TestDB(t)
	ctx := context.Background()
	admin := mkUserP0(t, "rbq-admin", model.Admin)
	u2 := mkUserP0(t, "rbq-u2", model.NormalUser)
	b := mkBucketP0(t, "rbq-bucket", model.NormalUser, admin.ID)

	// 自己的回收站文件 ×2(先删的 deleted_at 更早,期望倒序后在后)
	own1 := mkFileP0(t, b.ID, 0, "own1.txt", 10)
	own2 := mkFileP0(t, b.ID, 0, "own2.txt", 20)
	// 他人归属的文件(u2 删除)
	other := mkFileP0(t, b.ID, 0, "other.txt", 30)
	// 自己未删除的文件(不应出现在回收站)
	active := mkFileP0(t, b.ID, 0, "active.txt", 40)
	_ = active
	core.DB.Model(&model.File{}).Where("id = ?", other.ID).Update("uploaded_by", u2.ID)

	for _, f := range []*model.File{own1, other} {
		if err := DeleteFile(ctx, DeleteFileArg{UserID: admin.ID, BucketID: b.ID, FileID: f.ID}); err != nil {
			t.Fatalf("delete %d: %v", f.ID, err)
		}
	}
	if err := DeleteFile(ctx, DeleteFileArg{UserID: admin.ID, BucketID: b.ID, FileID: own2.ID}); err != nil {
		t.Fatalf("delete own2: %v", err)
	}
	// 错开删除时间(own1 更早),保证 deleted_at 倒序断言确定性
	backdateSoftDelete(t, &model.File{}, own1.ID, time.Now().Add(-time.Hour))
	// active 不删

	// 自己的回收站:own1/own2(deleted_at 倒序 → own2 在前)
	total, items, err := ListRecycleBin(ctx, ListRecycleBinArg{UserID: admin.ID, Page: 1, PageSize: 50})
	if err != nil {
		t.Fatalf("list recycle bin: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("total=%d items=%d, want 2/2", total, len(items))
	}
	if items[0].ID != own2.ID || items[1].ID != own1.ID {
		t.Fatalf("order = [%d, %d], want [own2, own1](deleted_at 倒序)", items[0].ID, items[1].ID)
	}
	// 软删行(带 deleted_at 供前端展示删除时间)
	for i := range items {
		if !items[i].DeletedAt.Valid {
			t.Fatalf("file %d missing deleted_at", items[i].ID)
		}
	}

	// 他人视角:只看到自己归属的 other
	total2, items2, err := ListRecycleBin(ctx, ListRecycleBinArg{UserID: u2.ID, Page: 1, PageSize: 50})
	if err != nil {
		t.Fatalf("list recycle bin (u2): %v", err)
	}
	if total2 != 1 || len(items2) != 1 || items2[0].ID != other.ID {
		t.Fatalf("u2 recycle = %+v(total %d), want [other](1)", items2, total2)
	}

	// 分页:page_size=1 只回一页,total 仍为 2
	pagedTotal, paged, err := ListRecycleBin(ctx, ListRecycleBinArg{UserID: admin.ID, Page: 1, PageSize: 1})
	if err != nil {
		t.Fatalf("paged list: %v", err)
	}
	if len(paged) != 1 || paged[0].ID != own2.ID || pagedTotal != 2 {
		t.Fatalf("page1 = %+v(total %d), want [own2](2)", paged, pagedTotal)
	}
}

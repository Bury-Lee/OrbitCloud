// p1_perm_test.go —— 权限模型重构后的语义单测:
//   - 桶等级:CreateBucket 负等级报错;UpdateBucket 非管理员不得把等级改得比自身高、
//     管理等级不得松于访问等级、管理等级 0 = 跟随访问等级;
//   - 条目级可见性:非管理员只能把条目设为"自己所在组"可见(防自锁);
//   - visibilitySQL:损坏/空数组 JSON 行保守隐藏(列表不抛错),user==nil 拒绝一切。
package server

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"orbitcloud/core"
	"orbitcloud/model"
)

// TestCreateBucketRejectsNegativeLevel CreateBucket 负等级直接 ErrInvalidInput。
func TestCreateBucketRejectsNegativeLevel(t *testing.T) {
	db := newP0TestDB(t)
	defer dropP0DB(db)
	ctx := context.Background()

	u := mkUserP0(t, "alice", model.NormalUser)
	if _, err := CreateBucket(ctx, CreateBucketArg{OwnerID: u.ID, Name: "neg", PermissionLevel: -1}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("create bucket with negative permission: want ErrInvalidInput, got %v", err)
	}
	if _, err := CreateBucket(ctx, CreateBucketArg{OwnerID: u.ID, Name: "negm", PermissionLevel: model.NormalUser, ManagePermissionLevel: -1}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("create bucket with negative manage permission: want ErrInvalidInput, got %v", err)
	}
}

// TestUpdateBucketLevelRules 等级变更规则:
//   - 非管理员不得把访问/管理等级改得比自身权限更高 → ErrForbidden;
//   - 管理等级不得松于访问等级(manage > access)→ ErrInvalidInput;
//   - 管理等级 0 = 跟随访问等级(允许);管理员(IsAdmin)可把等级改到 0。
func TestUpdateBucketLevelRules(t *testing.T) {
	db := newP0TestDB(t)
	defer dropP0DB(db)
	ctx := context.Background()

	admin := mkUserP0(t, "admin", model.Admin)
	alice := mkUserP0(t, "alice", model.NormalUser) // 3 级
	b := mkBucketP0(t, "b1", model.NormalUser, alice.ID)

	// 非管理员:把访问等级改为 0(比自身高)→ ErrForbidden
	lv := model.SuperAdmin
	if _, err := UpdateBucket(ctx, UpdateBucketArg{OperatorID: alice.ID, BucketID: b.ID, In: UpdateBucketInput{PermissionLevel: &lv}}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-admin raise access level to 0: want ErrForbidden, got %v", err)
	}
	// 非管理员:把访问等级改为 5(比自身低)→ 允许
	lv5 := model.PermissionLevel(5)
	if _, err := UpdateBucket(ctx, UpdateBucketArg{OperatorID: alice.ID, BucketID: b.ID, In: UpdateBucketInput{PermissionLevel: &lv5}}); err != nil {
		t.Fatalf("non-admin lower access level to 5: %v", err)
	}
	// 非管理员:管理等级不得比自身高 → ErrForbidden
	mgr := model.Admin
	if _, err := UpdateBucket(ctx, UpdateBucketArg{OperatorID: alice.ID, BucketID: b.ID, In: UpdateBucketInput{ManagePermissionLevel: &mgr}}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-admin raise manage level: want ErrForbidden, got %v", err)
	}
	// 管理等级不得松于访问等级:访问 5,管理 9 → ErrInvalidInput
	access, manage := model.PermissionLevel(5), model.PermissionLevel(9)
	if _, err := UpdateBucket(ctx, UpdateBucketArg{OperatorID: alice.ID, BucketID: b.ID, In: UpdateBucketInput{PermissionLevel: &access, ManagePermissionLevel: &manage}}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("manage looser than access: want ErrInvalidInput, got %v", err)
	}
	// 管理等级 0 = 跟随访问等级 → 允许
	zero := model.SuperAdmin
	if _, err := UpdateBucket(ctx, UpdateBucketArg{OperatorID: alice.ID, BucketID: b.ID, In: UpdateBucketInput{ManagePermissionLevel: &zero}}); err != nil {
		t.Fatalf("manage level 0 (follow access): %v", err)
	}
	// 管理员:可把访问等级改为 0(专业人员,前端已三次确认)
	if _, err := UpdateBucket(ctx, UpdateBucketArg{OperatorID: admin.ID, BucketID: b.ID, In: UpdateBucketInput{PermissionLevel: &zero}}); err != nil {
		t.Fatalf("admin set access level 0: %v", err)
	}
}

// TestSetVisibilityRequiresMembership 非管理员只能把条目设为"自己所在组"可见:
// 不在组内 → ErrForbidden;加入组后 → 成功;管理员可设任意组。
func TestSetVisibilityRequiresMembership(t *testing.T) {
	db := newP0TestDB(t)
	defer dropP0DB(db)
	ctx := context.Background()

	admin := mkUserP0(t, "admin", model.Admin)
	alice := mkUserP0(t, "alice", model.NormalUser)
	b := mkBucketP0(t, "b1", model.NormalUser, alice.ID)
	f := mkFolderP0(t, b.ID, 0, alice.ID, "dir")
	g1 := mkGroupP0(t, "g1")
	g2 := mkGroupP0(t, "g2")

	// alice 不在任何组 → 设 g2 可见 → ErrForbidden
	if err := SetFolderVisibility(ctx, SetFolderVisibilityArg{UserID: alice.ID, BucketID: b.ID, FolderID: f.ID, GroupIDs: []uint{g2.ID}}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-member set visibility: want ErrForbidden, got %v", err)
	}
	// alice 加入 g1 → 设 g1 可见 → 成功
	if err := core.DB.Create(&model.UserGroupMember{GroupID: g1.ID, UserID: alice.ID}).Error; err != nil {
		t.Fatalf("join g1: %v", err)
	}
	if err := SetFolderVisibility(ctx, SetFolderVisibilityArg{UserID: alice.ID, BucketID: b.ID, FolderID: f.ID, GroupIDs: []uint{g1.ID}}); err != nil {
		t.Fatalf("member set own group visibility: %v", err)
	}
	// 恢复不限制(空列表)始终允许
	if err := SetFolderVisibility(ctx, SetFolderVisibilityArg{UserID: alice.ID, BucketID: b.ID, FolderID: f.ID, GroupIDs: nil}); err != nil {
		t.Fatalf("clear visibility: %v", err)
	}
	// 管理员可设任意组
	if err := SetFolderVisibility(ctx, SetFolderVisibilityArg{UserID: admin.ID, BucketID: b.ID, FolderID: f.ID, GroupIDs: []uint{g2.ID}}); err != nil {
		t.Fatalf("admin set any group visibility: %v", err)
	}
}

// TestListToleratesBrokenVisibilityJSON 损坏/空数组 JSON 的条目在列表中保守隐藏,
// 列表查询不抛错(json_each 不再直接炸);同时校验 visibilitySQL 的 user==nil 语义。
func TestListToleratesBrokenVisibilityJSON(t *testing.T) {
	db := newP0TestDB(t)
	defer dropP0DB(db)
	ctx := context.Background()

	alice := mkUserP0(t, "alice", model.NormalUser)
	b := mkBucketP0(t, "b1", model.NormalUser, alice.ID)

	// 正常目录
	mkFolderP0(t, b.ID, 0, alice.ID, "ok")
	// 损坏 JSON 目录
	bad := mkFolderP0(t, b.ID, 0, alice.ID, "bad")
	setVisible(t, bad.ID, `{"broken`)
	// 空数组目录(Go 规则 = 拒绝访问,列表应隐藏)
	empty := mkFolderP0(t, b.ID, 0, alice.ID, "empty")
	setVisible(t, empty.ID, `[]`)
	// 组内可见目录(alice 属于 g1)
	g := mkGroupP0(t, "g1")
	if err := core.DB.Create(&model.UserGroupMember{GroupID: g.ID, UserID: alice.ID}).Error; err != nil {
		t.Fatalf("join g1: %v", err)
	}
	grouped := mkFolderP0(t, b.ID, 0, alice.ID, "grouped")
	setVisible(t, grouped.ID, `[999,`+strconv.FormatUint(uint64(g.ID), 10)+`]`)

	// 列表查询:不抛错;损坏/空数组行隐藏;组内行可见
	files, folders, _, err := ListFiles(ctx, ListFilesArg{UserID: alice.ID, BucketID: b.ID, DirPath: "/", Page: 1, PageSize: 50})
	if err != nil {
		t.Fatalf("list files with broken visibility json: %v", err)
	}
	_ = files
	names := map[string]bool{}
	for _, f := range folders {
		names[f.Name] = true
	}
	if !names["ok"] || !names["grouped"] {
		t.Fatalf("expected ok+grouped visible, got %v", names)
	}
	if names["bad"] || names["empty"] {
		t.Fatalf("broken/empty visibility rows must be hidden, got %v", names)
	}

	// visibilitySQL:user == nil → 拒绝一切谓词("1 = 0")
	sql, args := visibilitySQL("sqlite", nil, nil)
	if sql != "1 = 0" || args != nil {
		t.Fatalf("nil user: want deny-all predicate, got sql=%q args=%v", sql, args)
	}
	// visibilitySQL:管理员 → 不过滤
	admin := mkUserP0(t, "admin2", model.Admin)
	sql, args = visibilitySQL("sqlite", admin, nil)
	if sql != "" || args != nil {
		t.Fatalf("admin: want no filter, got sql=%q args=%v", sql, args)
	}
}

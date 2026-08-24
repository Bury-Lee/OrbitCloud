// perm_test.go —— api 层桶管理权限判定单测(permCanManageBucket):
//   - owner 直接通过;管理员(IsAdmin)可代管任意桶;
//   - 其余按桶管理等级判定:管理等级 <=0 跟随访问等级;
//   - owner 软删后回退纯等级判定,不报错。
package api

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"orbitcloud/config"
	"orbitcloud/core"
	"orbitcloud/model"
	"orbitcloud/server"
)

// newPermTestDB 内存 SQLite 建库并挂 core.DB(仅权限判定用,不需要存储)。
func newPermTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:test-perm-%d?mode=memory&cache=shared", time.Now().UnixNano())
	cfg := config.Database{
		Source:  "sqlite",
		Sources: []config.DBConnConfig{{Source: "sqlite", DSN: dsn}},
	}
	db, err := cfg.InitDB(logger.Default)
	if err != nil {
		t.Fatalf("init db: %v", err)
	}
	core.DB = db
	return db
}

func dropPermTestDB(db *gorm.DB) {
	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}
	time.Sleep(10 * time.Millisecond)
}

func mkPermUser(t *testing.T, username string, perm model.PermissionLevel) *model.User {
	t.Helper()
	u := &model.User{Username: username, Password: "x", Name: username, PermissionLevel: perm, Status: 1}
	if err := core.DB.Create(u).Error; err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	return u
}

func mkPermBucket(t *testing.T, name string, access, manage model.PermissionLevel, owner uint) *model.Bucket {
	t.Helper()
	b := &model.Bucket{Name: name, PermissionLevel: access, ManagePermissionLevel: manage, OwnerID: owner, Status: 1}
	if err := core.DB.Create(b).Error; err != nil {
		t.Fatalf("create bucket %s: %v", name, err)
	}
	return b
}

func TestPermCanManageBucket(t *testing.T) {
	db := newPermTestDB(t)
	defer dropPermTestDB(db)
	ctx := context.Background()

	owner := mkPermUser(t, "owner", model.NormalUser)     // 3 级
	peer := mkPermUser(t, "peer", model.NormalUser)       // 3 级
	lower := mkPermUser(t, "lower", model.PermissionLevel(4))
	admin := mkPermUser(t, "admin", model.Admin)

	// 管理等级 3(与访问同级):owner/同级/管理员通过,4 级拒绝
	b1 := mkPermBucket(t, "b1", model.NormalUser, model.NormalUser, owner.ID)
	if err := permCanManageBucket(ctx, owner.ID, b1); err != nil {
		t.Fatalf("owner: %v", err)
	}
	if err := permCanManageBucket(ctx, peer.ID, b1); err != nil {
		t.Fatalf("same-level peer: %v", err)
	}
	if err := permCanManageBucket(ctx, admin.ID, b1); err != nil {
		t.Fatalf("admin: %v", err)
	}
	if err := permCanManageBucket(ctx, lower.ID, b1); !errors.Is(err, server.ErrForbidden) {
		t.Fatalf("level-4 on manage-3 bucket: want ErrForbidden, got %v", err)
	}

	// 管理等级 0 = 跟随访问等级(访问 3):4 级仍拒绝,3 级通过
	b2 := mkPermBucket(t, "b2", model.NormalUser, model.SuperAdmin, owner.ID)
	if err := permCanManageBucket(ctx, peer.ID, b2); err != nil {
		t.Fatalf("peer on manage-0 (follow access) bucket: %v", err)
	}
	if err := permCanManageBucket(ctx, lower.ID, b2); !errors.Is(err, server.ErrForbidden) {
		t.Fatalf("level-4 on manage-0 bucket: want ErrForbidden, got %v", err)
	}

	// owner 软删后:等级满足者仍可管理,不报错
	if err := core.DB.Delete(&model.User{}, owner.ID).Error; err != nil {
		t.Fatalf("delete owner: %v", err)
	}
	if err := permCanManageBucket(ctx, peer.ID, b1); err != nil {
		t.Fatalf("manage after owner deleted: %v", err)
	}
	if err := permCanManageBucket(ctx, lower.ID, b1); !errors.Is(err, server.ErrForbidden) {
		t.Fatalf("level-4 after owner deleted: want ErrForbidden, got %v", err)
	}
}

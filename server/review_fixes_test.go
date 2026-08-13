// review_fixes_test.go —— 批量上传 folder_id 直传失败进 failed 的单测(不静默回退桶根)。
// 复用 p0_acl_test.go 的内存 SQLite + 内存对象存储 mock。
package server

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"orbitcloud/core"
	"orbitcloud/model"
)

// TestUploadFilesFolderIDRestricted P1-1 修复:批量上传 folder_id 直传受限目录,
// 定位/校验失败必须进 failed,不得静默回退桶根(否则文件绕过受限目录 ACL 落到桶根)。
func TestUploadFilesFolderIDRestricted(t *testing.T) {
	db := newP0TestDB(t)
	defer dropP0DB(db)
	ctx := context.Background()

	admin := mkUserP0(t, "admin", 1)
	doctor := mkUserP0(t, "doctor", 5)
	b := mkBucketP0(t, "b1", 9, admin.ID)
	g := mkGroupP0(t, "g1")

	// 受限目录(仅组 g1 可见),创建者为 admin(非 doctor,排除创建者豁免)
	restricted := mkFolderP0(t, b.ID, 0, admin.ID, "restricted")
	setVisible(t, restricted.ID, fmt.Sprintf("[%d]", g.ID))

	// 非组内用户上传到受限目录 → 该项必须 failed,且不落任何文件(桶根/受限目录均无)
	results := UploadFiles(ctx, UploadFilesArg{
		UserID:   doctor.ID,
		BucketID: b.ID,
		FolderID: restricted.ID,
		Items:    []UploadItem{{Name: "x.txt", Reader: strings.NewReader("hi")}},
	})
	if len(results) != 1 || results[0].Error == "" {
		t.Fatalf("restricted folder upload: want 1 failed item, got %+v", results)
	}
	var n int64
	if err := core.DB.Model(&model.File{}).Count(&n).Error; err != nil {
		t.Fatalf("count files: %v", err)
	}
	if n != 0 {
		t.Fatalf("restricted upload leaked %d files (must be 0)", n)
	}

	// 回归:无限制目录 folder_id 直传 → 成功,文件落在目标目录
	open := mkFolderP0(t, b.ID, 0, doctor.ID, "open")
	results = UploadFiles(ctx, UploadFilesArg{
		UserID:   doctor.ID,
		BucketID: b.ID,
		FolderID: open.ID,
		Items:    []UploadItem{{Name: "y.txt", Reader: strings.NewReader("hi")}},
	})
	if len(results) != 1 || results[0].Error != "" {
		t.Fatalf("open folder upload: want success, got %+v", results)
	}
	var f model.File
	if err := core.DB.Where("bucket_id = ? AND folder_id = ?", b.ID, open.ID).First(&f).Error; err != nil {
		t.Fatalf("file not in open folder: %v", err)
	}
}

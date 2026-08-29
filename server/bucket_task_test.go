// bucket_task_test.go —— 目录删除任务(processDeleteDirTask)游标批次 + 可见组跳过测试。
package server

import (
	"context"
	"fmt"
	"testing"

	"orbitcloud/core"
	"orbitcloud/model"
)

// folderRow 读文件夹记录(softDeleted=true 时用 Unscoped 含软删行)。
func folderRow(t *testing.T, id uint, softDeleted bool) model.Folder {
	t.Helper()
	var f model.Folder
	q := core.DB
	if softDeleted {
		q = q.Unscoped()
	}
	if err := q.First(&f, id).Error; err != nil {
		t.Fatalf("load folder %d: %v", id, err)
	}
	return f
}

// fileRow 读文件记录(softDeleted=true 时用 Unscoped 含软删行)。
func fileRow(t *testing.T, id uint, softDeleted bool) model.File {
	t.Helper()
	var f model.File
	q := core.DB
	if softDeleted {
		q = q.Unscoped()
	}
	if err := q.First(&f, id).Error; err != nil {
		t.Fatalf("load file %d: %v", id, err)
	}
	return f
}

// taskGone 删除任务记录是否已硬删。
func taskGone(t *testing.T, taskID uint) bool {
	t.Helper()
	var n int64
	if err := core.DB.Unscoped().Model(&model.DeleteTask{}).Where("id = ?", taskID).Count(&n).Error; err != nil {
		t.Fatalf("count delete task %d: %v", taskID, err)
	}
	return n == 0
}

// runDeleteDirTask 落目录删除任务并同步处理。
func runDeleteDirTask(t *testing.T, bucketID, dirID, operatorID uint) uint {
	t.Helper()
	task := &model.DeleteTask{BucketID: bucketID, DirID: dirID, OperatorID: operatorID,
		OperatorLevel: int(model.NormalUser), Status: 0}
	if err := core.DB.Create(task).Error; err != nil {
		t.Fatalf("create delete task: %v", err)
	}
	if err := ProcessDeleteTask(context.Background(), ProcessDeleteTaskArg{TaskID: task.ID}); err != nil {
		t.Fatalf("ProcessDeleteTask: %v", err)
	}
	return task.ID
}

// TestProcessDeleteDirTaskSkipsInvisible 目录删除任务跳过操作者不可见的子文件夹
// (可见组 ACL):受限子文件夹整棵保留,任务根恢复 FolderUsable(不产生孤儿),
// 可见内容照常软删进回收站,任务记录删除。
func TestProcessDeleteDirTaskSkipsInvisible(t *testing.T) {
	newP0TestDB(t)
	op := mkUserP0(t, "op", model.NormalUser)
	g := mkGroupP0(t, "g")
	b := mkBucketP0(t, "b", model.NormalUser, op.ID)

	d := mkFolderP0(t, b.ID, 0, op.ID, "D")
	f1 := mkFileP0(t, b.ID, d.ID, "a.txt", 10)            // 可见文件
	s1 := mkFolderP0(t, b.ID, d.ID, op.ID, "S1")          // 可见子文件夹
	f3 := mkFileP0(t, b.ID, s1.ID, "c.txt", 20)           // S1 内文件
	s2 := mkFolderP0(t, b.ID, d.ID, op.ID, "S2")          // 受限子文件夹(op 不在组 g)
	setVisible(t, s2.ID, fmt.Sprintf("[%d]", g.ID))       // S2 仅 g 组可见
	f2 := mkFileP0(t, b.ID, s2.ID, "b.txt", 30)           // S2 内文件(同样被跳过)

	taskID := runDeleteDirTask(t, b.ID, d.ID, op.ID)

	if !taskGone(t, taskID) {
		t.Fatal("delete task record should be removed")
	}
	// 根 D:有残留 → 恢复 FolderUsable,不软删
	df := folderRow(t, d.ID, false)
	if df.Isable != model.FolderUsable {
		t.Fatalf("root isable = %d, want FolderUsable(1)", df.Isable)
	}
	// 受限子文件夹 S2 整棵保留(活动行)
	if sf := folderRow(t, s2.ID, false); sf.Isable != model.FolderUsable {
		t.Fatalf("invisible folder S2 isable = %d, want FolderUsable", sf.Isable)
	}
	fileRow(t, f2.ID, false) // 仍存在
	// 可见文件 f1、f3 已软删
	if fileRow(t, f1.ID, true).DeletedAt.Valid != true {
		t.Fatal("visible file f1 should be soft-deleted")
	}
	if fileRow(t, f3.ID, true).DeletedAt.Valid != true {
		t.Fatal("visible file f3 should be soft-deleted")
	}
	// 可见子文件夹 S1 已软删
	if folderRow(t, s1.ID, true).DeletedAt.Valid != true {
		t.Fatal("visible folder S1 should be soft-deleted")
	}
}

// TestProcessDeleteDirTaskFullClean 无受限内容时保持原行为:整棵子树软删,
// 根置 FolderUnusable + 软删,任务记录删除。
func TestProcessDeleteDirTaskFullClean(t *testing.T) {
	newP0TestDB(t)
	op := mkUserP0(t, "op", model.NormalUser)
	b := mkBucketP0(t, "b", model.NormalUser, op.ID)

	d := mkFolderP0(t, b.ID, 0, op.ID, "D")
	f1 := mkFileP0(t, b.ID, d.ID, "a.txt", 10)
	s1 := mkFolderP0(t, b.ID, d.ID, op.ID, "S1")
	f2 := mkFileP0(t, b.ID, s1.ID, "b.txt", 20)

	taskID := runDeleteDirTask(t, b.ID, d.ID, op.ID)

	if !taskGone(t, taskID) {
		t.Fatal("delete task record should be removed")
	}
	if df := folderRow(t, d.ID, true); df.DeletedAt.Valid != true || df.Isable != model.FolderUnusable {
		t.Fatalf("root should be soft-deleted+unusable, got isable=%d deleted=%v", df.Isable, df.DeletedAt.Valid)
	}
	if folderRow(t, s1.ID, true).DeletedAt.Valid != true {
		t.Fatal("subfolder S1 should be soft-deleted")
	}
	if fileRow(t, f1.ID, true).DeletedAt.Valid != true {
		t.Fatal("file f1 should be soft-deleted")
	}
	if fileRow(t, f2.ID, true).DeletedAt.Valid != true {
		t.Fatal("file f2 should be soft-deleted")
	}
}

// TestProcessDeleteDirTaskInvisibleFileSkipped 可见文件夹内的受限文件同样被跳过
// (文件级可见组匹配),根恢复可用。
func TestProcessDeleteDirTaskInvisibleFileSkipped(t *testing.T) {
	newP0TestDB(t)
	op := mkUserP0(t, "op", model.NormalUser)
	g := mkGroupP0(t, "g")
	b := mkBucketP0(t, "b", model.NormalUser, op.ID)

	d := mkFolderP0(t, b.ID, 0, op.ID, "D")
	f1 := mkFileP0(t, b.ID, d.ID, "a.txt", 10)
	f2 := mkFileP0(t, b.ID, d.ID, "b.txt", 20)
	if err := core.DB.Model(&model.File{}).Where("id = ?", f2.ID).
		Update("visible_to_groups", fmt.Sprintf("[%d]", g.ID)).Error; err != nil {
		t.Fatalf("set file visible groups: %v", err)
	}

	taskID := runDeleteDirTask(t, b.ID, d.ID, op.ID)

	if !taskGone(t, taskID) {
		t.Fatal("delete task record should be removed")
	}
	if df := folderRow(t, d.ID, false); df.Isable != model.FolderUsable {
		t.Fatalf("root isable = %d, want FolderUsable(残留文件)", df.Isable)
	}
	if fileRow(t, f1.ID, true).DeletedAt.Valid != true {
		t.Fatal("visible file f1 should be soft-deleted")
	}
	fileRow(t, f2.ID, false) // 受限文件保留(活动行)
}

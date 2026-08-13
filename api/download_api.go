// download_api.go 下载任务接口(文件夹下载登记,前端主导递归下载)。
// 单文件下载不登记任务,直接走原生下载接口;全部路由需 AuthMiddleware,
// 任务仅创建者可见。
package api

import (
	"context"

	"github.com/gin-gonic/gin"

	"orbitcloud/common"
	"orbitcloud/log"
	"orbitcloud/model"
	"orbitcloud/server"
)

// DownloadAPI 下载任务模块接口(无状态)。
type DownloadAPI struct{}

type CreateDownloadTaskReq struct {
	BucketID uint `json:"bucket_id"`
	FolderID uint `json:"folder_id"`
}

// CreateDownloadTask 登记文件夹下载任务(POST /download-tasks)。
// 请求体:{"bucket_id","folder_id"};下载进度/断点由前端自管,服务端不落进度。
func (DownloadAPI) CreateDownloadTask(c *gin.Context) {
	var req CreateDownloadTaskReq
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, server.ErrInvalidInput)
		return
	}

	if req.BucketID == 0 || req.FolderID == 0 {
		respondError(c, server.ErrInvalidInput)
		return
	}
	ctx := c.Request.Context()
	userID := currentUser(c)

	// 权限预检:与文件夹读取同一通路,防止未授权用户登记任务
	if _, err := precheckFolderRead(ctx, userID, req.BucketID, req.FolderID); err != nil {
		respondError(c, err)
		return
	}

	task, err := server.CreateDownloadTask(ctx, server.CreateDownloadTaskArg{UserID: userID, BucketID: req.BucketID, FolderID: req.FolderID})
	if err != nil {
		respondError(c, err)
		return
	}
	log.Infof("download task: register user=%d bucket=%d folder=%d → task=%d path=%q",
		userID, req.BucketID, req.FolderID, task.ID, task.FilePath)

	common.Success(c, task)
}

// GetDownloadTask 查询下载任务及起点文件夹当前元数据(GET /download-tasks/:id),
// 供前端校验任务仍有效并恢复下载;任务不存在 → 404,由前端重新登记。
func (DownloadAPI) GetDownloadTask(c *gin.Context) {
	taskID := parseIDParam(c, "id")
	if taskID == 0 {
		return
	}
	ctx := c.Request.Context()
	userID := currentUser(c)

	// 任务存在 → 归属(仅创建者)→ 与文件夹读取同口径的权限预检
	task, folder, err := downloadTaskResume(ctx, userID, taskID)
	if err != nil {
		log.Infof("download task: resume user=%d task=%d → %v", userID, taskID, err)
		respondError(c, err)
		return
	}
	log.Infof("download task: resume user=%d task=%d → folder=%d path=%q",
		userID, taskID, folder.ID, task.FilePath)

	common.Success(c, gin.H{"task": task, "folder": folder})
}

// downloadTaskResume 任务存在性 + 归属(仅创建者,不因管理员特殊放行)+
// 与文件夹读取完全同口径的权限预检(桶权限 + 条目/祖先 ACL)。
func downloadTaskResume(ctx context.Context, userID, taskID uint) (*model.DownloadTask, *model.Folder, error) {
	task, err := server.LoadDownloadTask(ctx, server.LoadDownloadTaskArg{TaskID: taskID})
	if err != nil {
		return nil, nil, err
	}
	// 归属校验:仅创建者可恢复(防断点信息泄露)
	if task.UserID != userID {
		return nil, nil, server.ErrForbidden
	}
	folder, err := precheckFolderRead(ctx, userID, task.BucketID, task.FolderID)
	if err != nil {
		return nil, nil, err
	}
	return task, folder, nil
}

// CompleteDownloadTask 完成/取消后清理任务(DELETE /download-tasks/:id,硬删,成功 204);
// 仅创建者可清理。
func (DownloadAPI) CompleteDownloadTask(c *gin.Context) {
	taskID := parseIDParam(c, "id")
	if taskID == 0 {
		return
	}
	ctx := c.Request.Context()
	userID := currentUser(c)

	// 任务存在 + 归属校验
	task, err := server.LoadDownloadTask(ctx, server.LoadDownloadTaskArg{TaskID: taskID})
	if err != nil {
		respondError(c, err)
		return
	}
	if task.UserID != userID {
		respondError(c, server.ErrForbidden)
		return
	}

	if err := server.DeleteDownloadTask(ctx, server.DeleteDownloadTaskArg{TaskID: taskID}); err != nil {
		respondError(c, err)
		return
	}
	log.Infof("download task: complete user=%d task=%d bucket=%d folder=%d path=%q",
		userID, taskID, task.BucketID, task.FolderID, task.FilePath)

	c.Status(204)
}

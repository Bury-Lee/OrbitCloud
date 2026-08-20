// router_file.go 文件模块路由注册。
// 文件走 /files/:fid,文件夹走 /dirs/:fid,按类型分离寻址。
// 流式接口通过 StreamingPoolMiddleware 准入(避免慢流挤占短请求),
// 其余接口通过全局 ExecPoolMiddleware 自动提交至 core.ExecPool 执行。
package api

import "github.com/gin-gonic/gin"

// FileRouter 注册文件模块路由(全部需 AuthMiddleware)。
func FileRouter(authed *gin.RouterGroup) {
	// —— 流式接口子组 ——————————————————————————
	// 使用 StreamingPoolMiddleware 做准入令牌控制(handler 原地执行);
	// 经 core.StreamingPool 限并发(默认 64),防止慢流耗尽 worker。
	stream := authed.Group("")
	stream.Use(StreamingPoolMiddleware)
	{
		stream.GET("/buckets/:id/files/:fid/download", AuthMiddleware, App.File.DownloadFile)
		stream.GET("/buckets/:id/files/:fid/preview", AuthMiddleware, App.File.PreviewFile)
		stream.GET("/buckets/:id/files/:fid/stream", QueryTokenAuthMiddleware, App.File.StreamFile)
		stream.GET("/buckets/:id/items/batch-download", AuthMiddleware, App.File.BatchDownload)
	}

	// —— 非流式接口(经全局 ExecPoolMiddleware 自动池化) ——————————
	authed.POST("/buckets/:id/files", AuthMiddleware, App.File.UploadFile)
	authed.POST("/buckets/:id/files/batch", AuthMiddleware, App.File.UploadFiles)
	authed.GET("/buckets/:id/files", AuthMiddleware, App.File.ListFiles)
	authed.GET("/buckets/:id/files/search", AuthMiddleware, App.File.FileSearch)
	authed.GET("/buckets/:id/dirs/search", AuthMiddleware, App.File.FolderSearch)
	authed.GET("/buckets/:id/files/:fid", AuthMiddleware, App.File.GetFileMeta)
	authed.GET("/buckets/:id/dirs/:fid", AuthMiddleware, App.File.GetFolderMeta)
	authed.POST("/buckets/:id/files/:fid/copy", AuthMiddleware, App.File.CopyFile)
	authed.POST("/buckets/:id/files/:fid/move", AuthMiddleware, App.File.MoveFile)
	authed.POST("/buckets/:id/dirs/:fid/move", AuthMiddleware, App.File.MoveFolder)
	authed.DELETE("/buckets/:id/files/:fid", AuthMiddleware, App.File.DeleteFile)
	authed.POST("/buckets/:id/dirs", AuthMiddleware, App.File.CreateDir)
	authed.DELETE("/buckets/:id/dirs/:fid", AuthMiddleware, App.File.DeleteDir)
	authed.POST("/buckets/:id/dirs/:fid/copy", AuthMiddleware, App.File.CopyFolder)
	// 批量操作(行式参数,失败逐项返回):
	authed.POST("/buckets/:id/items/batch-delete", AuthMiddleware, App.File.BatchDelete)
	authed.POST("/buckets/:id/items/batch-copy", AuthMiddleware, App.File.BatchCopy)
	authed.POST("/buckets/:id/items/batch-move", AuthMiddleware, App.File.BatchMove)
	authed.PUT("/buckets/:id/files/:fid/visibility", AuthMiddleware, App.File.SetFileVisibility)
	authed.PUT("/buckets/:id/dirs/:fid/visibility", AuthMiddleware, App.File.SetFolderVisibility)
}
// router_file.go 文件模块路由注册。
// 文件走 /files/:fid,文件夹走 /dirs/:fid,按类型分离寻址。
package api

import "github.com/gin-gonic/gin"

// FileRouter 注册文件模块路由(全部需 AuthMiddleware)。
func FileRouter(authed *gin.RouterGroup) {
	authed.POST("/buckets/:id/files", AuthMiddleware, App.File.UploadFile)
	authed.POST("/buckets/:id/files/batch", AuthMiddleware, App.File.UploadFiles)
	authed.GET("/buckets/:id/files", AuthMiddleware, App.File.ListFiles)
	authed.GET("/buckets/:id/files/:fid", AuthMiddleware, App.File.GetFileMeta)
	authed.GET("/buckets/:id/dirs/:fid", AuthMiddleware, App.File.GetFolderMeta)
	authed.GET("/buckets/:id/files/:fid/download", AuthMiddleware, App.File.DownloadFile)
	authed.GET("/buckets/:id/files/:fid/preview", AuthMiddleware, App.File.PreviewFile)
	authed.GET("/buckets/:id/files/:fid/stream", QueryTokenAuthMiddleware, App.File.StreamFile)
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
	authed.GET("/buckets/:id/items/batch-download", AuthMiddleware, App.File.BatchDownload)
	authed.PUT("/buckets/:id/files/:fid/visibility", AuthMiddleware, App.File.SetFileVisibility)
	authed.PUT("/buckets/:id/dirs/:fid/visibility", AuthMiddleware, App.File.SetFolderVisibility)
}

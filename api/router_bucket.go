// router_bucket.go 桶模块路由注册。
package api

import "github.com/gin-gonic/gin"

// BucketRouter 注册桶模块路由(全部需 AuthMiddleware)。
func BucketRouter(authed *gin.RouterGroup) {
	authed.POST("/buckets", AuthMiddleware, App.Bucket.CreateBucket)
	authed.GET("/buckets", AuthMiddleware, App.Bucket.ListBuckets)
	authed.GET("/buckets/:id", AuthMiddleware, App.Bucket.GetBucket)
	authed.PUT("/buckets/:id", AuthMiddleware, App.Bucket.UpdateBucket)
	authed.DELETE("/buckets/:id", AuthMiddleware, App.Bucket.DeleteBucket)
}

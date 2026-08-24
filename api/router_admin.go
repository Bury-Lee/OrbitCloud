// router_admin.go —— 管理接口路由(pprof 按需采样等)。
package api

import "github.com/gin-gonic/gin"

// PprofRouter pprof 管理路由(管理员;挂 streamAuthed 组避免占用执行池,
// CPU 采样等慢操作与流式接口同等待遇)。
func PprofRouter(g *gin.RouterGroup) {
	p := g.Group("/admin/pprof", AuthMiddleware, AdminMiddleware)
	{
		p.POST("/cpu", App.Pprof.Cpu)           // CPU 火焰图采样(seconds=30,当场采样落盘+下载)
		p.GET("/heap", App.Pprof.Heap)          // 内存快照(排查泄漏)
		p.GET("/goroutine", App.Pprof.Goroutine) // 协程快照(死锁/泄漏诊断)
		p.GET("/block", App.Pprof.Block)        // 阻塞快照
		p.GET("/mutex", App.Pprof.Mutex)        // 锁竞争快照
		p.GET("/list", App.Pprof.List)          // 已保存采样文件列表(日志文件)
		p.GET("/download", App.Pprof.Download)  // 下载采样文件(白名单)
	}
}

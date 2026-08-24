// Package core 是最底层基础设施:存放全局单例(GlobalConfig / DB / JWT / Storage / Pool / ExecPool)。
// 只被上层包(server/api/cron/flag/log/appinit)引用,自身不 import 上层业务包,避免循环引用。
// 初始化由 main 包调用 appinit 各函数并赋值到下面的全局变量,运行期只读。
package core

import (
	"orbitcloud/config"

	agilepool "github.com/Yiming1997/agilePool/v2"
	"gorm.io/gorm"
)

var (
	// GlobalConfig 全局配置,启动期加载后只读。
	GlobalConfig *config.Config

	// DB 元数据库连接。
	DB *gorm.DB

	// JWT JWT 服务单例,签发/校验令牌。
	JWT *JwtService

	// Pool 全局并发任务池(agilePool),HTTP handler 内后台任务统一经其提交。
	Pool *agilepool.Pool

	// ExecPool 全局请求执行池(agilePool)。非流式 API handler 通过
	// ExecPoolMiddleware 提交至此执行;池有界队列提供显式背压。
	ExecPool *agilepool.Pool

	// StreamExecPool 流式请求执行池(agilePool)。下载/预览/流媒体 handler
	// 经此池执行,与短请求分离,防止慢流挤占。
	StreamExecPool *agilepool.Pool

	// RequestPool 普通请求准入池(AdmissionPool,已由 ExecPool 替代,保留以便未来兼容)。
	RequestPool *AdmissionPool

	// StreamingPool 流式请求准入池(AdmissionPool),仅做准入令牌控制,
	// handler 仍在 Gin goroutine 原地执行。与 StreamExecPool 配合使用。
	StreamingPool *AdmissionPool

	// Storage 对象存储单例,提供 Put/Get/Delete 对象实体能力。
	Storage ObjectStorage
)
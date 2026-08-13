package core

// Package core 是最底层基础设施:存放全局单例(GlobalConfig / DB / JWT / Storage / Pool)。
// 只被上层包(server/api/cron/flag/log/appinit)引用,自身不 import 上层业务包,避免循环引用。
// 初始化由 main 包调用 appinit 各函数并赋值到下面的全局变量,运行期只读。

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

	// Pool 全局并发任务池,HTTP handler 内并发任务统一经其提交。
	Pool *agilepool.Pool

	// Storage 对象存储单例,提供 Put/Get/Delete 对象实体能力。
	Storage ObjectStorage
)

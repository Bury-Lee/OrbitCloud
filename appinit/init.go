// Package appinit 存放各处的初始化程序(core 仅存全局数据,初始化逻辑在此)。
//
// 约定:
//   - 每个初始化函数均为纯函数式:func (输入参数) (返回参数, error),不直接改全局变量;
//   - 由 main 包按依赖顺序调用,并把返回结果赋给 core 的全局变量;
//   - 依赖方向:appinit → core/config/log/model(单向,无环)。
//
// 组装顺序(见 main.go):
//
//	cfg, err := InitConfig()          // 加载配置 + 建本地目录
//	InitLog(cfg)                      // 日志输出目标/级别(DB 就绪前)
//	db, err := InitDB(cfg)            // 数据库(自动建表 + 重连)
//	AttachLogDBWriter(cfg)            // 可选:日志入库(config.Log.DBLevel 非空时)
//	core.JWT     = InitJWT(cfg)       // JWT 服务
//	core.Storage, _ = InitStorage(cfg) // 对象存储驱动
//	core.Pool    = InitPool(cfg)      // agilePool 协程池
package appinit

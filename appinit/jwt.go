package appinit

// JWT 服务初始化(类型与实现仍在 core;由 main 赋值给 core.JWT)。

import (
	"orbitcloud/config"
	"orbitcloud/core"
)

// InitJWT 构造 JWT 服务。
func InitJWT(cfg *config.Config) *core.JwtService {
	return core.InitJwtServers(&cfg.AccessJWT, &cfg.RefreshJWT)
}

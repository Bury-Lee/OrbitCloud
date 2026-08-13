package appinit

// 配置初始化:加载 ./config.yaml(不存在时用内置 embed 默认配置生成)并做校验。

import (
	"os"

	"orbitcloud/config"
)

// InitConfig 加载并校验配置(不存在时自动用内置默认配置生成 ./config.yaml)。
// 返回 *config.Config;由 main 赋值给 core.GlobalConfig。
func InitConfig() (*config.Config, error) {
	// 检查本地目录 .orbitcloud/ 是否存在(工作目录,config.InitConfig 内部也会兜底)
	if _, err := os.Stat(".orbitcloud/"); os.IsNotExist(err) {
		if err := os.MkdirAll(".orbitcloud/", 0o755); err != nil {
			return nil, err
		}
	}

	return (config.Config{}).InitConfig()
}

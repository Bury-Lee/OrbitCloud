package config

// 内置默认配置:编译期将 config/default.yaml 硬编码进二进制。
// ./config.yaml 不存在时,InitConfig 自动用本配置生成;`orbitcloud -initConfig` 可强制覆盖写入。
import (
	_ "embed"
	"fmt"
	"os"
)

//go:embed default.yaml
var DefaultConfigYAML []byte

// WriteDefaultConfigFile 将内置默认配置(embed 硬编码)写入指定路径(强制覆盖)。
func WriteDefaultConfigFile(path string) error {
	if err := os.WriteFile(path, DefaultConfigYAML, 0o644); err != nil {
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	return nil
}

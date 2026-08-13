package config

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"gorm.io/driver/postgres"

	// sqlite 方言:使用纯 Go 驱动 github.com/glebarez/sqlite(零 CGO 依赖,
	// 与 gorm.io/driver/sqlite 同签名 sqlite.Open;无 gcc 环境亦可构建/运行)
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/plugin/dbresolver"

	"orbitcloud/model"
)

// InitDB 初始化元数据库连接并自动建表(gorm AutoMigrate)。
//   - 主库 = sources[0](承担 AutoMigrate/审计日志);
//   - 多写库或配置读库时注册 dbresolver:写走 sources(轮询)、读走 replicas(负载均衡);
//   - 单一写库且无读库 → 不注册 resolver,读写全走主库。
// 连接不可用时将持续自动重试。
func (d Database) InitDB(lg logger.Interface) (*gorm.DB, error) {
	// 0. 校验写库列表非空及 sqlite 限制(单写库、无读库,遍历全部写库防混合列表绕过)
	if len(d.Sources) == 0 {
		return nil, errors.New("config: database.sources must not be empty (at least one write source)")
	}
	for i := range d.Sources {
		if strings.EqualFold(d.Sources[i].effectiveSource(d.Source), "sqlite") {
			if len(d.Replicas) > 0 {
				return nil, errors.New("config: sqlite does not support read-write separation (replicas must be empty)")
			}
			if len(d.Sources) > 1 {
				return nil, errors.New("config: sqlite does not support multiple write sources")
			}
			log.Printf("config: WARN SQLite 仅用于开发/测试,禁止生产部署")
			break
		}
	}

	// 1. 主库 = sources[0]
	dialector, err := d.connDialector(d.Sources[0], "sources", 0)
	if err != nil {
		return nil, err
	}

	// 2. 打开连接(TranslateError: true 是 isUniqueViolation 依赖的关键配置)
	db, err := gorm.Open(dialector, &gorm.Config{
		TranslateError: true,
		Logger:         lg,
	})
	if err != nil {
		return nil, fmt.Errorf("config: open database: %w", err)
	}

	// 3. 连接池调优(仅对主库 sources[0] 生效; dbresolver 的其它写库/读库由插件
	//    内部 gorm.Open 建立, 使用 gorm 默认连接池——见 poolFor 注释)
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("config: get sql.DB: %w", err)
	}
	if maxIdle, maxOpen := d.poolFor(d.Sources[0]); maxIdle > 0 || maxOpen > 0 {
		if maxIdle > 0 {
			sqlDB.SetMaxIdleConns(maxIdle)
		}
		if maxOpen > 0 {
			sqlDB.SetMaxOpenConns(maxOpen)
		}
	}

	// 4. 自动迁移(表结构由 model gorm tag 驱动;必须走主库,先于 dbresolver 注册)
	//    Log 与 DeleteTask 必须包含,否则对应功能落库报错;UserGroup/UserGroupMember 支撑条目级可见性。
	if err := db.AutoMigrate(
		&model.User{}, &model.Bucket{}, &model.Folder{}, &model.File{},
		&model.UserGroup{}, &model.UserGroupMember{},
		&model.RefreshToken{}, &model.ShareLink{}, &model.OperationLog{},
		&model.Log{}, &model.DeleteTask{}, &model.CopyTask{},
		&model.DownloadTask{}, // 下载任务(断点续传)
	); err != nil {
		return nil, fmt.Errorf("config: automigrate: %w", err)
	}

	// 5. 读写分离:多写库或配置了读库 → 注册 dbresolver(写 sources / 读 replicas)
	if len(d.Sources) > 1 || len(d.Replicas) > 0 {
		srcDialectors := make([]gorm.Dialector, 0, len(d.Sources))
		for i, s := range d.Sources {
			dl, err := d.connDialector(s, "sources", i)
			if err != nil {
				return nil, err
			}
			srcDialectors = append(srcDialectors, dl)
		}
		repDialectors := make([]gorm.Dialector, 0, len(d.Replicas))
		for i, r := range d.Replicas {
			dl, err := d.connDialector(r, "replicas", i)
			if err != nil {
				return nil, err
			}
			repDialectors = append(repDialectors, dl)
		}
		resolver := dbresolver.Register(dbresolver.Config{
			Sources:  srcDialectors, // 写库(多写库轮询;需库间主主复制)
			Replicas: repDialectors, // 读库(负载均衡)
			Policy:   dbresolver.RandomPolicy{},
		})
		if err := db.Use(resolver); err != nil {
			return nil, fmt.Errorf("config: register dbresolver: %w", err)
		}
		// 审计日志固定走主库(gorm.DB),避免复制延迟丢失
		resolver.Register(dbresolver.Config{}, &model.OperationLog{})
	}

	// 6. 连接自检:失败则一直重试(分布式要求;进程退出前不放弃)
	for {
		if err := sqlDB.Ping(); err == nil {
			break
		} else {
			log.Printf("config: WARN database ping failed, retrying in 2s: %v", err)
			time.Sleep(2 * time.Second)
		}
	}

	return db, nil
}

// connDialector 为单个连接配置构建 gorm.Dialector(sqlite|postgres)。
// dsn 为空时用 host/port/username/password/database 拼装(postgres)。
func (d Database) connDialector(conn DBConnConfig, list string, idx int) (gorm.Dialector, error) {
	switch strings.ToLower(conn.effectiveSource(d.Source)) {
	case "sqlite":
		dsn := strings.TrimSpace(conn.DSN)
		if dsn == "" {
			dsn = "orbitcloud.db"
		}
		return sqlite.Open(dsn), nil
	case "postgres", "postgresql":
		dsn := strings.TrimSpace(conn.DSN)
		if dsn == "" {
			dsn = fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable TimeZone=Asia/Shanghai",
				conn.Host, conn.Port, conn.Username, conn.Password, conn.Database)
		}
		return postgres.Open(dsn), nil
	default:
		return nil, fmt.Errorf("config: %s[%d] unsupported source %q (sqlite|postgres)", list, idx, conn.Source)
	}
}

// poolFor 返回主库(sources[0])生效的连接池参数(项级 > database 级)。
// 仅主库生效:dbresolver 的其它写库/读库使用 gorm 默认连接池,无法经此处配置。
func (d Database) poolFor(conn DBConnConfig) (maxIdle, maxOpen int) {
	maxIdle, maxOpen = d.MaxIdleConns, d.MaxOpenConns
	if conn.MaxIdleConns > 0 {
		maxIdle = conn.MaxIdleConns
	}
	if conn.MaxOpenConns > 0 {
		maxOpen = conn.MaxOpenConns
	}
	return maxIdle, maxOpen
}

// InitConfig 加载 ./config.yaml(必须位于工作目录)。
// 文件不存在时自动用内置默认配置(embed)生成;`orbitcloud -initConfig` 可强制覆盖。
// 不做缺省值兜底——检查到问题则报错,按提示修改配置后重启。
func (c Config) InitConfig() (*Config, error) {
	const path = "./config.yaml"

	// 1. 文件不存在 → 用内置默认配置(embed)生成 ./config.yaml
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := WriteDefaultConfigFile(path); err != nil {
			return nil, fmt.Errorf("config: %s missing and default config unreadable: %w", path, err)
		}
		log.Printf("config: 已生成 %s(内置默认配置),请按需修改后重新启动", path)
	}

	// 2. 解析 YAML(os.ReadFile + yaml.Unmarshal)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}

	// 3. 校验(不兜底,发现即报错,按提示修改后重启)
	if strings.TrimSpace(c.App.Name) == "" {
		return nil, errors.New("config: app.name is required")
	}
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return nil, fmt.Errorf("config: server.port must be in 1~65535, got %d", c.Server.Port)
	}
	if strings.TrimSpace(c.AccessJWT.Secret) == "" {
		return nil, errors.New("config: jwt.secret is required (suggest env orbitcloud_JWT_SECRET)")
	}
	if len(c.Database.Sources) == 0 {
		return nil, errors.New("config: database.sources must not be empty (at least one write source)")
	}
	for i, s := range c.Database.Sources {
		switch strings.ToLower(s.effectiveSource(c.Database.Source)) {
		case "sqlite":
			if strings.TrimSpace(s.DSN) == "" {
				return nil, fmt.Errorf("config: database.sources[%d]: sqlite requires dsn (database file path)", i)
			}
		case "postgres", "postgresql":
			if strings.TrimSpace(s.DSN) == "" && strings.TrimSpace(s.Host) == "" {
				return nil, fmt.Errorf("config: database.sources[%d]: dsn or host is required (postgres)", i)
			}
		default:
			return nil, fmt.Errorf("config: database.sources[%d] unsupported source %q (sqlite|postgres)", i, s.Source)
		}
	}
	for i, r := range c.Database.Replicas {
		if strings.TrimSpace(r.DSN) == "" && strings.TrimSpace(r.Host) == "" {
			return nil, fmt.Errorf("config: database.replicas[%d]: dsn or host is required", i)
		}
	}
	if strings.TrimSpace(string(c.Storage.Provider)) == "" {
		return nil, errors.New("config: storage.provider is required")
	}
	if len(c.Nodes) == 0 {
		return nil, errors.New("config: nodes must not be empty (at least one storage node)")
	}
	for i, n := range c.Nodes {
		if strings.TrimSpace(n.ID) == "" {
			return nil, fmt.Errorf("config: nodes[%d].id is required", i)
		}
		if strings.EqualFold(n.Driver, "s3") && strings.TrimSpace(n.Endpoint) == "" {
			return nil, fmt.Errorf("config: nodes[%d].endpoint is required when driver is s3", i)
		}
	}

	return &c, nil
}

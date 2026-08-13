package config

import "strings"

// Database 数据库配置:写库列表(sources)+ 读库列表(replicas),经 dbresolver 实现读写分离。
//   - sources 必填(≥1),首个为主库(承担 AutoMigrate 建表/审计日志);
//   - replicas 为空 → 读写同库;非空 → 读操作走读库(负载均衡);
//   - 多写库时写请求轮询分发,依赖库间主主复制,生产常规为单写库 + N 读库。
type Database struct {
	Source       string `yaml:"source" json:"source"`                 // 默认数据库类型: sqlite | postgres(列表项未指定时继承)
	Charset      string `yaml:"charset" json:"charset"`               // 字符集, 默认 utf8mb4
	LogLevel     string `yaml:"log_level" json:"log_level"`           // GORM 日志级别: silent, error, warn, info
	MaxIdleConns int    `yaml:"max_idle_conns" json:"max_idle_conns"` // 主库(sources[0])最大空闲连接数(项级可覆盖; 其它写库/读库用 gorm 默认池)
	MaxOpenConns int    `yaml:"max_open_conns" json:"max_open_conns"` // 主库(sources[0])最大打开连接数(项级可覆盖; 其它写库/读库用 gorm 默认池)
	// 写库列表(必填 ≥1; 首个为主库)。
	Sources []DBConnConfig `yaml:"sources" json:"sources"`
	// 读库列表(可选; sqlite 不支持读写分离, 仅 postgres 支持)。
	Replicas []DBConnConfig `yaml:"replicas" json:"replicas"`
}

// DBConnConfig 单个数据库连接(sources 写库 / replicas 读库共用)。
// dsn 优先于 host/port/username/password/database 拼装(未提供 dsn 时用这些字段拼);
// 连接池字段为 0 时继承 database 级默认值。
type DBConnConfig struct {
	Source       string `yaml:"source" json:"source"`                 // 数据库类型(空 = 继承 database.source)
	Host         string `yaml:"host" json:"host"`                     // 数据库主机地址(postgres 拼装用)
	Port         int    `yaml:"port" json:"port"`                     // 数据库端口
	Username     string `yaml:"username" json:"username"`             // 用户名
	Password     string `yaml:"password" json:"password"`             // 密码
	Database     string `yaml:"database" json:"database"`             // 数据库名称
	DSN          string `yaml:"dsn" json:"dsn"`                       // 自定义 DSN(可选, 优先于以上字段; sqlite 必须给文件路径)
	MaxIdleConns int    `yaml:"max_idle_conns" json:"max_idle_conns"` // 最大空闲连接数(0 = 继承 database 级)
	MaxOpenConns int    `yaml:"max_open_conns" json:"max_open_conns"` // 最大打开连接数(0 = 继承 database 级)
}

// effectiveSource 返回该连接项生效的数据库类型(项级 source > database.source)。
func (c DBConnConfig) effectiveSource(def string) string {
	if strings.TrimSpace(c.Source) != "" {
		return c.Source
	}
	return def
}

// Server HTTP服务配置
type Server struct {
	Host           string `yaml:"host" json:"host"`                         // 监听地址, 默认 0.0.0.0
	Port           int    `yaml:"port" json:"port"`                         // 监听端口, 默认 8080
	ReadTimeout    int    `yaml:"read_timeout" json:"read_timeout"`         // 读超时时间(分钟;0=不限)
	WriteTimeout   int    `yaml:"write_timeout" json:"write_timeout"`       // 写超时时间(分钟;0=不限,下载流式返回必需)
	IdleTimeout    int    `yaml:"idle_timeout" json:"idle_timeout"`         // 空闲超时时间(分钟;0=不限)
	MaxHeaderBytes int    `yaml:"max_header_bytes" json:"max_header_bytes"` // 最大请求头大小(字节)
	Mode           string `yaml:"mode" json:"mode"`                         // 运行模式: debug, release, test
}

type StorageProvider string

const (
	// 存储提供商常量,取值与 config.yaml 的 storage.provider 小写写法一致。
	Minio  StorageProvider = "minio"
	Rustfs StorageProvider = "rustfs"
)

// Storage 存储引擎配置(config.yaml 的 storage 段,含对象存储后端与分片/副本参数)。
type Storage struct {
	// —— 对象存储后端 ——
	// Provider 仅为标识字段(仅校验非空),驱动由 storage.driver(s3|local)决定。
	Provider   StorageProvider `yaml:"provider" json:"provider"`       // 存储提供商标识: minio, rustfs 等(供展示/校验,不参与驱动选择)
	Endpoint   string          `yaml:"endpoint" json:"endpoint"`       // 服务端点地址
	AccessKey  string          `yaml:"access_key" json:"access_key"`   // 访问密钥ID
	SecretKey  string          `yaml:"secret_key" json:"secret_key"`   // 访问密钥Secret
	Region     string          `yaml:"region" json:"region"`           // 区域(如 cn-north-1)
	UseSSL     bool            `yaml:"use_ssl" json:"use_ssl"`         // 是否启用SSL
	Timeout    int             `yaml:"timeout" json:"timeout"`         // 超时时间(秒)
	MaxRetries int             `yaml:"max_retries" json:"max_retries"` // 最大重试次数

	// —— 存储引擎参数 ——
	ChunkSize         int    `yaml:"chunk_size" json:"chunk_size"`                 // 分片大小(MB,默认 5;实现侧转字节)
	MinReplicas       int    `yaml:"min_replicas" json:"min_replicas"`             // 最小副本数(默认 2)
	Driver            string `yaml:"driver" json:"driver"`                         // 默认存储驱动: s3 | local
	RepairInterval    string `yaml:"repair_interval" json:"repair_interval"`       // 副本修复扫描周期(默认 30s)
	RepairConcurrency int    `yaml:"repair_concurrency" json:"repair_concurrency"` // 修复并发上限(默认 4)
}

// App 应用基础配置
type App struct {
	Name        string `yaml:"name" json:"name"`               // 应用名称
	Version     string `yaml:"version" json:"version"`         // 版本号
	Environment string `yaml:"environment" json:"environment"` // 环境: dev, test, prod
	Debug       bool   `yaml:"debug" json:"debug"`             // 是否开启调试模式
	Timezone    string `yaml:"timezone" json:"timezone"`       // 时区, 默认 Asia/Shanghai
}

// AccessJWT 访问令牌认证配置
type AccessJWT struct {
	Secret        string `yaml:"secret" json:"secret"`                 // 签名密钥
	ExpireMinutes int    `yaml:"expire_minutes" json:"expire_minutes"` // 过期时间(分钟,默认 720)
	Issuer        string `yaml:"issuer" json:"issuer"`                 // 签发者
	Algorithm     string `yaml:"algorithm" json:"algorithm"`           // 签名算法(HS256;空则默认 HS256)
}

// RefreshJWT 刷新令牌配置(与 AccessJWT 分离;Secret 为空时由访问密钥派生,见 core.InitJwtServers)。
type RefreshJWT struct {
	Secret        string `yaml:"secret" json:"secret"`                 // 签名密钥(为空时由访问密钥派生)
	ExpireMinutes int    `yaml:"expire_minutes" json:"expire_minutes"` // 过期时间(分钟,默认 7*24*60)
	Issuer        string `yaml:"issuer" json:"issuer"`                 // 签发者
	Algorithm     string `yaml:"algorithm" json:"algorithm"`           // 签名算法(HS256;空则默认 HS256)
}

// Log 日志配置
type Log struct {
	Level         string `yaml:"level" json:"level"`                   // 日志记录的最低级别: debug, info, warn, error, panic
	Format        string `yaml:"format" json:"format"`                 // 日志格式: json, text
	OutputPath    string `yaml:"output_path" json:"output_path"`       // 日志输出路径
	MaxSize       int    `yaml:"max_size" json:"max_size"`             // 日志文件最大大小(KB;>0 时单文件超过即轮转新文件;<=0 仅按日期轮转)
	MaxBackups    int    `yaml:"max_backups" json:"max_backups"`       // 最大保留旧文件数
	MaxAge        int    `yaml:"max_age" json:"max_age"`               // 最大保留天数
	DBLevel       string `yaml:"db_level" json:"db_level"`             // 日志入库的最低级别;为""时表示不入库
	RetentionDays int    `yaml:"retention_days" json:"retention_days"` // 日志保留天数,默认 30;cron 按此清理 logs 与 operation_logs 两表
}

// Config 总配置结构
type Config struct {
	App      App      `yaml:"app" json:"app"`
	Server   Server   `yaml:"server" json:"server"`
	Database Database `yaml:"database" json:"database"`
	AccessJWT  AccessJWT  `yaml:"jwt" json:"jwt"`
	RefreshJWT RefreshJWT `yaml:"refresh_jwt" json:"refresh_jwt"` // 刷新令牌
	Log        Log        `yaml:"log" json:"log"`
	Storage    Storage    `yaml:"storage" json:"storage"` // 存储引擎 = 对象存储

	Health Health `yaml:"health" json:"health"`
	Pool   Pool   `yaml:"pool" json:"pool"`
	Nodes  []Node `yaml:"nodes" json:"nodes"`
}

// Health 健康检查配置(config.yaml 的 health 段)。
type Health struct {
	CheckInterval string `yaml:"check_interval" json:"check_interval"` // 节点探测周期(默认 5s)
	FailThreshold int    `yaml:"fail_threshold" json:"fail_threshold"` // 连续失败 N 次标记 inactive(默认 3)
}

// Pool 协程池配置(config.yaml 的 pool 段)。
type Pool struct {
	MaxWorkers int `yaml:"max_workers" json:"max_workers"` // 最大工作协程数(默认 64)
	QueueSize  int `yaml:"queue_size" json:"queue_size"`   // 任务队列容量(默认 1024;超出后提交方阻塞)
}

// Node 存储节点配置(config.yaml 的 nodes 列表项)。
// 当前仅做非空与字段校验(config/init.go);驱动/端点/凭据实际取自 storage 段,逐节点覆盖为后续扩展预留。
type Node struct {
	ID        string `yaml:"id" json:"id"`                 // 节点 ID(唯一,如 node1)
	Driver    string `yaml:"driver" json:"driver"`         // 可选,覆盖 storage.driver(s3 | local)
	Path      string `yaml:"path" json:"path"`             // s3: bucket 名;local: 目录路径
	Endpoint  string `yaml:"endpoint" json:"endpoint"`     // s3 驱动必填:该节点 S3 端点
	AccessKey string `yaml:"access_key" json:"access_key"` // 可选,覆盖全局 storage.access_key
	SecretKey string `yaml:"secret_key" json:"secret_key"` // 可选,覆盖全局 storage.secret_key
	MaxSpace  int64  `yaml:"max_space" json:"max_space"`   // 容量配额(GB;0=不限)
	Enabled   bool   `yaml:"enabled" json:"enabled"`       // 可选,默认 true;false = 启动时不注册该节点
}

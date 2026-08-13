package flag

// Package flag 命令行指令定义与实现。
// 约定:权限 0(超级管理员)只能经命令行创建,HTTP API 不可创建权限 0 的用户;
// 配置固定从工作目录 ./config.yaml 读取,不支持命令行覆盖路径。
//
// 指令分两批执行:
//   - RunPreInit(在 appinit 初始化之前,无需任何初始化):-initConfig 强制生成配置后退出;
//   - Run(在 appinit 初始化之后,需 DB 就绪):--add-superadmin / --version / --help。

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"orbitcloud/config"
	"orbitcloud/core"
	"orbitcloud/model"
)

// 指令名常量。
const (
	CmdAddSuperAdmin = "add-superadmin"
	CmdInitConfig    = "init-config"
)

// ErrExit 指令命中并执行完毕的哨兵:main 收到后以退出码 0 结束进程(不启动服务)。
var ErrExit = errors.New("flag: command executed, exit")

const usageText = `orbitcloud 分布式网盘服务

用法:
  orbitcloud                         正常启动服务
  orbitcloud -initConfig              用内置默认配置(embed)强制覆盖生成 ./config.yaml 后退出
  orbitcloud --add-superadmin <username> <password>   添加超级管理员(权限 0)后退出
  orbitcloud --version               打印版本号后退出
  orbitcloud --help                  打印本帮助后退出

配置文件:工作目录 ./config.yaml(不存在时自动用内置默认配置生成; -initConfig 可强制覆盖)
`

// RunPreInit 处理无需任何初始化的指令,必须在 appinit 初始化之前调用(命中则执行后退出):
// -initConfig 用内置默认配置(embed)强制覆盖写入 ./config.yaml。
// 不能并入 Run:Run 在 appinit 初始化之后执行,而 appinit 会先读配置并初始化 DB
// (不可用会持续重试);若配置缺失/损坏,-initConfig 将永远轮不到执行。
func RunPreInit(args []string) error {
	if len(args) == 0 {
		return nil // 无指令 → 继续正常启动流程
	}

	switch args[0] {
	case "-initConfig", "--initConfig", "--" + CmdInitConfig:
		if err := config.WriteDefaultConfigFile("./config.yaml"); err != nil {
			return err
		}
		fmt.Println("默认配置已强制写入 ./config.yaml(内置 embed 配置),请按需修改后重启")
		return ErrExit
	default:
		return nil // 未命中 → 继续正常启动流程
	}
}

// Run 解析命令行参数并执行命中的指令;未命中任何指令 → 返回 nil,继续正常启动流程。
// 由 main 在 appinit(配置/DB 初始化)之后调用——add-superadmin 需要 DB。
func Run(args []string) error {
	if len(args) == 0 {
		return nil // 无指令 → 正常启动
	}

	switch args[0] {
	case "--version":
		version := "unknown"
		if core.GlobalConfig != nil {
			version = core.GlobalConfig.App.Version
		}
		fmt.Printf("orbitcloud version %s\n", version)
		return ErrExit

	case "--help", "-h":
		fmt.Print(usageText)
		return ErrExit

	case "--" + CmdAddSuperAdmin:
		if len(args) < 3 {
			fmt.Print(usageText)
			return fmt.Errorf("flag: --add-superadmin requires <username> <password>")
		}
		return addSuperAdmin(args[1], args[2])

	default:
		return nil // 未命中 → 继续正常启动流程
	}
}

// addSuperAdmin 直接经 core.DB 创建权限 0 用户(绕过 server.Register 的权限归一)。
func addSuperAdmin(username, password string) error {
	name := strings.TrimSpace(username)
	if name == "" || password == "" || len(password) < 8 {
		return fmt.Errorf("flag: add-superadmin: username required, password must be at least 8 chars")
	}

	// 已存在同名用户 → 报错退出
	var count int64
	if err := core.DB.Model(&model.User{}).Where("username = ?", name).Count(&count).Error; err != nil {
		return fmt.Errorf("flag: add-superadmin: query user: %w", err)
	}
	if count > 0 {
		return fmt.Errorf("flag: add-superadmin: user %q already exists", name)
	}

	// bcrypt 哈希密码;PermissionLevel 0 = 超级管理员;Status = 1
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("flag: add-superadmin: hash password: %w", err)
	}
	// 用 map 落库:permission_level 字段 gorm tag 带 default:1,对零值(0)字段的
	// struct Create 会以默认值 1 填充;map Create 保留显式 0
	if err := core.DB.Model(&model.User{}).Create(map[string]any{
		"username":         name,
		"password":         string(hash),
		"name":             name,
		"permission_level": int8(0),
		"status":           1,
	}).Error; err != nil {
		return fmt.Errorf("flag: add-superadmin: create user: %w", err)
	}

	fmt.Printf("superadmin %q created (permission level 0)\n", name)
	return ErrExit
}

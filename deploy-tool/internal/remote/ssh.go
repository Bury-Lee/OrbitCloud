// ssh.go —— 原生 SSH 执行器(参考 tem/sshbox 实现: golang.org/x/crypto/ssh + pkg/sftp)。
// 定位: 工具默认通道,不再依赖外部 sshbox.exe;配置(host/user/pass/port)仍兼容 sshbox.json。
package remote

import (
	"errors"
	"io"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"deploy-tool/internal/model"
)

// NativeExecer 基于 golang.org/x/crypto/ssh 的原生执行器。
// 特性: 连接懒建立 + 复用 + 断线自动重连;单命令超时 sshTimeout;输出经 Sanitize 脱敏。
type NativeExecer struct {
	mu     sync.Mutex
	cfg    *model.ToolConfig
	client *ssh.Client
}

// NewNativeExecer 构造原生执行器。
// 参数: cfg *model.ToolConfig(Host 必填;User/Pass 为空时从 sshbox.json 合并)
// 返回: *NativeExecer; err 配置缺失
func NewNativeExecer(cfg *model.ToolConfig) (*NativeExecer, error) {
	if cfg == nil {
		return nil, errors.New("配置为空")
	}
	// 与 sshbox 相同规则: Host 为空时合并 sshbox.json 的连接参数
	if strings.TrimSpace(cfg.Host) == "" {
		mergeFromSSHBoxJSON(cfg)
	}
	if strings.TrimSpace(cfg.Host) == "" {
		return nil, errors.New("服务器地址未配置: 请到设置页填写 Host(或提供 sshbox.json)")
	}
	if strings.TrimSpace(cfg.User) == "" {
		return nil, errors.New("SSH 用户名未配置")
	}
	return &NativeExecer{cfg: cfg}, nil
}

// connect 获取可用连接: 懒建立;已缓存连接先做 keepalive 探活,失败则重连(内部)。
func (n *NativeExecer) connect() (*ssh.Client, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.client != nil {
		_, _, err := n.client.SendRequest("keepalive@openssh.com", true, nil)
		if err == nil {
			return n.client, nil
		}
		_ = n.client.Close()
		n.client = nil
	}

	port := n.cfg.Port
	if port == 0 {
		port = 22
	}
	cc := &ssh.ClientConfig{
		User: n.cfg.User,
		Auth: []ssh.AuthMethod{ssh.Password(n.cfg.Pass)},
		// 部署运维工具场景: 接受任意主机密钥(与 sshbox 一致,凭据复用 sshbox.json)
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}
	addr := net.JoinHostPort(n.cfg.Host, strconv.Itoa(port))
	client, err := ssh.Dial("tcp", addr, cc)
	if err != nil {
		return nil, fmt.Errorf("SSH 连接 %s 失败: %v", addr, err)
	}
	n.client = client
	return client, nil
}

// Close 关闭连接。
func (n *NativeExecer) Close() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.client != nil {
		_ = n.client.Close()
		n.client = nil
	}
	return nil
}

// (NativeExecer) Exec 执行命令(输出脱敏)。
func (n *NativeExecer) Exec(cmd string) (string, int, error) {
	out, code, err := n.execRaw(cmd)
	return Sanitize(out), code, err
}

// (NativeExecer) ExecRaw 执行命令(不做脱敏,供 yaml 解析等场景)。
func (n *NativeExecer) ExecRaw(cmd string) (string, int, error) {
	return n.execRaw(cmd)
}

// execRaw 白名单 + 超时执行(内部公共逻辑)。
func (n *NativeExecer) execRaw(cmd string) (string, int, error) {
	ok := false
	for _, p := range allowedPrefixes {
		if strings.HasPrefix(cmd, p) {
			ok = true
			break
		}
	}
	if !ok {
		return "", -1, fmt.Errorf("命令不在只读白名单,已拒绝: %s", truncate(cmd, 60))
	}

	client, err := n.connect()
	if err != nil {
		return "", -1, err
	}
	session, err := client.NewSession()
	if err != nil {
		n.dropClient()
		return "", -1, err
	}
	defer session.Close()

	type res struct {
		out  string
		code int
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		out, err := session.CombinedOutput(cmd)
		if err != nil {
			if ee, ok := err.(*ssh.ExitError); ok {
				ch <- res{out: string(out), code: ee.ExitStatus()}
				return
			}
			ch <- res{err: err}
			return
		}
		ch <- res{out: string(out)}
	}()

	select {
	case r := <-ch:
		if r.err != nil {
			n.dropClient()
			return "", -1, r.err
		}
		return r.out, r.code, nil
	case <-time.After(sshTimeout):
		_ = session.Close()
		n.dropClient()
		return "", -1, errors.New("远程执行超时(60s)")
	}
}

// (NativeExecer) Fetch 取回远端文件(sftp)。
// 参数: remotePath 远端绝对路径(仅允许部署目录/家目录下文件)
// 返回: content 文件文本; err 取回失败
func (n *NativeExecer) Fetch(remotePath string) (string, error) {
	lower := strings.ToLower(remotePath)
	allow := []string{strings.ToLower(n.cfg.DeployedPath), "/home/"}
	ok := false
	for _, p := range allow {
		if strings.HasPrefix(lower, p) {
			ok = true
			break
		}
	}
	if !ok || strings.Contains(remotePath, "..") {
		return "", fmt.Errorf("路径越界,已拒绝: %s", truncate(remotePath, 80))
	}

	client, err := n.connect()
	if err != nil {
		return "", err
	}
	c, err := sftp.NewClient(client)
	if err != nil {
		n.dropClient()
		return "", err
	}
	defer c.Close()

	data, err := readSFTPFile(c, remotePath)
	if err != nil {
		return "", fmt.Errorf("sftp 读取 %s 失败: %v", truncate(remotePath, 80), err)
	}
	return string(data), nil
}

// readSFTPFile 读取远端文件全文(内部)。
func readSFTPFile(c *sftp.Client, path string) ([]byte, error) {
	f, err := c.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// (NativeExecer) Ping 连通性测试。
func (n *NativeExecer) Ping() error {
	out, code, err := n.Exec("uname -a")
	if err != nil {
		return err
	}
	if code != 0 || strings.TrimSpace(out) == "" {
		return errors.New("远程执行 uname 失败")
	}
	return nil
}

// dropClient 断开并清空缓存连接(内部,出错时调用)。
func (n *NativeExecer) dropClient() {
	n.mu.Lock()
	if n.client != nil {
		_ = n.client.Close()
		n.client = nil
	}
	n.mu.Unlock()
}

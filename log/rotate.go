package log

// 日志文件轮转 writer(io.Writer):
//   - 目录结构:{dir}/{YYYY-MM-DD}/{prefix}-{HH-MM-SS}.log(按日期建子文件夹);
//   - 自动切换条件:
//     ① 日期变化 → 切换到新日期子文件夹,开启新文件;
//     ② 单文件累计写入超过 maxSizeKB(>0 时生效)→ 同文件夹内新建文件继续写(文件名取当前 时-分-秒)。
//   - 并发安全(内部 mutex 串行化 Write/rotate)。

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// rotateWriter 可轮转的日志文件 writer。
type rotateWriter struct {
	mu      sync.Mutex
	dir     string           // 日志根目录(日期子文件夹的父目录)
	prefix  string           // 文件名前缀(不含扩展名)
	maxSize int64            // 单文件大小上限(字节;<=0 表示不按大小切换,仅按日期切换)
	now     func() time.Time // 时间源(测试可注入)
	file    *os.File
	size    int64  // 当前文件已写入字节数
	curDay  string // 当前文件所属日期 YYYY-MM-DD
}

// NewRotateWriter 构造轮转文件 writer(出错时返回 error,由调用方决定回退策略)。
// outputPath 两种语义:
//   - 带扩展名(如 ./logs/orbitcloud.log):目录 = 其父目录,前缀 = 去扩展名的文件名;
//   - 无扩展名(视为目录):目录 = 其自身,前缀 = "orbitcloud"。
//
// maxSizeKB 为单文件大小上限(单位 KB;<=0 表示不按大小切换)。
func NewRotateWriter(outputPath string, maxSizeKB int64) (io.Writer, error) {
	dir := outputPath
	prefix := "orbitcloud"
	if ext := filepath.Ext(outputPath); ext != "" {
		dir = filepath.Dir(outputPath)
		prefix = strings.TrimSuffix(filepath.Base(outputPath), ext)
	}
	if strings.TrimSpace(dir) == "" {
		dir = "."
	}
	return &rotateWriter{
		dir:     dir,
		prefix:  prefix,
		maxSize: maxSizeKB * 1024,
		now:     time.Now,
	}, nil
}

// Write 实现 io.Writer:写入前检查是否需要轮转(日期变化 / 大小超限)。
func (w *rotateWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	now := w.now()
	if w.file == nil || now.Format("2006-01-02") != w.curDay {
		if err := w.rotate(now); err != nil {
			return 0, err
		}
	}
	if w.maxSize > 0 && w.size+int64(len(p)) > w.maxSize {
		if err := w.rotate(now); err != nil {
			return 0, err
		}
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

// rotate 关闭旧文件并打开新文件(按 now 的日期/时分秒命名)。
// 同一秒内重复轮转(或同秒重启)时追加 .1/.2 序号,避免 O_APPEND 混写同一文件。
func (w *rotateWriter) rotate(now time.Time) error {
	if w.file != nil {
		_ = w.file.Close()
		w.file = nil
	}

	day := now.Format("2006-01-02")
	dayDir := filepath.Join(w.dir, day)
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		return fmt.Errorf("rotate: mkdir %s: %w", dayDir, err)
	}

	stamp := now.Format("15-04-05")
	path := filepath.Join(dayDir, fmt.Sprintf("%s-%s.log", w.prefix, stamp))
	for i := 1; fileExists(path); i++ {
		path = filepath.Join(dayDir, fmt.Sprintf("%s-%s.%d.log", w.prefix, stamp, i))
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("rotate: open %s: %w", path, err)
	}
	w.file = f
	w.curDay = day
	if fi, err := f.Stat(); err == nil {
		w.size = fi.Size() // 文件已存在(同秒重启)时以实际大小为准
	} else {
		w.size = 0
	}
	return nil
}

// Close 关闭当前日志文件(进程退出前可调用;轮转切换时旧文件在 rotate 内自动关闭)。
func (w *rotateWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

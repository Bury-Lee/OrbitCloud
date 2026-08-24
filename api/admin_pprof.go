// admin_pprof.go —— pprof 管理接口(仅管理员)。
//
// 设计(按需采样,避免常驻开销):
//   - CPU 火焰图:POST /api/v1/admin/pprof/cpu?seconds=30,管理员主动触发,
//     当场采样 N 秒(默认 30,上限 300,并发互斥),完成后落盘为 pprof 文件;
//   - 内存/协程/阻塞/锁快照:GET /api/v1/admin/pprof/{heap,goroutine,block,mutex},
//     快照式当场抓取(无常驻采样损耗,可随时调用排查内存泄漏);
//   - 采样文件作为"日志文件"保存(默认 <log.output_path>/pprof/,等价 config.pprof_dir),
//     通过管理员接口列表查看 / 下载,文件名白名单防路径穿越。
package api

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime/pprof"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"orbitcloud/common"
	"orbitcloud/core"
	"orbitcloud/log"
	"orbitcloud/server"
)

// pprofCPU 采样文件命名:pprof-cpu-20060102-150405.pprof
var pprofFilePattern = regexp.MustCompile(`^pprof-(cpu|heap|goroutine|block|mutex)-[0-9]{8}-[0-9]{6}\.pprof$`)

// cpuMu CPU 采样互斥:同一时刻只允许一个采样任务(采样期间占用采样器)。
var cpuMu sync.Mutex

// pprofEnabled pprof 管理接口总开关(config server.pprof_enabled)。
func pprofEnabled() bool {
	return core.GlobalConfig != nil && core.GlobalConfig.Server.PprofEnabled
}

// pprofDir 采样文件保存目录:config.pprof_dir > <log.output_path>/pprof > ./logs/pprof。
func pprofDir() string {
	cfg := core.GlobalConfig
	if cfg != nil {
		if d := strings.TrimSpace(cfg.Server.PprofDir); d != "" {
			return d
		}
		if d := strings.TrimSpace(cfg.Log.OutputPath); d != "" {
			return filepath.Join(d, "pprof")
		}
	}
	return "logs/pprof"
}

// ensurePprofDir 确保采样目录存在并返回。
func ensurePprofDir() string {
	dir := pprofDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Errorf("pprof: mkdir %s: %v", dir, err)
	}
	return dir
}

// pprofSnapshot 抓取指定 profile 快照并落盘为日志文件。
func pprofSnapshot(c *gin.Context, profile string) {
	if !pprofEnabled() {
		respondError(c, server.ErrForbidden)
		return
	}
	p := pprof.Lookup(profile)
	if p == nil {
		respondError(c, server.ErrInvalidInput)
		return
	}
	path := filepath.Join(ensurePprofDir(),
		fmt.Sprintf("pprof-%s-%s.pprof", profile, time.Now().Format("20060102-150405")))
	f, err := os.Create(path)
	if err != nil {
		log.Errorf("pprof: create %s: %v", path, err)
		respondError(c, err)
		return
	}
	if err := p.WriteTo(f, 0); err != nil {
		f.Close()
		log.Errorf("pprof: write %s: %v", path, err)
		respondError(c, err)
		return
	}
	f.Close()
	log.Infof("pprof: %s 快照已保存 %s", profile, path)
	commonSuccessPprofFile(c, path)
}

// commonSuccessPprofFile 以附件形式返回采样文件(保留本地副本作日志文件)。
func commonSuccessPprofFile(c *gin.Context, path string) {
	c.FileAttachment(path, filepath.Base(path))
}

// PprofAPI pprof 管理接口。
type PprofAPI struct{}

// Cpu 主动触发 CPU 采样(POST /api/v1/admin/pprof/cpu?seconds=30)。
// 采样期间并发互斥;完成后文件落盘并返回下载。
func (PprofAPI) Cpu(c *gin.Context) {
	if !pprofEnabled() {
		respondError(c, server.ErrForbidden)
		return
	}
	seconds := queryInt(c, "seconds", 30)
	if seconds < 1 {
		seconds = 1
	}
	if seconds > 300 {
		seconds = 300
	}
	if !cpuMu.TryLock() {
		common.Error(c, 409, "已有 CPU 采样进行中,请稍后再试")
		return
	}
	defer cpuMu.Unlock()

	path := filepath.Join(ensurePprofDir(),
		fmt.Sprintf("pprof-cpu-%s.pprof", time.Now().Format("20060102-150405")))
	f, err := os.Create(path)
	if err != nil {
		log.Errorf("pprof: create %s: %v", path, err)
		respondError(c, err)
		return
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		f.Close()
		log.Errorf("pprof: start cpu profile: %v", err)
		respondError(c, err)
		return
	}
	log.Infof("pprof: CPU 采样开始(%d 秒)-> %s", seconds, path)
	time.Sleep(time.Duration(seconds) * time.Second)
	pprof.StopCPUProfile()
	f.Close()
	log.Infof("pprof: CPU 采样完成 %s", path)
	commonSuccessPprofFile(c, path)
}

// Heap 内存快照(GET /api/v1/admin/pprof/heap;排查内存泄漏主用)。
func (PprofAPI) Heap(c *gin.Context)    { pprofSnapshot(c, "heap") }
func (PprofAPI) Goroutine(c *gin.Context) { pprofSnapshot(c, "goroutine") }
func (PprofAPI) Block(c *gin.Context)   { pprofSnapshot(c, "block") }
func (PprofAPI) Mutex(c *gin.Context)   { pprofSnapshot(c, "mutex") }

// List 列出已保存的 pprof 采样文件(日志文件,G 管理员可查看)。
func (PprofAPI) List(c *gin.Context) {
	if !pprofEnabled() {
		respondError(c, server.ErrForbidden)
		return
	}
	dir := ensurePprofDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		common.Success(c, gin.H{"dir": dir, "files": []gin.H{}})
		return
	}
	files := make([]gin.H, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !pprofFilePattern.MatchString(e.Name()) {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		files = append(files, gin.H{
			"name": e.Name(), "size": info.Size(),
			"mod_time": info.ModTime().Format("2006-01-02 15:04:05"),
		})
	}
	common.Success(c, gin.H{"dir": dir, "files": files})
}

// Download 下载指定采样文件(文件名白名单,防路径穿越)。
func (PprofAPI) Download(c *gin.Context) {
	if !pprofEnabled() {
		respondError(c, server.ErrForbidden)
		return
	}
	name := filepath.Base(c.Query("file"))
	if name == "." || name == "" || !pprofFilePattern.MatchString(name) {
		respondError(c, server.ErrInvalidInput)
		return
	}
	path := filepath.Join(pprofDir(), name)
	if _, err := os.Stat(path); err != nil {
		respondError(c, server.ErrNotFound)
		return
	}
	log.Infof("pprof: 下载采样文件 %s", path)
	commonSuccessPprofFile(c, path)
}

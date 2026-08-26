package smbgateway

// SMB 网关日志统一封装:全部日志显式带 "[SMB]" 组件标签(与 Web 端 "[Web]" 区分)。
// 规则:
//   - 读操作(Read/GetRange/Stat/列目录)不产生 Info 级日志(仅保留 Debug,不进入日志库);
//   - 每次文件变更(建/写回/删除/重命名/关闭即删)在完成点仅记一条 Info 汇总;
//   - 原有 Debug 级日志全部保留(排查用)。
//
// 入参/返回值与 log 包同名函数一致,仅附加 ["SMB"] 标签。

import "orbitcloud/log"

// smbDebugf 带 [SMB] 标签的 Debugf(原有调试日志,全部保留)。
func smbDebugf(format string, args ...any) error {
	return log.DebugfP(format, []string{"SMB"}, args...)
}

// smbInfof 带 [SMB] 标签的 Infof(文件变更汇总等关键业务日志)。
func smbInfof(format string, args ...any) error {
	return log.InfofP(format, []string{"SMB"}, args...)
}

// smbWarnf 带 [SMB] 标签的 Warnf。
func smbWarnf(format string, args ...any) error {
	return log.WarnfP(format, []string{"SMB"}, args...)
}

// smbErrorf 带 [SMB] 标签的 Errorf。
func smbErrorf(format string, args ...any) error {
	return log.ErrorfP(format, []string{"SMB"}, args...)
}

package common

// 通用参数校验(供 server 层落库前防御;校验失败统一返回哨兵错误,api 层映射 400)。

import (
	"strings"
	"unicode/utf8"
)

// 路径字段长度上限:完整路径 ≤500(NormalizeDirPath 校验;另有 MaxDirPathLen=512 见 path.go);
// 条目名 ≤255(Name varchar(255))。
const (
	maxDirPathLen  = 500
	maxItemNameLen = 255
)

// NormalizeDirPath 规范化父目录路径(业务层过滤,落库前统一):
//   - 空串或 "/" → "/"(桶根);
//   - 其余:按 "/" 分段,拒绝空段 / "." / ".." 段(防路径穿越与歧义),重拼为
//     无首尾斜杠、段间单斜杠的段序列(如 "科室/影像");
//   - 超长(> 500 字符)→ ErrInvalidInput。
//
// 供 UploadFile/CreateDir/MoveFile 等所有写路径入口调用——"dir/sub" 与 "dir/sub/"
// 不归一化会被当成两个不同目录。
func NormalizeDirPath(p string) (string, error) {
	if p == "" || p == "/" {
		return "/", nil
	}
	segs := strings.Split(strings.Trim(p, "/"), "/")
	var b strings.Builder
	for _, s := range segs {
		if s == "" || s == "." || s == ".." {
			return "", ErrInvalidInput
		}
		if b.Len() > 0 {
			b.WriteByte('/')
		}
		b.WriteString(s)
	}
	out := b.String()
	if utf8.RuneCountInString(out) > maxDirPathLen {
		return "", ErrInvalidInput
	}
	return out, nil
}

// ValidateItemName 校验条目名(Name,文件与文件夹共用):
//   - 空串 → ErrInvalidInput;
//   - 含 "/" 或 "\\" → ErrInvalidInput(Name 是单段,不允许路径分隔符);
//   - 超过 255 字符 → ErrInvalidInput;
//   - Windows 禁止字符:控制字符、`\ : * ? " < > |`、结尾点/空格、保留设备名
//     (CON/PRN/AUX/NUL/COM1-9/LPT1-9,含带扩展名形式如 CON.txt)。
func ValidateItemName(name string) error {
	if name == "" {
		return ErrInvalidInput
	}
	if strings.ContainsAny(name, `/\`) {
		return ErrInvalidInput
	}
	if utf8.RuneCountInString(name) > maxItemNameLen {
		return ErrInvalidInput
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7F { // 控制字符(含 DEL)
			return ErrInvalidInput
		}
		if strings.ContainsRune(`:*?"<>|`, r) {
			return ErrInvalidInput
		}
	}
	// Windows 禁止结尾点/空格(资源管理器无法创建/访问;含全点/全空格名)
	if strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return ErrInvalidInput
	}
	// Windows 保留设备名(不区分大小写;去扩展名后判定,CON.txt 同样保留)
	base := name
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	switch strings.ToUpper(base) {
	case "CON", "PRN", "AUX", "NUL":
		return ErrInvalidInput
	}
	if len(base) == 4 && (base[0] == 'C' || base[0] == 'c' || base[0] == 'L' || base[0] == 'l') {
		up := strings.ToUpper(base)
		if (strings.HasPrefix(up, "COM") || strings.HasPrefix(up, "LPT")) && up[3] >= '1' && up[3] <= '9' {
			return ErrInvalidInput
		}
	}
	return nil
}

// JoinItemPath 拼接完整逻辑路径:桶根下直接返回 Name,否则 FilePath + "/" + Name。
func JoinItemPath(dirPath, name string) string {
	if dirPath == "" || dirPath == "/" {
		return name
	}
	return dirPath + "/" + name
}

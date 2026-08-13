package common

// validate_test.go —— ValidateItemName 校验测试。

import "testing"

func TestValidateItemNameOK(t *testing.T) {
	valid := []string{
		"report.txt",
		"Report.txt", // 大小写允许(唯一性由 name_lower 兜底,见 P0-2)
		"a b.txt",
		"2026-08-08 报告.pdf",
		"中文文件名",
		"a", "1", "-", "_", "~",
		"file.with.dots.txt",
		"COM10", "LPT0", // 非保留设备名(仅 1-9 保留)
		"conma", "NULx", // 非精确命中
	}
	for _, name := range valid {
		if err := ValidateItemName(name); err != nil {
			t.Errorf("ValidateItemName(%q) = %v, want nil", name, err)
		}
	}
}

func TestValidateItemNameReject(t *testing.T) {
	invalid := []string{
		"",         // 空
		"a/b",      // 路径分隔符
		`a\b`,      // 反斜杠
		"a\x01b",   // 控制字符
		"a\x1fb",   // 控制字符(单位分隔符)
		"a\x7fb",   // DEL
		"a:b", "a*b", "a?b", `a"b`, "a<b", "a>b", "a|b", // Windows 保留符号
		"a.", "a ", "..", "...", ". ", // 结尾点/空格
		"CON", "con", "con.txt", "CON.TXT", // 保留设备名(含扩展名形式)
		"PRN", "AUX", "NUL",
		"COM1", "com3", "COM9", "LPT1", "lpt8",
	}
	long := ""
	for i := 0; i < 300; i++ {
		long += "a"
	}
	invalid = append(invalid, long)
	for _, name := range invalid {
		if err := ValidateItemName(name); err == nil {
			t.Errorf("ValidateItemName(%q) = nil, want error", name)
		}
	}
}

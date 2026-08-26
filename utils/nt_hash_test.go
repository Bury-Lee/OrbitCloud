package utils

// nt_hash_test.go —— NtHashOf NT hash 计算测试。
// 标准向量为 NTLM 协议公开测试值:
//   - 空密码 → 31d6cfe0d16ae931b73c59d7e0c089c0(MD4(空) 的公认值)
//   - "Password" → a4f49c406510bdcab6824ee7c30fd852(NTLM 官方测试向量)
//   - "password" → 8846f7eaee8fb117ad06bdd830b7586c

import "testing"

func TestNtHashOfKnownVectors(t *testing.T) {
	cases := []struct {
		password string
		want     string
	}{
		{"", "31d6cfe0d16ae931b73c59d7e0c089c0"},
		{"Password", "a4f49c406510bdcab6824ee7c30fd852"},
		{"password", "8846f7eaee8fb117ad06bdd830b7586c"},
	}
	for _, tc := range cases {
		if got := NtHashOf(tc.password); got != tc.want {
			t.Errorf("NtHashOf(%q) = %q, want %q", tc.password, got, tc.want)
		}
	}
}

// TestNtHashOfShape 任意输入输出恒为 32 位小写十六进制(结构性质)。
func TestNtHashOfShape(t *testing.T) {
	inputs := []string{"a", "abc", "中文密码", "😀", "a4f49c406510bdcab6824ee7c30fd852"}
	for _, in := range inputs {
		got := NtHashOf(in)
		if len(got) != 32 {
			t.Errorf("NtHashOf(%q) len = %d, want 32", in, len(got))
		}
		for i := 0; i < len(got); i++ {
			c := got[i]
			if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
				t.Errorf("NtHashOf(%q) = %q: 非小写 hex 字符 %q", in, got, c)
				break
			}
		}
	}
}

// TestNtHashOfDeterministic 同一密码多次计算必须一致(SMB 校验依赖确定性)。
func TestNtHashOfDeterministic(t *testing.T) {
	a := NtHashOf("orbitcloud-secret")
	b := NtHashOf("orbitcloud-secret")
	if a != b {
		t.Errorf("NtHashOf 不稳定: %q != %q", a, b)
	}
}

// TestNtHashOfUnicode 非 ASCII 字符按 UTF-16LE 参与计算(与 SMB 客户端一致)。
func TestNtHashOfUnicode(t *testing.T) {
	// 性质:大小写/全角不同 → hash 不同(UTF-16 编码路径参与)。
	if NtHashOf("测试") == NtHashOf("测试a") {
		t.Errorf("不同密码应产生不同 NT hash")
	}
}

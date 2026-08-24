// nt_hash.go —— 密码的 NT hash 计算(SMB NTLM 校验用)。
//
// NT hash = MD4(UTF-16LE(明文密码)) 的 hex。
// users 表同时存 bcrypt(HTTP 登录)与 nt_hash(SMB 登录):
//   - bcrypt 不可逆,无法反推 NT hash,故密码设置/修改时必须由明文
//     额外计算 nt_hash 落库;
//   - nt_hash 仅经 SMB 网关私有通道下发(Rust 侧 NTLM 校验),HTTP 永不返回。
package utils

import (
	"unicode/utf16"

	"golang.org/x/crypto/md4"
)

// NtHashOf 计算密码的 NT hash。
// 参数:plainPassword 明文密码。
// 返回值:NT hash 十六进制(32 字符,小写)。
func NtHashOf(plainPassword string) string {
	units := utf16.Encode([]rune(plainPassword))
	le := make([]byte, 0, len(units)*2)
	for _, u := range units {
		le = append(le, byte(u), byte(u>>8))
	}
	h := md4.New()
	h.Write(le)
	return hexString(h.Sum(nil))
}

func hexString(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, v := range b {
		out = append(out, digits[v>>4], digits[v&0x0f])
	}
	return string(out)
}

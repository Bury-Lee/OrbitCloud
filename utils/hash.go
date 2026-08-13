package utils

// 架构无关的工具函数,例如哈希,随机,字符串解析等

import (
	"crypto/md5"
	"encoding/hex"
	"io"
	"os"

	"orbitcloud/common"
)

// ValidateS3BucketName 校验桶名符合 S3 bucket 命名规范:
//   - 长度 3~63 字符;
//   - 仅小写字母 / 数字 / 连字符(-)(不允许点,天然排除 IPv4 形式);
//   - 首尾必须为字母或数字。
// 供 server.CreateBucket 在落库前校验,避免"创建成功但实体读写失败";
// 成功 → nil,失败 → common.ErrInvalidInput(api 层映射 400)。
func ValidateS3BucketName(name string) error {
	n := len(name)
	if n < 3 || n > 63 {
		return common.ErrInvalidInput
	}
	if !isS3Alnum(name[0]) || !isS3Alnum(name[n-1]) {
		return common.ErrInvalidInput
	}
	for i := 0; i < n; i++ {
		c := name[i]
		if !isS3Alnum(c) && c != '-' {
			return common.ErrInvalidInput
		}
	}
	return nil
}

// isS3Alnum 小写字母或数字。
func isS3Alnum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
}

// ComputeSampleMD5 计算文件采样 MD5:头部 1MB + 中间 1MB + 尾部 1MB 拼接后计算,
// 文件总大小 ≤ 3MB 时全量参与。
// 输入流若实现 io.ReadSeeker(如 *os.File)→ 直接定位采样,零额外拷贝;
// 否则流式读入临时文件(磁盘缓冲,不整读内存),采样后自动清理。
// 返回 (md5Hex 小写 32 位, 文件总字节数 size, err);空文件返回 d41d8cd98f00b204e9800998ecf8427e。
func ComputeSampleMD5(r io.Reader) (string, int64, error) {
	const sample = 1 << 20 // 1MB
	emptyMD5 := "d41d8cd98f00b204e9800998ecf8427e"

	// 1. 确定可 Seek 的数据源(不可 Seek → 落临时文件)
	rs, ok := r.(io.ReadSeeker)
	if !ok {
		f, err := os.CreateTemp("", "orbitcloud-md5-*")
		if err != nil {
			return "", 0, err
		}
		defer os.Remove(f.Name())
		defer f.Close()
		if _, err := io.Copy(f, r); err != nil {
			return "", 0, err
		}
		rs = f
	}

	// 2. 取总大小并回到开头
	size, err := rs.Seek(0, io.SeekEnd)
	if err != nil {
		return "", 0, err
	}
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return "", 0, err
	}
	if size == 0 {
		return emptyMD5, 0, nil
	}

	h := md5.New()

	// 3. size <= 3MB → 全量参与
	if size <= 3*sample {
		if _, err := io.Copy(h, rs); err != nil {
			return "", 0, err
		}
		return hex.EncodeToString(h.Sum(nil)), size, nil
	}

	// 4. 否则取三段:head=[0,1MB); mid=[size/2, size/2+1MB); tail=[size-1MB, size)
	//    head
	if _, err := io.CopyN(h, rs, sample); err != nil {
		return "", 0, err
	}
	//    mid
	midOff := size / 2
	if _, err := rs.Seek(midOff, io.SeekStart); err != nil {
		return "", 0, err
	}
	if _, err := io.CopyN(h, rs, sample); err != nil {
		return "", 0, err
	}
	//    tail
	if _, err := rs.Seek(size-sample, io.SeekStart); err != nil {
		return "", 0, err
	}
	if _, err := io.CopyN(h, rs, sample); err != nil {
		return "", 0, err
	}

	return hex.EncodeToString(h.Sum(nil)), size, nil
}

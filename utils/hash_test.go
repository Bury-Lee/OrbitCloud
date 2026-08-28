package utils

// hash_test.go —— ValidateS3BucketName 桶名校验 + ComputeSampleMD5 采样哈希测试。

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"io"
	"strings"
	"testing"
)

func TestValidateS3BucketNameOK(t *testing.T) {
	valid := []string{
		"abc", "123", "a1b", // 长度下界 3
		"my-bucket", "a-b-c", "bucket-2026", "rustfs-data",
		"orbitcloud", strings.Repeat("a", 63), // 63 字符(长度上界)
	}
	for _, name := range valid {
		if err := ValidateS3BucketName(name); err != nil {
			t.Errorf("ValidateS3BucketName(%q) = %v, want nil", name, err)
		}
	}
}

func TestValidateS3BucketNameReject(t *testing.T) {
	long64 := strings.Repeat("a", 64)
	invalid := []string{
		"", "ab", "a", // 长度 < 3
		long64,          // 长度 > 63
		"ABC", "Bucket", // 大写
		"a.b", "my.bucket", // 点(IPv4 形式)
		"a_b", "my_bucket", // 下划线
		"-abc", "abc-", "a--", // 首/尾连字符
		"a b", "中文桶", // 空白/非 ASCII
		"ab\nc", // 控制字符
	}
	for _, name := range invalid {
		if err := ValidateS3BucketName(name); err == nil {
			t.Errorf("ValidateS3BucketName(%q) = nil, want error", name)
		}
	}
}

// ---------- ComputeSampleMD5 ----------

// sampleData 构造 size 字节的确定性数据(内容为 "0123456789abcdef" 循环)。
func sampleData(size int64) []byte {
	pattern := []byte("0123456789abcdef")
	data := make([]byte, size)
	for i := int64(0); i < size; i++ {
		data[i] = pattern[i%int64(len(pattern))]
	}
	return data
}

// expectedFullMD5 全量 MD5 期望值。
func expectedFullMD5(data []byte) string {
	sum := md5.Sum(data)
	return hex.EncodeToString(sum[:])
}

// TestComputeSampleMD5Empty 空文件 → 空内容 MD5(d41d8cd9...)。
func TestComputeSampleMD5Empty(t *testing.T) {
	got, size, err := ComputeSampleMD5(bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("ComputeSampleMD5(empty) err = %v", err)
	}
	if size != 0 {
		t.Errorf("size = %d, want 0", size)
	}
	const emptyMD5 = "d41d8cd98f00b204e9800998ecf8427e"
	if got != emptyMD5 {
		t.Errorf("md5 = %q, want %q", got, emptyMD5)
	}
}

// TestComputeSampleMD5Small 文件 ≤ 3MB(含恰好 3MB)全量参与,等价于标准 MD5。
func TestComputeSampleMD5Small(t *testing.T) {
	for _, size := range []int64{1, 17, 1 << 20, 3 << 20} {
		data := sampleData(size)
		got, gotSize, err := ComputeSampleMD5(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("size=%d err = %v", size, err)
		}
		if gotSize != size {
			t.Errorf("size=%d 返回 size = %d", size, gotSize)
		}
		if want := expectedFullMD5(data); got != want {
			t.Errorf("size=%d md5 = %q, want 全量 %q", size, got, want)
		}
	}
}

// TestComputeSampleMD5Large 文件 > 3MB 采样三段(head/mid/tail),与独立计算期望一致。
func TestComputeSampleMD5Large(t *testing.T) {
	const sample = 1 << 20
	for _, size := range []int64{3<<20 + 1, 4 << 20, 10<<20 + 12345} {
		data := sampleData(size)
		got, gotSize, err := ComputeSampleMD5(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("size=%d err = %v", size, err)
		}
		if gotSize != size {
			t.Errorf("size=%d 返回 size = %d", size, gotSize)
		}
		// 独立按采样契约拼三段计算期望,不照抄实现
		h := md5.New()
		h.Write(data[:sample]) // head
		mid := size / 2
		h.Write(data[mid : mid+sample]) // mid
		h.Write(data[size-sample:])     // tail
		want := hex.EncodeToString(h.Sum(nil))
		if got != want {
			t.Errorf("size=%d md5 = %q, want 采样 %q", size, got, want)
		}
	}
}

// nonSeeker 只实现 io.Reader,故意不实现 io.ReadSeeker(触发临时文件路径)。
type nonSeeker struct{ r *bytes.Reader }

func (n *nonSeeker) Read(p []byte) (int, error) { return n.r.Read(p) }

// TestComputeSampleMD5NonSeeker 不可 Seek 流(如 multipart 上传体)结果与可 Seek 一致。
func TestComputeSampleMD5NonSeeker(t *testing.T) {
	for _, size := range []int64{0, 17, 1 << 20, 3<<20 + 1} {
		data := sampleData(size)
		viaReader, _, err := ComputeSampleMD5(&nonSeeker{r: bytes.NewReader(data)})
		if err != nil {
			t.Fatalf("size=%d 非 Seek 路径 err = %v", size, err)
		}
		viaSeeker, _, err := ComputeSampleMD5(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("size=%d Seek 路径 err = %v", size, err)
		}
		if viaReader != viaSeeker {
			t.Errorf("size=%d: 非 Seek(%q) != Seek(%q)", size, viaReader, viaSeeker)
		}
	}
}

// TestComputeSampleMD5Sizes 采样路径必须报告真实文件大小(断点续传依赖)。
func TestComputeSampleMD5Sizes(t *testing.T) {
	for _, size := range []int64{0, 5, 2 << 20, 5 << 20} {
		_, got, err := ComputeSampleMD5(bytes.NewReader(sampleData(size)))
		if err != nil {
			t.Fatalf("size=%d err = %v", size, err)
		}
		if got != size {
			t.Errorf("size=%d 返回 %d", size, got)
		}
	}
}

var _ io.Reader = (*nonSeeker)(nil) // 编译期确认 nonSeeker 只承诺 Reader

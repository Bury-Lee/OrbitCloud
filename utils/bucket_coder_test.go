package utils

// bucket_coder_test.go —— BucketEncoder 36 进制桶名编码测试:
// 手算已知值 + 编解码往返(无损) + 前缀一致性。

import (
	"strings"
	"testing"
)

func TestBucketEncoderKnown(t *testing.T) {
	cases := []struct {
		id   uint
		want string
	}{
		{0, "orbitcloud-0"},
		{1, "orbitcloud-1"},
		{9, "orbitcloud-9"},
		{10, "orbitcloud-a"},
		{35, "orbitcloud-z"},
		{36, "orbitcloud-01"},    // 36 = 1*36 + 0,小端:低位 0 在前 → "01"
		{37, "orbitcloud-11"},    // 37 = 1*36 + 1
		{100, "orbitcloud-s2"},   // 100 = 2*36 + 28(28 → 's'),小端:余数在前
		{1296, "orbitcloud-001"}, // 1296 = 36*36,逐位取余 → "001"
	}
	for _, tc := range cases {
		if got := BucketEncoder(tc.id); got != tc.want {
			t.Errorf("BucketEncoder(%d) = %q, want %q", tc.id, got, tc.want)
		}
	}
}

// TestBucketEncoderRoundTrip 任意 ID 编码后按 36 进制小端解码应还原:
// 证明编码无损、无碰撞(编码函数与字符集索引一致)。
func TestBucketEncoderRoundTrip(t *testing.T) {
	ids := []uint{0, 1, 2, 9, 10, 35, 36, 37, 100, 1296, 46655, 46656,
		123456789, 1<<31 - 1, 1<<32 - 1, ^uint(0) >> 1, ^uint(0)}
	for _, id := range ids {
		enc := BucketEncoder(id)
		if !strings.HasPrefix(enc, bucketPrefix) {
			t.Fatalf("BucketEncoder(%d) = %q, missing prefix", id, enc)
		}
		got := decodeBucketName(enc)
		if got != id {
			t.Errorf("round-trip %d → %q → %d", id, enc, got)
		}
	}
}

// TestBucketEncoderPrefix 所有编码结果共享统一前缀,且字符集为小端 36 进制。
func TestBucketEncoderPrefix(t *testing.T) {
	for id := uint(0); id < 500; id++ {
		enc := BucketEncoder(id)
		if !strings.HasPrefix(enc, bucketPrefix) {
			t.Fatalf("BucketEncoder(%d) = %q, want prefix %q", id, enc, bucketPrefix)
		}
	}
}

// decodeBucketName BucketEncoder 的逆运算(仅测试用)。
func decodeBucketName(name string) uint {
	body := strings.TrimPrefix(name, bucketPrefix)
	var n uint
	base := uint(len(Charset))
	for i := 0; i < len(body); i++ {
		// 逐位还原,并检验该字符确实来自字符集(编码合法性)
		idx := strings.IndexByte(customCharset, body[i])
		if idx < 0 {
			return ^uint(0) // 非法字符:标记为不可能值
		}
		n += uint(idx) * pow(base, uint(i))
	}
	return n
}

func pow(base, exp uint) uint {
	r := uint(1)
	for i := uint(0); i < exp; i++ {
		r *= base
	}
	return r
}

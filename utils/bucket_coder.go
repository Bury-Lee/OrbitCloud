package utils

const (
	bucketPrefix = "orbitcloud-"
	// customCharset 桶名字符集:0~9(10 个)+ a~z(26 个)= 36 个字符 → 编码进制 base=36。
	// 顺序即索引:0~9 → 0..9,a~z → 10..35。
	customCharset = "0123456789abcdefghijklmnopqrstuvwxyz"
)

var Charset []byte = []byte(customCharset)

// BucketEncoder 将桶 ID 编码为对象存储 bucket 名(小端序:第一个字符是最低位)。
// 例:0 → "orbitcloud-0";10 → "orbitcloud-a";37 → "orbitcloud-11"(37 = 1*36 + 1)。
// 进制按实际字符集取 len(Charset);若未来增删字符,只需改 customCharset,编码自动跟随。
func BucketEncoder(bucketID uint) string {
	base := uint(len(Charset))

	if bucketID == 0 {
		return bucketPrefix + string(Charset[0])
	}

	result := []byte{}
	num := bucketID

	for num > 0 {
		remainder := num % base
		result = append(result, Charset[remainder]) //小端序存储:低位在前
		num = num / base
	}

	return bucketPrefix + string(result)
}

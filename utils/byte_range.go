package utils

// ByteRange 归一化后的字节区间,闭区间两端式 {Start, End},与 HTTP Range 头语义一致
// (不另造 offset+length 表示,音视频流服务/断点续传客户端天然携带这两端)。
type ByteRange struct {
	Start int64 // 起始字节下标(含,0 ≤ Start < size)
	End   int64 // 结束字节下标(含,Start ≤ End < size;End=size-1 = 读到文件尾)
}

// Length 区间字节数(派生值,End-Start+1;≥ 1)。
func (r *ByteRange) Length() int64 { return r.End - r.Start + 1 }

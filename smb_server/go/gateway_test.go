// gateway_test.go —— 传输层与路由层单元测试(真实现验证)。
package smbgateway

import (
	"bytes"         // 字节比较(流段断言)
	"context"       // 测试上下文
	"encoding/json" // JSON 反序列化(往返断言)
	"testing"       // 测试框架
)

// TestFrameHeaderRoundTrip 帧头编解码往返一致性。
// 验证:marshalFrameHeader → unmarshalFrameHeader 字段无损。
func TestFrameHeaderRoundTrip(t *testing.T) {
	hdr := FrameHeader{ // 构造样例帧头
		Magic:   Magic, // 魔数
		Version: Version, // 版本
		Flags:   FlagNeedReply, // 需响应标记
		MsgType: MSG_OPERATE, // 总操作类型
		Seq:     42, // 序列号
		BodyLen: 1234, // 长度
	}
	raw := marshalFrameHeader(hdr) // 编码为字节
	if len(raw) != headerLen { // 长度断言
		t.Fatalf("header length = %d, want %d", len(raw), headerLen) // 失败即终止
	}
	got, err := unmarshalFrameHeader(raw) // 解码回来
	if err != nil {
		t.Fatalf("unmarshal: %v", err) // 解码失败
	}
	if got != hdr { // 字段无损?
		t.Fatalf("round trip mismatch: got %+v want %+v", got, hdr) // 往返不一致
	}
}

// TestUnmarshalFrameHeaderRejectsBadMagic 坏魔数必须报错(协议硬契约)。
func TestUnmarshalFrameHeaderRejectsBadMagic(t *testing.T) {
	raw := marshalFrameHeader(FrameHeader{Magic: 0xDEADBEEF, Version: Version}) // 非法魔数帧头
	if _, err := unmarshalFrameHeader(raw); err == nil { // 必须报错
		t.Fatal("bad magic accepted") // 未报错 = 失败
	}
}

// TestMarshalFrameBodyLen 组帧时 BodyLen 自动回填。
func TestMarshalFrameBodyLen(t *testing.T) {
	frame := marshalFrame(FrameHeader{MsgType: MSG_OPERATE}, []byte("hello")) // 组帧(body 5 字节)
	if len(frame) != headerLen+5 { // 总长 = 16 + 5?
		t.Fatalf("frame length = %d, want %d", len(frame), headerLen+5) // 长度不符
	}
	hdr, err := unmarshalFrameHeader(frame[:headerLen]) // 解析帧头部分
	if err != nil {
		t.Fatal(err) // 解析失败
	}
	if hdr.BodyLen != 5 || hdr.MsgType != MSG_OPERATE { // 长度与类型回填正确?
		t.Fatalf("hdr = %+v", hdr) // 不符
	}
}

// TestOperateBodyRoundTrip 控制面 body 布局:[4B jsonLen] + JSON + 流段。
func TestOperateBodyRoundTrip(t *testing.T) {
	req := &OperateRequest{Code: CodeFileWrite, Write: &WriteArgs{HandleID: 7, Offset: 99}} // 写请求
	stream := []byte{1, 2, 3, 4} // 模拟流段
	body, err := marshalOperateBody(req, stream) // 编码
	if err != nil {
		t.Fatal(err) // 编码失败
	}
	jsonBytes, streamOut, err := splitOperateBody(body) // 拆分
	if err != nil {
		t.Fatal(err) // 拆分失败
	}
	if !bytes.Equal(streamOut, stream) { // 流段原样?
		t.Fatalf("stream mismatch: %v", streamOut) // 不符
	}
	var got OperateRequest // 解析目标
	if err := json.Unmarshal(jsonBytes, &got); err != nil { // JSON 段反序列化
		t.Fatal(err) // 失败
	}
	if got.Code != CodeFileWrite || got.Write == nil || got.Write.HandleID != 7 || got.Write.Offset != 99 { // 字段一致?
		t.Fatalf("json round trip mismatch: %+v", got) // 不符
	}
}

// TestSplitOperateBodyOverflow jsonLen 越界必须报错(硬契约)。
func TestSplitOperateBodyOverflow(t *testing.T) {
	// 构造 jsonLen 大于 body 剩余长度的坏帧。
	bad := []byte{0xFF, 0xFF, 0xFF, 0xFF, 0x00} // jsonLen=0xFFFFFFFF 远超实际
	if _, _, err := splitOperateBody(bad); err == nil { // 必须报错
		t.Fatal("overflow accepted") // 未报错 = 失败
	}
}

// mockHandler 测试用 OperateHandler:记录调用并返回固定结果。
type mockHandler struct {
	authUserCalls int // 记录 HandleAuthQueryUser 调用次数
	openCalls     int // 记录 HandleFileOpen 调用次数
}

func (m *mockHandler) HandleAuthQueryUser(_ context.Context, args *AuthUserArgs) (*AuthUserResult, error) {
	m.authUserCalls++ // 计数
	return &AuthUserResult{Found: true, Cred: &UserCred{Username: args.Username, NtHashHex: "abc"}}, nil // 固定返回
}
func (m *mockHandler) HandleAuthQueryAcl(_ context.Context, _ *AuthAclArgs) (*AuthAclResult, error) {
	return &AuthAclResult{Shares: []ShareInfo{}}, nil // 空清单
}
func (m *mockHandler) HandleAuthSnapshot(_ context.Context, _ *SnapshotArgs) (*SnapshotResult, error) {
	return &SnapshotResult{Users: []UserCred{}, Shares: []ShareInfo{}}, nil // 空快照
}
func (m *mockHandler) HandleFileOpen(_ context.Context, _ *OpenArgs) (*OpenResult, error) {
	m.openCalls++ // 计数
	return &OpenResult{HandleID: 1, EndOfFile: 10}, nil // 固定结果
}
func (m *mockHandler) HandleFileRead(_ context.Context, _ *ReadArgs) (*ReadResult, []byte, error) {
	return &ReadResult{Length: 2}, []byte{9, 9}, nil // 2 字节流段
}
func (m *mockHandler) HandleFileWrite(_ context.Context, _ *WriteArgs, _ []byte) (*WriteResult, error) {
	return &WriteResult{Written: 4}, nil // 写 4 字节
}
func (m *mockHandler) HandleFileFlush(_ context.Context, _ *FlushArgs) error { return nil } // 无结果
func (m *mockHandler) HandleFileStat(_ context.Context, _ *StatArgs) (*StatResult, error) {
	return &StatResult{Info: FileInfo{Name: "f", EndOfFile: 3}}, nil // 固定元信息
}
func (m *mockHandler) HandleFileSetTimes(_ context.Context, _ *SetTimesArgs) error { return nil } // 无结果
func (m *mockHandler) HandleFileTruncate(_ context.Context, _ *TruncateArgs) error { return nil } // 无结果
func (m *mockHandler) HandleFileListDir(_ context.Context, _ *ListDirArgs) (*ListDirResult, error) {
	return &ListDirResult{Entries: []FileInfo{}}, nil // 空条目
}
func (m *mockHandler) HandleFileClose(_ context.Context, _ *CloseArgs) error   { return nil } // 无结果
func (m *mockHandler) HandleFileUnlink(_ context.Context, _ *UnlinkArgs) error { return nil } // 无结果
func (m *mockHandler) HandleFileRename(_ context.Context, _ *RenameArgs) error { return nil } // 无结果

// TestRouteDispatch 正常路由:code 与指针匹配 → 调用对应 handler 方法。
func TestRouteDispatch(t *testing.T) {
	m := &mockHandler{} // 记录器
	req := &OperateRequest{Code: CodeAuthQueryUser, AuthUser: &AuthUserArgs{Username: "alice"}} // 合法请求
	resp, _, err := req.Route(context.Background(), m, nil) // 路由
	if err != nil {
		t.Fatal(err) // 路由错误
	}
	if resp.Err != nil { // 不应有业务错误
		t.Fatalf("unexpected err: %+v", resp.Err) // 有错误 = 失败
	}
	if resp.AuthUser == nil || !resp.AuthUser.Found || resp.AuthUser.Cred.Username != "alice" { // 结果正确?
		t.Fatalf("authUser result mismatch: %+v", resp.AuthUser) // 不符
	}
	if m.authUserCalls != 1 { // 恰好调用一次?
		t.Fatalf("handler calls = %d, want 1", m.authUserCalls) // 次数不符
	}
}

// TestRouteCodeZeroRejected Code=0(未填写)必须拒绝。
func TestRouteCodeZeroRejected(t *testing.T) {
	m := &mockHandler{} // 记录器
	req := &OperateRequest{} // code 默认 0
	resp, _, err := req.Route(context.Background(), m, nil) // 路由
	if err != nil {
		t.Fatal(err) // 不应有路由级错误(业务错误进 Err)
	}
	if resp.Err == nil || resp.Err.Code != ErrCodeBadRequest { // 应为坏请求?
		t.Fatalf("want ErrCodeBadRequest, got %+v", resp.Err) // 不符
	}
}

// TestRouteMultipleArgsRejected 多指针同时非 nil 必须拒绝(防误填)。
func TestRouteMultipleArgsRejected(t *testing.T) {
	m := &mockHandler{} // 记录器
	req := &OperateRequest{ // 构造误填请求
		Code:   CodeFileOpen, // 声明打开
		Open:   &OpenArgs{}, // 打开参数
		Unlink: &UnlinkArgs{}, // 误填:与 code 不匹配
	}
	resp, _, err := req.Route(context.Background(), m, nil) // 路由
	if err != nil {
		t.Fatal(err) // 不应有路由级错误
	}
	if resp.Err == nil || resp.Err.Code != ErrCodeBadRequest { // 应为坏请求?
		t.Fatalf("want ErrCodeBadRequest, got %+v", resp.Err) // 不符
	}
}

// TestRouteArgsMissing Code 有值但对应指针为 nil 必须拒绝。
func TestRouteArgsMissing(t *testing.T) {
	m := &mockHandler{} // 记录器
	req := &OperateRequest{Code: CodeFileOpen} // open 指针未填
	resp, _, err := req.Route(context.Background(), m, nil) // 路由
	if err != nil {
		t.Fatal(err) // 不应有路由级错误
	}
	if resp.Err == nil || resp.Err.Code != ErrCodeBadRequest { // 应为坏请求?
		t.Fatalf("want ErrCodeBadRequest, got %+v", resp.Err) // 不符
	}
}

// TestRouteUnknownCode 未知操作码 → NotImpl。
func TestRouteUnknownCode(t *testing.T) {
	m := &mockHandler{} // 记录器
	req := &OperateRequest{Code: 9999, Open: &OpenArgs{}} // 未知码
	resp, _, err := req.Route(context.Background(), m, nil) // 路由
	if err != nil {
		t.Fatal(err) // 不应有路由级错误
	}
	if resp.Err == nil || resp.Err.Code != ErrCodeNotImpl { // 应为未实现?
		t.Fatalf("want ErrCodeNotImpl, got %+v", resp.Err) // 不符
	}
}

// TestRouteReadReturnsStream Read 操作必须带回流数据段。
func TestRouteReadReturnsStream(t *testing.T) {
	m := &mockHandler{} // 记录器
	req := &OperateRequest{Code: CodeFileRead, Read: &ReadArgs{HandleID: 1, Offset: 0, Length: 2}} // 读请求
	resp, streamOut, err := req.Route(context.Background(), m, nil) // 路由
	if err != nil {
		t.Fatal(err) // 不应有路由级错误
	}
	if resp.Read == nil || resp.Read.Length != 2 { // 读结果正确?
		t.Fatalf("read result mismatch: %+v", resp.Read) // 不符
	}
	if !bytes.Equal(streamOut, []byte{9, 9}) { // 流段透传?
		t.Fatalf("stream out mismatch: %v", streamOut) // 不符
	}
}

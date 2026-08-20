// gateway_test.go —— 传输层与路由层单元测试(真实现验证)。
package smbgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

// TestFrameHeaderRoundTrip 帧头编解码往返一致性。
// 验证:marshalFrameHeader → unmarshalFrameHeader 字段无损。
func TestFrameHeaderRoundTrip(t *testing.T) {
	hdr := FrameHeader{
		Magic:   Magic,
		Version: Version,
		Flags:   FlagNeedReply,
		MsgType: MSG_OPERATE,
		Seq:     42,
		BodyLen: 1234,
	}
	raw := marshalFrameHeader(hdr)
	if len(raw) != headerLen {
		t.Fatalf("header length = %d, want %d", len(raw), headerLen)
	}
	got, err := unmarshalFrameHeader(raw)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != hdr {
		t.Fatalf("round trip mismatch: got %+v want %+v", got, hdr)
	}
}

// TestUnmarshalFrameHeaderRejectsBadMagic 坏魔数必须报错(协议硬契约)。
func TestUnmarshalFrameHeaderRejectsBadMagic(t *testing.T) {
	raw := marshalFrameHeader(FrameHeader{Magic: 0xDEADBEEF, Version: Version})
	if _, err := unmarshalFrameHeader(raw); err == nil {
		t.Fatal("bad magic accepted")
	}
}

// TestMarshalFrameBodyLen 组帧时 BodyLen 自动回填。
func TestMarshalFrameBodyLen(t *testing.T) {
	frame := marshalFrame(FrameHeader{MsgType: MSG_OPERATE}, []byte("hello"))
	if len(frame) != headerLen+5 {
		t.Fatalf("frame length = %d, want %d", len(frame), headerLen+5)
	}
	hdr, err := unmarshalFrameHeader(frame[:headerLen])
	if err != nil {
		t.Fatal(err)
	}
	if hdr.BodyLen != 5 || hdr.MsgType != MSG_OPERATE {
		t.Fatalf("hdr = %+v", hdr)
	}
}

// TestOperateBodyRoundTrip 控制面 body 布局:[4B jsonLen] + JSON + 流段。
func TestOperateBodyRoundTrip(t *testing.T) {
	req := &OperateRequest{Code: CodeFileWrite, Write: &WriteArgs{HandleID: 7, Offset: 99}}
	stream := []byte{1, 2, 3, 4}
	body, err := marshalOperateBody(req, stream)
	if err != nil {
		t.Fatal(err)
	}
	jsonBytes, streamOut, err := splitOperateBody(body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(streamOut, stream) {
		t.Fatalf("stream mismatch: %v", streamOut)
	}
	var got OperateRequest
	if err := json.Unmarshal(jsonBytes, &got); err != nil {
		t.Fatal(err)
	}
	if got.Code != CodeFileWrite || got.Write == nil || got.Write.HandleID != 7 || got.Write.Offset != 99 {
		t.Fatalf("json round trip mismatch: %+v", got)
	}
}

// TestSplitOperateBodyOverflow jsonLen 越界必须报错(硬契约)。
func TestSplitOperateBodyOverflow(t *testing.T) {
	// 构造 jsonLen 大于 body 剩余长度的坏帧。
	bad := []byte{0xFF, 0xFF, 0xFF, 0xFF, 0x00}
	if _, _, err := splitOperateBody(bad); err == nil {
		t.Fatal("overflow accepted")
	}
}

// mockHandler 测试用 OperateHandler:记录调用并返回固定结果。
type mockHandler struct {
	authUserCalls int
	openCalls     int
}

func (m *mockHandler) HandleAuthQueryUser(_ context.Context, args *AuthUserArgs) (*AuthUserResult, error) {
	m.authUserCalls++
	return &AuthUserResult{Found: true, Cred: &UserCred{Username: args.Username, NtHashHex: "abc"}}, nil
}
func (m *mockHandler) HandleAuthQueryAcl(_ context.Context, _ *AuthAclArgs) (*AuthAclResult, error) {
	return &AuthAclResult{Shares: []ShareInfo{}}, nil
}
func (m *mockHandler) HandleAuthSnapshot(_ context.Context, _ *SnapshotArgs) (*SnapshotResult, error) {
	return &SnapshotResult{Users: []UserCred{}, Shares: []ShareInfo{}}, nil
}
func (m *mockHandler) HandleFileOpen(_ context.Context, _ *OpenArgs) (*OpenResult, error) {
	m.openCalls++
	return &OpenResult{HandleID: 1, EndOfFile: 10}, nil
}
func (m *mockHandler) HandleFileRead(_ context.Context, _ *ReadArgs) (*ReadResult, []byte, error) {
	return &ReadResult{Length: 2}, []byte{9, 9}, nil
}
func (m *mockHandler) HandleFileWrite(_ context.Context, _ *WriteArgs, _ []byte) (*WriteResult, error) {
	return &WriteResult{Written: 4}, nil
}
func (m *mockHandler) HandleFileFlush(_ context.Context, _ *FlushArgs) error            { return nil }
func (m *mockHandler) HandleFileStat(_ context.Context, _ *StatArgs) (*StatResult, error) {
	return &StatResult{Info: FileInfo{Name: "f", EndOfFile: 3}}, nil
}
func (m *mockHandler) HandleFileSetTimes(_ context.Context, _ *SetTimesArgs) error       { return nil }
func (m *mockHandler) HandleFileTruncate(_ context.Context, _ *TruncateArgs) error       { return nil }
func (m *mockHandler) HandleFileListDir(_ context.Context, _ *ListDirArgs) (*ListDirResult, error) {
	return &ListDirResult{Entries: []FileInfo{}}, nil
}
func (m *mockHandler) HandleFileClose(_ context.Context, _ *CloseArgs) error   { return nil }
func (m *mockHandler) HandleFileUnlink(_ context.Context, _ *UnlinkArgs) error { return nil }
func (m *mockHandler) HandleFileRename(_ context.Context, _ *RenameArgs) error { return nil }

// TestRouteDispatch 正常路由:code 与指针匹配 → 调用对应 handler 方法。
func TestRouteDispatch(t *testing.T) {
	m := &mockHandler{}
	req := &OperateRequest{Code: CodeAuthQueryUser, AuthUser: &AuthUserArgs{Username: "alice"}}
	resp, _, err := req.Route(context.Background(), m, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Err != nil {
		t.Fatalf("unexpected err: %+v", resp.Err)
	}
	if resp.AuthUser == nil || !resp.AuthUser.Found || resp.AuthUser.Cred.Username != "alice" {
		t.Fatalf("authUser result mismatch: %+v", resp.AuthUser)
	}
	if m.authUserCalls != 1 {
		t.Fatalf("handler calls = %d, want 1", m.authUserCalls)
	}
}

// TestRouteCodeZeroRejected Code=0(未填写)必须拒绝。
func TestRouteCodeZeroRejected(t *testing.T) {
	m := &mockHandler{}
	req := &OperateRequest{} // code 默认 0
	resp, _, err := req.Route(context.Background(), m, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Err == nil || resp.Err.Code != ErrCodeBadRequest {
		t.Fatalf("want ErrCodeBadRequest, got %+v", resp.Err)
	}
}

// TestRouteMultipleArgsRejected 多指针同时非 nil 必须拒绝(防误填)。
func TestRouteMultipleArgsRejected(t *testing.T) {
	m := &mockHandler{}
	req := &OperateRequest{
		Code:     CodeFileOpen,
		Open:     &OpenArgs{},
		Unlink:   &UnlinkArgs{}, // 误填:与 code 不匹配
	}
	resp, _, err := req.Route(context.Background(), m, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Err == nil || resp.Err.Code != ErrCodeBadRequest {
		t.Fatalf("want ErrCodeBadRequest, got %+v", resp.Err)
	}
}

// TestRouteArgsMissing Code 有值但对应指针为 nil 必须拒绝。
func TestRouteArgsMissing(t *testing.T) {
	m := &mockHandler{}
	req := &OperateRequest{Code: CodeFileOpen} // open 指针未填
	resp, _, err := req.Route(context.Background(), m, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Err == nil || resp.Err.Code != ErrCodeBadRequest {
		t.Fatalf("want ErrCodeBadRequest, got %+v", resp.Err)
	}
}

// TestRouteUnknownCode 未知操作码 → NotImpl。
func TestRouteUnknownCode(t *testing.T) {
	m := &mockHandler{}
	req := &OperateRequest{Code: 9999, Open: &OpenArgs{}}
	resp, _, err := req.Route(context.Background(), m, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Err == nil || resp.Err.Code != ErrCodeNotImpl {
		t.Fatalf("want ErrCodeNotImpl, got %+v", resp.Err)
	}
}

// TestRouteReadReturnsStream Read 操作必须带回流数据段。
func TestRouteReadReturnsStream(t *testing.T) {
	m := &mockHandler{}
	req := &OperateRequest{Code: CodeFileRead, Read: &ReadArgs{HandleID: 1, Offset: 0, Length: 2}}
	resp, streamOut, err := req.Route(context.Background(), m, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Read == nil || resp.Read.Length != 2 {
		t.Fatalf("read result mismatch: %+v", resp.Read)
	}
	if !bytes.Equal(streamOut, []byte{9, 9}) {
		t.Fatalf("stream out mismatch: %v", streamOut)
	}
}

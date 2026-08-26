package utils

// stack_test.go —— Stack 调用栈格式化测试:输出包含调用者文件/行号与函数名。

import (
	"bytes"
	"strings"
	"testing"
)

// TestStackContainsCaller Stack(1) 的第一帧应为调用者(本测试函数),
// 输出须包含本文件路径、行号与函数名。
func TestStackContainsCaller(t *testing.T) {
	buf := Stack(1)
	if len(buf) == 0 {
		t.Fatal("Stack(1) 返回空输出")
	}
	out := string(buf)
	if !strings.Contains(out, "stack_test.go") {
		t.Errorf("Stack(1) 输出缺少本文件路径:\n%s", out)
	}
	if !strings.Contains(out, "TestStackContainsCaller") {
		t.Errorf("Stack(1) 输出缺少调用者函数名:\n%s", out)
	}
	// 应包含 PC 地址(0x... 形式)与制表符源码行
	if !bytes.Contains(buf, []byte("0x")) {
		t.Errorf("Stack(1) 输出缺少 PC 地址:\n%s", out)
	}
}

// TestStackSkipLarge 跳过帧数超过实际栈深:不应崩溃,输出可为空或部分帧。
func TestStackSkipLarge(t *testing.T) {
	buf := Stack(100000)
	_ = buf // 仅断言不 panic
}

// TestStackDifferentFrames skip 越大,输出应越短或包含更外层函数(不崩溃)。
func TestStackDifferentFrames(t *testing.T) {
	inner := string(Stack(1))
	outer := string(Stack(2))
	if len(inner) == 0 {
		t.Fatal("Stack(1) 为空")
	}
	// 两层输出不应完全相同(至少行号/函数不同)
	if inner == outer {
		t.Errorf("Stack(1) 与 Stack(2) 输出相同:\n%s", inner)
	}
}

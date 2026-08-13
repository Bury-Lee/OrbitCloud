package utils

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"runtime"
)

// Stack 返回格式化调用栈信息(含文件、行号、PC 及对应源码行)。
// skip 为跳过的栈帧数:1 = 跳过 Stack 自身,2 = 再跳过调用方。
// 源码不可读时显示 "Unknown";连续同一文件的帧省略重复源码行。
// 涉及文件 I/O,不适合高频调用。
func Stack(skip int) []byte {
	buf := new(bytes.Buffer)
	var lastFile string
	dunno := "Unknown"

	// 遍历调用栈帧
	for i := skip; ; i++ {
		pc, file, line, ok := runtime.Caller(i)
		if !ok {
			break // 已到栈顶
		}

		// 输出文件、行号与 PC 地址
		fmt.Fprintf(buf, "%s:%d (0x%x)\n", file, line, pc)

		// 仅对新文件输出源码行,避免重复
		if file != lastFile {
			sourceLine, err := readNthLine(file, line-1)
			if err != nil {
				sourceLine = dunno
			}
			fmt.Fprintf(buf, "\t%s: %s\n", function(pc), sourceLine)
			lastFile = file
		} else {
			// 同一文件只输出函数名
			fmt.Fprintf(buf, "\t%s\n", function(pc))
		}
	}
	return buf.Bytes()
}

// function 返回 pc 对应的函数名;找不到函数信息时返回 "unknown"。
func function(pc uintptr) string {
	fn := runtime.FuncForPC(pc)
	if fn == nil {
		return "unknown"
	}
	return fn.Name()
}

// readNthLine 读取文件的第 n 行(0 起始);文件无法打开、读取失败或行不存在时返回错误。
// 注意:每次从文件头扫描,不适合频繁调用。
func readNthLine(file string, n int) (string, error) {
	f, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		if lineNum == n {
			return scanner.Text(), nil
		}
		lineNum++
	}
	return "", scanner.Err()
}

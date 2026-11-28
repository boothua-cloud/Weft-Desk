package engine

import (
	"os"
	"strings"
)

// 三平台共用的助手日志/文件小工具。

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// tailFile 读取文件末尾若干字节，用于把安装/启动失败原因带进界面。
func tailFile(path string, max int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(b) > max {
		b = b[len(b)-max:]
	}
	return strings.TrimSpace(string(b))
}

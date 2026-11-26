//go:build darwin

package main

/*
#cgo LDFLAGS: -framework Foundation -framework AppKit
#include "appname_darwin.h"
#include <stdlib.h>
*/
import "C"

import "unsafe"

// setPlatformAppName 改掉 macOS 菜单栏与 Dock 上显示的应用名。
// 直接跑裸二进制时系统默认取可执行文件名（weft），这里显式覆盖成中文名。
func setPlatformAppName(name string) {
	c := C.CString(name)
	defer C.free(unsafe.Pointer(c))
	C.weft_set_process_name(c)
}

// menuBarTitle 读取菜单栏左上角当前显示的应用名。
func menuBarTitle() string {
	p := C.weft_menu_bar_title()
	if p == nil {
		return ""
	}
	defer C.free(unsafe.Pointer(p))
	return C.GoString(p)
}

package main

import (
	"bytes"
	"image/png"
	"testing"
)

// 托盘图标现在直接用应用图标（build/tray-icon.png），这里保证它被正确嵌入、
// 是合法 PNG，并且确实画了东西（不是一张全透明的空图）。
func TestTrayIconEmbedded(t *testing.T) {
	if len(trayIcon) == 0 {
		t.Fatal("托盘图标为空，检查 build/tray-icon.png 是否还在")
	}
	img, err := png.Decode(bytes.NewReader(trayIcon))
	if err != nil {
		t.Fatalf("托盘图标解不出 PNG: %v", err)
	}
	b := img.Bounds()
	if b.Dx() != 44 || b.Dy() != 44 {
		t.Fatalf("托盘图标尺寸应为 44x44，实际 %dx%d", b.Dx(), b.Dy())
	}
	opaque := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a > 0x8000 {
				opaque++
			}
		}
	}
	if opaque < 400 {
		t.Fatalf("托盘图标几乎全透明，只有 %d 个不透明像素", opaque)
	}
}

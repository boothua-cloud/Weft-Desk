package tests

import (
	"bytes"
	"image/png"
	"os"
	"testing"
)

// 托盘图标直接用应用图标（build/tray-icon.png）。根包把它 embed 进二进制，
// 这里校验的是源文件本身：合法 PNG、44×44、确实画了东西（不是全透明空图）。
func TestTrayIconFile(t *testing.T) {
	raw, err := os.ReadFile("../../build/tray-icon.png")
	if err != nil {
		t.Fatalf("读不到 build/tray-icon.png: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
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

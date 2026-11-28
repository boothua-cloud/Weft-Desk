package tests

import (
	"testing"

	"github.com/boothua/weft/internal/update"
	"github.com/wailsapp/wails/v3/pkg/updater"
	"github.com/wailsapp/wails/v3/pkg/updater/providers/github"
)

// 版本比对与附件挑选。挑错附件的后果是下载下来装不上，
// 用户只看到一句「安装失败」。

func TestIsNewer(t *testing.T) {
	cases := []struct {
		tag, cur string
		want     bool
	}{
		{"v0.1.4", "0.1.3", true},
		{"v0.1.3", "0.1.3", false},
		{"v0.1.3", "0.1.4", false},
		{"0.2.0", "0.1.9", true},
		{"v1.0.0", "0.9.9", true},
		{"v0.1.4", "", true},
	}
	for _, c := range cases {
		if got := update.IsNewer(c.tag, c.cur); got != c.want {
			t.Errorf("IsNewer(%q,%q)=%v want %v", c.tag, c.cur, got, c.want)
		}
	}
}

func TestPickUpdateAsset(t *testing.T) {
	assets := []github.ReleaseAsset{
		{Name: "weft-0.1.4-macos-arm64.dmg"},
		{Name: "weft-0.1.4-macos-arm64.zip"},
		{Name: "weft-0.1.4-windows-amd64-setup.exe"},
		{Name: "weft-0.1.4-windows-amd64.zip"},
		{Name: "weft-0.1.4-windows-amd64-update.zip"},
		{Name: "weft-0.1.4-linux-amd64.tar.gz"},
	}
	for _, c := range []struct{ plat, want string }{
		{"windows", "weft-0.1.4-windows-amd64-update.zip"},
		{"darwin", "weft-0.1.4-macos-arm64.zip"},
		{"linux", "weft-0.1.4-linux-amd64.tar.gz"},
	} {
		i := update.PickUpdateAsset(updater.CheckRequest{Platform: c.plat, Arch: "arm64"}, assets)
		if i < 0 || assets[i].Name != c.want {
			got := "<none>"
			if i >= 0 {
				got = assets[i].Name
			}
			t.Errorf("%s 选中了 %s，期望 %s", c.plat, got, c.want)
		}
	}
}

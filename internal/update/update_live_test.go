package update

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/updater"
	"github.com/wailsapp/wails/v3/pkg/updater/providers/github"
)

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
		if got := isNewer(c.tag, c.cur); got != c.want {
			t.Errorf("isNewer(%q,%q)=%v want %v", c.tag, c.cur, got, c.want)
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
		i := pickUpdateAsset(updater.CheckRequest{Platform: c.plat, Arch: "arm64"}, assets)
		if i < 0 || assets[i].Name != c.want {
			got := "<none>"
			if i >= 0 {
				got = assets[i].Name
			}
			t.Errorf("%s 选中了 %s，期望 %s", c.plat, got, c.want)
		}
	}
}

func TestFetchLatestReleaseLive(t *testing.T) {
	// 依赖公网 GitHub API，默认不跑；要用时 WEFT_LIVE_UPDATE=1 go test -run Live
	if os.Getenv("WEFT_LIVE_UPDATE") != "1" {
		t.Skip("设置 WEFT_LIVE_UPDATE=1 才跑")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rel, err := fetchLatestRelease(ctx)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if rel == nil {
		t.Fatal("没有发布")
	}
	t.Logf("最新 tag=%s 附件 %d 个", rel.TagName, len(rel.Assets))
	if !isNewer(rel.TagName, "0.1.0") {
		t.Errorf("0.1.0 应该比 %s 旧", rel.TagName)
	}
}

func TestCanReplaceSelf(t *testing.T) {
	t.Logf("canReplaceSelf=%v", canReplaceSelf())
	if _, err := pickInstaller([]ghAsset{
		{Name: "weft-0.1.4-macos-arm64.dmg"},
		{Name: "weft-0.1.4-windows-amd64-setup.exe"},
		{Name: "weft-0.1.4-linux-amd64.tar.gz"},
	}); err != nil {
		t.Fatalf("pickInstaller: %v", err)
	}
}

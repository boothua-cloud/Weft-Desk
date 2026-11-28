package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/boothua/weft/internal/appinfo"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/updater"
	"github.com/wailsapp/wails/v3/pkg/updater/providers/github"
)

// 更新流程向前端推事件时用的名字。前端只认这两个，具体是「框架自己下载」
// 还是「我们下载安装包」，对界面没有区别。
const (
	updateEventProgress  = "weft:update-progress"
	updateEventAvailable = "weft:update-available"
)

// UpdateStatus 是「检查更新」的结果，前端据此决定显示哪一屏。
type UpdateStatus struct {
	CurrentVersion string `json:"currentVersion"`
	LatestVersion  string `json:"latestVersion"`
	HasUpdate      bool   `json:"hasUpdate"`
	Notes          string `json:"notes"`
	PublishedAt    string `json:"publishedAt"`
	ReleaseURL     string `json:"releaseURL"`
	AssetName      string `json:"assetName"`
	AssetSize      int64  `json:"assetSize"`

	// CanAutoUpdate 为 false 时只能下载安装包让用户手动装。
	CanAutoUpdate bool `json:"canAutoUpdate"`
	// ManualReason 说明为什么不能自动更新，界面据此换文案：
	// "permission" 是安装位置没写权限，"" 表示其他原因（发布里缺更新包）。
	ManualReason string `json:"manualReason"`

	// ReadyToRestart 表示新版已经落到暂存区，重启即生效。
	ReadyToRestart bool `json:"readyToRestart"`

	// State 是给界面用的阶段：idle / latest / available / downloading /
	// ready / manual / error。
	State string `json:"state"`
	Error string `json:"error"`
}

// updateState 把自动更新的运行态从 WeftService 里分出来，避免那个结构继续膨胀。
// Manager 是自动更新的运行态：框架的 Updater + 最近一次检查结果。
type Manager struct {
	app *application.App

	mu      sync.RWMutex
	up      *updater.Updater
	release *updater.Release
	ready   bool
}

func (m *Manager) updater() *updater.Updater {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.up
}

func (m *Manager) setRelease(r *updater.Release) {
	m.mu.Lock()
	m.release = r
	m.mu.Unlock()
}

func (m *Manager) releaseSnapshot() *updater.Release {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.release
}

// initUpdater 在 application.New 之后调用（app.Updater 那时才存在）。
// Window 用 WindowNone：更新界面是我们自己的 Vue 弹窗，不需要框架另开一个
// webview；框架仍然负责下载、校验、暂存和替换。
func (m *Manager) Init() {
	app := m.app
	if app == nil {
		return
	}
	gh, err := github.New(github.Config{
		Repository:   appinfo.UpdateRepo,
		AssetMatcher: PickUpdateAsset,
	})
	if err != nil {
		log.Printf("[update] 初始化发布源失败: %v", err)
		return
	}
	err = app.Updater.Init(updater.Config{
		CurrentVersion: appinfo.Version,
		Providers:      []updater.Provider{gh},
		Window:         updater.WindowNone,
	})
	if err != nil {
		log.Printf("[update] 初始化更新器失败: %v", err)
		return
	}
	m.mu.Lock()
	m.up = app.Updater
	m.mu.Unlock()

	// 框架自己的下载进度转发成统一事件，前端只订阅一个名字。
	app.Event.On(updater.EventDownloadProgress, func(e *application.CustomEvent) {
		// Data 是 any，框架内部直接塞的是 updater.Progress；不同版本可能
		// 走序列化形态，统一 marshal 一次再解，省得按类型分支。
		raw, err := json.Marshal(e.Data)
		if err != nil {
			return
		}
		var p updater.Progress
		if err := json.Unmarshal(raw, &p); err != nil {
			return
		}
		emitUpdateProgress(app, p.Written, p.Total, p.Rate)
	})
	log.Printf("[update] 更新器就绪，当前 v%s", appinfo.Version)
}

// PickUpdateAsset 决定每个平台下载哪个附件。
//
// 不能用框架默认的匹配规则：它按文件名里有没有 goos 字样来挑，而我们的
// mac 产物叫 macos（不是 darwin）；更要紧的是它会把便携版 zip 选中 ——
// 那个包里是 weft.exe + wintun.dll 两个顶层条目，而替换逻辑要求压缩包
// 解出来只能有一个顶层文件。
func PickUpdateAsset(req updater.CheckRequest, assets []github.ReleaseAsset) int {
	pick := func(match func(name string) bool) int {
		for i, a := range assets {
			if match(strings.ToLower(a.Name)) {
				return i
			}
		}
		return -1
	}
	switch req.Platform {
	case "windows":
		if i := pick(func(n string) bool {
			return strings.HasSuffix(n, "-windows-amd64-update.zip")
		}); i >= 0 {
			return i
		}
	case "darwin":
		// dmg 是一层磁盘映像，框架不会挂载它；mac 的自动更新必须是
		// 内含单个 .app 的 zip。
		if i := pick(func(n string) bool {
			return strings.HasSuffix(n, "-macos-arm64.zip")
		}); i >= 0 {
			return i
		}
	case "linux":
		// tar.gz 里只有一个 weft，正好符合「单顶层条目」的要求。
		if i := pick(func(n string) bool {
			return strings.HasSuffix(n, "-linux-amd64.tar.gz")
		}); i >= 0 {
			return i
		}
	}
	// 找不到专用附件就返回 -1，让 Check 报「no asset」，走上面的兜底
	// 分支显示「有新版但需要手动装」。不要退回默认规则：它很可能选中
	// 便携版 zip，那个包里有两个顶层文件，解压替换一定会失败。
	return -1
}

// CheckUpdate 供界面的「检查更新」调用。
func (m *Manager) CheckUpdate() (string, error) {
	st := m.checkUpdate()
	return jsonString(st), nil
}

func (m *Manager) checkUpdate() UpdateStatus {
	st := UpdateStatus{CurrentVersion: appinfo.Version, State: "idle"}
	up := m.updater()
	if up == nil {
		st.State = "error"
		st.Error = "更新模块未就绪"
		return st
	}
	st.CanAutoUpdate = canReplaceSelf()
	if !st.CanAutoUpdate {
		st.ManualReason = "permission"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	rel, err := up.Check(ctx)
	if err != nil {
		// 发布里没有可替换的附件，或者网络抖动。退回直接查 GitHub API：
		// 至少让用户看到「有新版本」并能手动下载，而不是只看到一句报错。
		st.CanAutoUpdate = false
		st.ManualReason = ""
		if latest, ferr := fetchLatestRelease(ctx); ferr == nil && latest != nil {
			if IsNewer(latest.TagName, appinfo.Version) {
				st.HasUpdate = true
				st.State = "available"
				st.LatestVersion = strings.TrimPrefix(latest.TagName, "v")
				st.Notes = latest.Body
				st.PublishedAt = shortDate(latest.PublishedAt)
				st.ReleaseURL = latest.HTMLURL
				st.Error = err.Error()
				return st
			}
			st.State = "latest"
			return st
		}
		st.State = "error"
		st.Error = err.Error()
		return st
	}
	if rel == nil {
		st.State = "latest"
		return st
	}

	m.setRelease(rel)
	st.HasUpdate = true
	st.State = "available"
	st.LatestVersion = rel.Version
	st.Notes = rel.Notes
	st.PublishedAt = rel.PublishedAt.Format("2006-01-02")
	st.AssetName = rel.Artifact.Filename
	st.AssetSize = rel.Artifact.Size
	if v, ok := rel.Metadata["github.release.htmlURL"].(string); ok {
		st.ReleaseURL = v
	}
	return st
}

// DownloadUpdate 下载并暂存新版。完成后调用 ApplyUpdate 重启生效。
func (m *Manager) DownloadUpdate() (string, error) {
	up := m.updater()
	if up == nil {
		return "", errors.New("更新模块未就绪")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if err := up.DownloadAndInstall(ctx); err != nil {
		return "", err
	}
	m.mu.Lock()
	m.ready = true
	m.mu.Unlock()
	log.Printf("[update] 新版已暂存，等待重启安装")
	st := m.statusSnapshot()
	return jsonString(st), nil
}

// ApplyUpdate 用后台助手替换旧文件并重新拉起应用。调用后本进程会退出。
func (m *Manager) ApplyUpdate() error {
	up := m.updater()
	if up == nil {
		return errors.New("更新模块未就绪")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	return up.Restart(ctx)
}

// UpdateInfo 返回当前更新状态，前端在弹窗打开时先拿一次。
func (m *Manager) UpdateInfo() (string, error) {
	return jsonString(m.statusSnapshot()), nil
}

func (m *Manager) statusSnapshot() UpdateStatus {
	st := UpdateStatus{
		CurrentVersion: appinfo.Version,
		State:          "idle",
		CanAutoUpdate:  canReplaceSelf(),
	}
	if !st.CanAutoUpdate {
		st.ManualReason = "permission"
	}
	if m == nil {
		return st
	}
	m.mu.RLock()
	rel, ready := m.release, m.ready
	m.mu.RUnlock()

	st.ReadyToRestart = ready
	if rel == nil {
		return st
	}
	st.HasUpdate = true
	st.State = "available"
	st.LatestVersion = rel.Version
	st.Notes = rel.Notes
	st.PublishedAt = rel.PublishedAt.Format("2006-01-02")
	st.AssetName = rel.Artifact.Filename
	st.AssetSize = rel.Artifact.Size
	if v, ok := rel.Metadata["github.release.htmlURL"].(string); ok {
		st.ReleaseURL = v
	}
	if ready {
		st.State = "ready"
	}
	return st
}

// OpenReleasePage 用系统浏览器打开发布页。
func (m *Manager) OpenReleasePage() error {
	url := "https://github.com/" + appinfo.UpdateRepo + "/releases/latest"
	if rel := m.releaseSnapshot(); rel != nil {
		if v, ok := rel.Metadata["github.release.htmlURL"].(string); ok && v != "" {
			url = v
		}
	}
	return openURL(url)
}

// DownloadInstaller 用于当前安装位置不可写的情况：把安装包下到下载目录
// 再打开它，剩下的交给系统安装器。
func (m *Manager) DownloadInstaller() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	rel, err := fetchLatestRelease(ctx)
	if err != nil {
		return "", err
	}
	if rel == nil {
		return "", errors.New("没有找到已发布的版本")
	}
	asset, err := pickInstaller(rel.Assets)
	if err != nil {
		return "", err
	}
	dir, err := downloadDir()
	if err != nil {
		return "", err
	}
	// 附件名来自远端，取 Base 再拼：带 `../` 的名字不能落到下载目录外面
	dst := filepath.Join(dir, filepath.Base(asset.Name))
	onProgress := func(written, total int64, rate float64) {
		emitUpdateProgress(m.app, written, total, rate)
	}
	if err := downloadFile(ctx, asset.BrowserDownloadURL, dst, asset.Size, asset.Digest, onProgress); err != nil {
		return "", err
	}
	log.Printf("[update] 安装包已下载：%s", dst)
	if err := openInstaller(dst, asset.Digest != ""); err != nil {
		// 打不开不算失败：文件已经下好了，告诉用户在哪儿。
		return dst, fmt.Errorf("已下载到 %s，但没能自动打开：%w", dst, err)
	}
	return dst, nil
}

// ---------------------------------------------------------------- 平台判定

// canReplaceSelf 判断当前安装位置是否允许直接替换文件。
//
// Windows 装到 %LOCALAPPDATA%\Programs\Weft、Linux 放在用户目录、mac 放在
// ~/Applications 时都能直接换；mac 拖进 /Applications、deb 装到 /usr/bin
// 时不行，只能下载安装包让用户手动装。
func canReplaceSelf() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	target := exe
	if runtime.GOOS == "darwin" {
		if p := appBundlePath(exe); p != "" {
			target = p
		}
	}
	dir := filepath.Dir(target)
	f, err := os.CreateTemp(dir, ".weft-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

// appBundlePath 从可执行文件路径往上找 .app 目录，找不到返回空串。
func appBundlePath(exe string) string {
	parts := strings.Split(filepath.Clean(exe), string(os.PathSeparator))
	for i, p := range parts {
		if strings.HasSuffix(p, ".app") {
			return string(os.PathSeparator) + filepath.Join(parts[1:i+1]...)
		}
	}
	return ""
}

func openURL(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

// openInstaller 把下载好的安装包交给系统。
// verified 表示这个包有发布方的 sha256 且已经比对通过；没有校验值的包在
// Windows 上不直接执行，只打开所在目录让用户自己决定。
func openInstaller(path string, verified bool) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", path).Start()
	case "windows":
		if !verified {
			return exec.Command("explorer", "/select,"+path).Start()
		}
		return exec.Command(path).Start()
	default:
		// Linux 发的是 tar.gz，没法「运行」；打开所在目录让用户自己解。
		return exec.Command("xdg-open", filepath.Dir(path)).Start()
	}
}

func downloadDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, "Downloads")
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		return dir, nil
	}
	return os.TempDir(), nil
}

// ---------------------------------------------------------------- 兜底查询

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
	// Digest 是 GitHub 给的 `sha256:xxx`，2025 年之后的 release API 才有，
	// 没有时是空串，只能退回到大小校验。
	Digest string `json:"digest"`
}

type ghRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	HTMLURL     string    `json:"html_url"`
	PublishedAt string    `json:"published_at"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	Assets      []ghAsset `json:"assets"`
}

func fetchLatestRelease(ctx context.Context) (*ghRelease, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.github.com/repos/"+appinfo.UpdateRepo+"/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("查询发布信息失败：HTTP %d", resp.StatusCode)
	}
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

// pickInstaller 挑用户手动安装时该下哪个包。
func pickInstaller(assets []ghAsset) (ghAsset, error) {
	want := func(name string) bool {
		n := strings.ToLower(name)
		switch runtime.GOOS {
		case "windows":
			return strings.HasSuffix(n, "-windows-amd64-setup.exe")
		case "darwin":
			return strings.HasSuffix(n, ".dmg") && strings.Contains(n, "arm64")
		default:
			return strings.HasSuffix(n, "-linux-amd64.tar.gz")
		}
	}
	for _, a := range assets {
		if want(a.Name) {
			return a, nil
		}
	}
	return ghAsset{}, fmt.Errorf("发布里没有适合 %s/%s 的安装包", runtime.GOOS, runtime.GOARCH)
}

// downloadFile 下载并落到 dst。wantDigest 是发布方给的 `sha256:...`，
// 非空就必须比对通过；为空只能按字节数判断，调用方需要自己决定敢不敢执行。
func downloadFile(ctx context.Context, url, dst string, size int64, wantDigest string, onProgress func(written, total int64, rate float64)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 15 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载失败：HTTP %d", resp.StatusCode)
	}
	if size == 0 {
		size = resp.ContentLength
	}
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	var h hash.Hash
	var out io.Writer = f
	if wantDigest != "" {
		h = sha256.New()
		out = io.MultiWriter(f, h)
	}
	written, err := copyWithProgress(ctx, out, resp.Body, size, onProgress)
	cerr := f.Close()
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if cerr != nil {
		_ = os.Remove(tmp)
		return cerr
	}
	if size > 0 && written != size {
		_ = os.Remove(tmp)
		return fmt.Errorf("下载不完整：%d/%d 字节", written, size)
	}
	if h != nil {
		got := "sha256:" + hex.EncodeToString(h.Sum(nil))
		if !strings.EqualFold(got, strings.TrimSpace(wantDigest)) {
			_ = os.Remove(tmp)
			return fmt.Errorf("安装包校验不通过，已丢弃：发布方 %s，实际 %s", wantDigest, got)
		}
	} else {
		log.Printf("[update] 发布信息里没有 sha256，只能按大小校验（%d 字节）", written)
	}
	return os.Rename(tmp, dst)
}

func copyWithProgress(ctx context.Context, dst io.Writer, src io.Reader, total int64, onProgress func(written, total int64, rate float64)) (int64, error) {
	buf := make([]byte, 64*1024)
	var written int64
	last := time.Now()
	var lastBytes int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return written, werr
			}
			written += int64(n)
			if time.Since(last) > 200*time.Millisecond {
				rate := float64(written-lastBytes) / time.Since(last).Seconds()
				if onProgress != nil {
					onProgress(written, total, rate)
				}
				last, lastBytes = time.Now(), written
			}
		}
		if rerr == io.EOF {
			if onProgress != nil {
				onProgress(written, total, 0)
			}
			return written, nil
		}
		if rerr != nil {
			return written, rerr
		}
	}
}

func emitUpdateProgress(app *application.App, written, total int64, rate float64) {
	if app == nil {
		return
	}
	percent := 0.0
	if total > 0 {
		percent = float64(written) / float64(total) * 100
	}
	app.Event.Emit(updateEventProgress, map[string]any{
		"written": written,
		"total":   total,
		"percent": percent,
		"rate":    rate,
	})
}

// shortDate 把 GitHub 的 RFC3339 时间裁成日期。兜底路径拿到的是原始字符串，
// 直接显示出来是 2026-11-27T08:53:58Z，界面里太吵。
func shortDate(s string) string {
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}

func jsonString(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// IsNewer 只用于兜底路径的版本比较：拆成数字段比大小，容忍 v 前缀和
// 段数不一致（0.1.4 vs 0.1.4.1）。
func IsNewer(tag, current string) bool {
	a := strings.Split(strings.TrimPrefix(strings.TrimPrefix(tag, "v"), "V"), ".")
	b := strings.Split(strings.TrimPrefix(strings.TrimPrefix(current, "v"), "V"), ".")
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y int
		if i < len(a) {
			fmt.Sscanf(a[i], "%d", &x)
		}
		if i < len(b) {
			fmt.Sscanf(b[i], "%d", &y)
		}
		if x != y {
			return x > y
		}
	}
	return false
}

// New 建一个管理器。app 传 nil 时只读状态，不初始化框架更新器。
func New(app *application.App) *Manager { return &Manager{app: app} }

// CheckQuiet 是启动后的静默检查：只在确实有新版本时发事件，不打扰界面。
func (m *Manager) CheckQuiet() {
	st := m.checkUpdate()
	if !st.HasUpdate || m.app == nil {
		return
	}
	log.Printf("[update] 发现新版本 v%s（当前 v%s）", st.LatestVersion, st.CurrentVersion)
	m.app.Event.Emit(updateEventAvailable, st)
}

package desktop

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/boothua/weft/internal/appinfo"
	"github.com/boothua/weft/internal/engine"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// 一个诊断包里放这些。名字就是用户解开 zip 之后看到的文件名，
// 用英文是为了在任何系统上都不会解压出一堆乱码。
const (
	bundleMeta      = "meta.json"
	bundleConfig    = "config.json"
	bundleSelfCheck = "selfcheck.json"
	bundleDiag      = "diag.txt"
	bundleLog       = "app.log"
)

// ExportText 把一段文本存成文件，供「导出 CSV」这类功能用。
// 返回落盘路径；用户取消时返回空串和 nil，界面不用当成错误。
func (s *WeftService) ExportText(ctx context.Context, filename, content string) (string, error) {
	if !s.callerAllowed(ctx) {
		return "", errNotMainWindow
	}
	ext := strings.ToLower(filepath.Ext(filename))
	path, err := s.pickSavePath("导出到文件", filename, fileFilter(ext))
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", nil
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		s.audit("导出文件", filename, err)
		return "", err
	}
	s.audit("导出文件", path, nil)
	return path, nil
}

func fileFilter(ext string) []application.FileFilter {
	switch ext {
	case ".csv":
		return []application.FileFilter{{DisplayName: "CSV 文件", Pattern: "*.csv"}}
	case ".zip":
		return []application.FileFilter{{DisplayName: "ZIP 压缩包", Pattern: "*.zip"}}
	case ".json":
		return []application.FileFilter{{DisplayName: "JSON 文件", Pattern: "*.json"}}
	}
	return nil
}

// pickSavePath 弹系统保存对话框。没有主窗口（或还没建好）时退到下载目录，
// 免得「导出」在一个没有窗口的场合直接不可用。
func (s *WeftService) pickSavePath(title, defaultName string, filters []application.FileFilter) (string, error) {
	s.mu.Lock()
	app := s.app
	s.mu.Unlock()
	if app == nil {
		dir := downloadDir()
		if dir == "" {
			return "", fmt.Errorf("找不到可写的目录")
		}
		return filepath.Join(dir, defaultName), nil
	}
	path, err := app.Dialog.SaveFileWithOptions(&application.SaveFileDialogOptions{
		Title:    title,
		Filename: defaultName,
		Filters:  filters,
	}).PromptForSingleSelection()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(path) == "" {
		return "", nil
	}
	return path, nil
}

func downloadDir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dir := filepath.Join(home, "Downloads")
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			return dir
		}
		return home
	}
	return ""
}

// revealFile 在系统文件管理器里选中这个文件。
func revealFile(path string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", "-R", path)
	case "windows":
		cmd = exec.Command("explorer", "/select,", path)
	default:
		cmd = exec.Command("xdg-open", filepath.Dir(path))
	}
	_ = cmd.Start()
}

// ExportDiagBundle 生成一个可以直接发给别人看的诊断包。
//
// 生成过程不发任何网络请求（除了本机已有的状态），自检和分层诊断各有超时，
// 服务端挂着也能出包——出问题的时候恰恰最需要它。
func (s *WeftService) ExportDiagBundle(ctx context.Context) (string, error) {
	if !s.callerAllowed(ctx) {
		return "", errNotMainWindow
	}
	now := time.Now()
	files, notes := s.bundleFiles(ctx, now)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		w, err := zw.Create(f.name)
		if err != nil {
			return "", err
		}
		if _, err := w.Write(f.body); err != nil {
			return "", err
		}
	}
	if err := zw.Close(); err != nil {
		return "", err
	}

	name := "weft-diag-" + now.Format("20060102-150405") + ".zip"
	path, err := s.pickSavePath("保存诊断包", name, fileFilter(".zip"))
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", nil
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		s.audit("生成诊断包", name, err)
		return "", err
	}
	s.audit("生成诊断包", path+"（"+notes+"）", nil)
	// 生成完直接把文件选中，用户不用自己去翻文件夹
	go func() {
		time.Sleep(120 * time.Millisecond)
		revealFile(path)
	}()
	return path, nil
}

type bundleFile struct {
	name string
	body []byte
}

func (s *WeftService) bundleFiles(ctx context.Context, now time.Time) ([]bundleFile, string) {
	s.mu.Lock()
	cfg, eng := s.cfg, s.engine
	s.mu.Unlock()

	files := []bundleFile{}

	meta, _ := json.MarshalIndent(map[string]any{
		"version":  appinfo.Version,
		"platform": runtime.GOOS,
		"arch":     runtime.GOARCH,
		"time":     now.Format(time.RFC3339),
		"dataDir":  appinfo.DataDirDisplay(),
	}, "", "  ")
	files = append(files, bundleFile{bundleMeta, meta})

	// 配置里没有明文 Key（它在钥匙串），但 AuthKey 字段和地址仍要过一遍脱敏
	cfg.AuthKey = ""
	raw, _ := json.MarshalIndent(cfg, "", "  ")
	files = append(files, bundleFile{bundleConfig, []byte(redactText(string(raw)))})

	// 自检和分层诊断都在当前进程里跑，各自限时；超时写清楚原因，别让整包卡住
	self := s.selfCheckJSON(6 * time.Second)
	files = append(files, bundleFile{bundleSelfCheck, []byte(redactText(self))})

	diagLines := diagSnapshot(eng, 6*time.Second)
	files = append(files, bundleFile{bundleDiag, []byte(redactText(strings.Join(diagLines, "\n")) + "\n")})

	logTail := tailLog(800)
	files = append(files, bundleFile{bundleLog, []byte(strings.Join(redactLines(logTail), "\n") + "\n")})

	notes := fmt.Sprintf("自检 %d 行、诊断 %d 行、日志 %d 行", len(strings.Split(self, "\n")), len(diagLines), len(logTail))
	return files, notes
}

// selfCheckJSON 限时跑一次自检。它内部会探服务端，服务端不通时可能拖很久。
func (s *WeftService) selfCheckJSON(timeout time.Duration) string {
	done := make(chan string, 1)
	go func() {
		out, err := s.SelfCheck()
		if err != nil {
			done <- fmt.Sprintf(`{"error":%q}`, redactError(err.Error()))
			return
		}
		done <- out
	}()
	select {
	case out := <-done:
		return out
	case <-time.After(timeout):
		return fmt.Sprintf(`{"error":"自检超时（%s）"}`, timeout)
	}
}

// diagSnapshot 取分层诊断的文本。引擎没起来就写一句现状，
// 这本身也是有用信息：说明问题在隧道之前。
func diagSnapshot(eng engine.TunnelEngine, timeout time.Duration) []string {
	if eng == nil {
		return []string{"引擎还没创建，隧道未启动"}
	}
	dumper, ok := eng.(interface {
		DiagDump(context.Context, []engine.DiagTarget) []string
	})
	if !ok {
		return []string{"当前引擎 " + eng.Name() + " 不支持分层诊断"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	lines := dumper.DiagDump(ctx, []engine.DiagTarget{})
	if len(lines) == 0 {
		lines = []string{"没有输出"}
	}
	return lines
}

// tailLog 取日志末尾 n 行。日志可能有几十兆，整份塞进包里不合适。
func tailLog(n int) []string {
	dir := appinfo.DataDir()
	if dir == "" {
		return []string{"读不到数据目录"}
	}
	path := filepath.Join(dir, "weft.log")
	data, err := os.ReadFile(path)
	if err != nil {
		return []string{"读不到日志：" + err.Error()}
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

package desktop

import "github.com/boothua/weft/internal/engine"

// 系统提权助手：安装/移除常驻助手，让连隧道不再每次弹授权。

// GetHelperStatus 让前端知道「打开就能连」是否已经成立。
func (s *WeftService) GetHelperStatus() (string, error) {
	return toJSON(engine.HelperStatus())
}

// InstallHelper 安装常驻助手：一次授权，之后连接零弹窗。
func (s *WeftService) InstallHelper() (string, error) {
	if err := engine.InstallHelper(); err != nil {
		s.audit("安装系统助手", helperLabelSafe(), err)
		return "", err
	}
	s.audit("安装系统助手", helperLabelSafe(), nil)
	st, _ := toJSON(engine.HelperStatus())
	return st, nil
}

func (s *WeftService) UninstallHelper() (string, error) {
	if err := engine.UninstallHelper(); err != nil {
		s.audit("移除系统助手", helperLabelSafe(), err)
		return "", err
	}
	s.audit("移除系统助手", helperLabelSafe(), nil)
	st, _ := toJSON(engine.HelperStatus())
	return st, nil
}

func helperLabelSafe() string {
	if h := engine.HelperStatus(); h.Installed {
		return "已安装"
	}
	return "未安装"
}

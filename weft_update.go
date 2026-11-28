package main

// 自动更新的绑定方法。状态机与下载替换都在 internal/update，
// 这里只做转发：前端能调到的名字保持不变。

import (
	"errors"

	"github.com/boothua/weft/internal/update"
)

var errUpdateNotReady = errors.New("更新模块未就绪")

// initUpdater 在 application.New 之后调用，那时 app.Updater 才存在。
func (s *WeftService) initUpdater() {
	s.upd = update.New(s.app)
	s.upd.Init()
}

func (s *WeftService) CheckUpdate() (string, error) {
	if s.upd == nil {
		return "", errUpdateNotReady
	}
	return s.upd.CheckUpdate()
}

func (s *WeftService) DownloadUpdate() (string, error) {
	if s.upd == nil {
		return "", errUpdateNotReady
	}
	return s.upd.DownloadUpdate()
}

func (s *WeftService) ApplyUpdate() error {
	if s.upd == nil {
		return errUpdateNotReady
	}
	return s.upd.ApplyUpdate()
}

func (s *WeftService) UpdateInfo() (string, error) {
	if s.upd == nil {
		return "", errUpdateNotReady
	}
	return s.upd.UpdateInfo()
}

func (s *WeftService) OpenReleasePage() error {
	if s.upd == nil {
		return errUpdateNotReady
	}
	return s.upd.OpenReleasePage()
}

func (s *WeftService) DownloadInstaller() (string, error) {
	if s.upd == nil {
		return "", errUpdateNotReady
	}
	return s.upd.DownloadInstaller()
}

// checkUpdateQuiet 供启动流程调用：查一次，有新版本才发事件。
func (s *WeftService) checkUpdateQuiet() {
	if s.upd == nil {
		return
	}
	s.upd.CheckQuiet()
}

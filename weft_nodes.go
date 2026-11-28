package main

import (
	"context"
	"fmt"

	"github.com/boothua/weft/internal/model"
)

// 设备：Headscale 节点列表与增删改、路由审批、节点认证请求。

func (s *WeftService) ListDevices() (string, error) {
	if msg := s.manageErr(); msg != "" {
		return toJSON(model.Cached[model.Device]{Items: []model.Device{}, Source: "none", Error: msg})
	}
	return cachedList(s.store, s.cacheKey("devices"), func() ([]model.Device, error) { return s.cp.Devices("") })
}

// ListDevicesByUser 支持按成员过滤（对应 ?user=）。
func (s *WeftService) ListDevicesByUser(user string) (string, error) {
	if msg := s.manageErr(); msg != "" {
		return toJSON(model.Cached[model.Device]{Items: []model.Device{}, Source: "none", Error: msg})
	}
	return wrap(s.cp.Devices(user))
}

func (s *WeftService) GetDevice(id string) (string, error) {
	return wrap(s.cp.Device(id))
}

// RegisterNode 用 Auth Key 手动注册一个节点（对应 POST /node/register）。
func (s *WeftService) RegisterNode(user, key string) (string, error) {
	d, err := s.cp.RegisterNode(user, key)
	if err != nil {
		return "", err
	}
	s.store.AddAudit("注册节点", d.Name)
	return wrapOne(d)
}

func (s *WeftService) DeleteDevice(ctx context.Context, id string) error {
	if !s.callerAllowed(ctx) {
		return errNotMainWindow
	}
	if err := s.cp.DeleteDevice(id); err != nil {
		return err
	}
	s.store.AddAudit("删除设备", id)
	return nil
}

func (s *WeftService) RenameDevice(id, name string) error {
	if err := s.cp.RenameDevice(id, name); err != nil {
		return err
	}
	s.store.AddAudit("重命名设备", id+" → "+name)
	return nil
}

func (s *WeftService) SetDeviceTags(id, tags string) error {
	if err := s.cp.SetDeviceTags(id, normalizeTags(tags)); err != nil {
		return err
	}
	s.store.AddAudit("设置设备标签", id+" · "+tags)
	return nil
}

func (s *WeftService) ExpireDevice(id string) error {
	if err := s.cp.ExpireDevice(id); err != nil {
		return err
	}
	s.store.AddAudit("设备登出", id)
	return nil
}

// DisableDeviceExpiry 取消密钥到期时间，让节点长期有效。
func (s *WeftService) DisableDeviceExpiry(id string) error {
	if err := s.cp.DisableExpiry(id); err != nil {
		return err
	}
	s.store.AddAudit("取消设备到期", id)
	return nil
}

func (s *WeftService) ApproveRoutes(ctx context.Context, id, routes string) error {
	if !s.callerAllowed(ctx) {
		return errNotMainWindow
	}
	if err := s.cp.ApproveRoutes(id, splitList(routes)); err != nil {
		return err
	}
	s.store.AddAudit("批准路由", id+" · "+routes)
	return nil
}

// BackfillIPs 为历史节点补齐缺失的 IP，返回受影响节点数。
func (s *WeftService) BackfillIPs(confirmed bool) (int, error) {
	n, err := s.cp.BackfillIPs(confirmed)
	if err != nil {
		return 0, err
	}
	s.store.AddAudit("补齐节点 IP", fmt.Sprintf("%d 个节点", n))
	return n, nil
}

// DebugCreateNode 直接建一个调试节点，仅用于联调 ACL / 路由。
func (s *WeftService) DebugCreateNode(user, key, name, routes string) (string, error) {
	d, err := s.cp.DebugCreateNode(user, key, name, splitList(routes))
	if err != nil {
		return "", err
	}
	s.store.AddAudit("调试建节点", name)
	return wrapOne(d)
}

// ---------- 节点认证 ----------

func (s *WeftService) RegisterAuth(user, authID string) (string, error) {
	d, err := s.cp.RegisterAuth(user, authID)
	if err != nil {
		return "", err
	}
	s.store.AddAudit("认证并注册节点", d.Name)
	return wrapOne(d)
}

func (s *WeftService) ApproveAuth(authID string) error {
	if err := s.cp.ApproveAuth(authID); err != nil {
		return err
	}
	s.store.AddAudit("批准节点认证", authID)
	return nil
}

func (s *WeftService) RejectAuth(authID string) error {
	if err := s.cp.RejectAuth(authID); err != nil {
		return err
	}
	s.store.AddAudit("拒绝节点认证", authID)
	return nil
}

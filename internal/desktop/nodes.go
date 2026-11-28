package desktop

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
		s.audit("注册节点", user, err)
		return "", err
	}
	s.audit("注册节点", d.Name, nil)
	return wrapOne(d)
}

func (s *WeftService) DeleteDevice(ctx context.Context, id string) error {
	if !s.callerAllowed(ctx) {
		return errNotMainWindow
	}
	if err := s.cp.DeleteDevice(id); err != nil {
		s.audit("删除设备", id, err)
		return err
	}
	s.audit("删除设备", id, nil)
	return nil
}

func (s *WeftService) RenameDevice(id, name string) error {
	if err := s.cp.RenameDevice(id, name); err != nil {
		s.audit("重命名设备", id+" → "+name, err)
		return err
	}
	s.audit("重命名设备", id+" → "+name, nil)
	return nil
}

func (s *WeftService) SetDeviceTags(id, tags string) error {
	if err := s.cp.SetDeviceTags(id, normalizeTags(tags)); err != nil {
		s.audit("设置设备标签", id+" · "+tags, err)
		return err
	}
	s.audit("设置设备标签", id+" · "+tags, nil)
	return nil
}

func (s *WeftService) ExpireDevice(id string) error {
	if err := s.cp.ExpireDevice(id); err != nil {
		s.audit("设备登出", id, err)
		return err
	}
	s.audit("设备登出", id, nil)
	return nil
}

// DisableDeviceExpiry 取消密钥到期时间，让节点长期有效。
func (s *WeftService) DisableDeviceExpiry(id string) error {
	if err := s.cp.DisableExpiry(id); err != nil {
		s.audit("取消设备到期", id, err)
		return err
	}
	s.audit("取消设备到期", id, nil)
	return nil
}

func (s *WeftService) ApproveRoutes(ctx context.Context, id, routes string) error {
	if !s.callerAllowed(ctx) {
		return errNotMainWindow
	}
	if err := s.cp.ApproveRoutes(id, splitList(routes)); err != nil {
		s.audit("批准路由", id+" · "+routes, err)
		return err
	}
	s.audit("批准路由", id+" · "+routes, nil)
	return nil
}

// BackfillIPs 为历史节点补齐缺失的 IP，返回受影响节点数。
func (s *WeftService) BackfillIPs(confirmed bool) (int, error) {
	n, err := s.cp.BackfillIPs(confirmed)
	if err != nil {
		s.audit("补齐节点 IP", "", err)
		return 0, err
	}
	s.audit("补齐节点 IP", fmt.Sprintf("%d 个节点", n), nil)
	return n, nil
}

// DebugCreateNode 直接建一个调试节点，仅用于联调 ACL / 路由。
func (s *WeftService) DebugCreateNode(user, key, name, routes string) (string, error) {
	d, err := s.cp.DebugCreateNode(user, key, name, splitList(routes))
	if err != nil {
		s.audit("调试建节点", name, err)
		return "", err
	}
	s.audit("调试建节点", name, nil)
	return wrapOne(d)
}

// ---------- 节点认证 ----------

func (s *WeftService) RegisterAuth(user, authID string) (string, error) {
	d, err := s.cp.RegisterAuth(user, authID)
	if err != nil {
		s.audit("认证并注册节点", authID, err)
		return "", err
	}
	s.audit("认证并注册节点", d.Name, nil)
	return wrapOne(d)
}

func (s *WeftService) ApproveAuth(authID string) error {
	if err := s.cp.ApproveAuth(authID); err != nil {
		s.audit("批准节点认证", authID, err)
		return err
	}
	s.audit("批准节点认证", authID, nil)
	return nil
}

func (s *WeftService) RejectAuth(authID string) error {
	if err := s.cp.RejectAuth(authID); err != nil {
		s.audit("拒绝节点认证", authID, err)
		return err
	}
	s.audit("拒绝节点认证", authID, nil)
	return nil
}

// ---------- 批量操作 ----------
// headscale 没有批量接口，这里逐个发。一个失败不中断：十几台里错一台，
// 剩下那些该办的还是要办，最后把失败的列出来。

// BatchFail 是一台没办成的设备。
type BatchFail struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Error string `json:"error"`
}

// BatchResult 给界面做汇总：成功几台、哪几台没成、为什么。
type BatchResult struct {
	Done   int         `json:"done"`
	Failed []BatchFail `json:"failed"`
}

// unit 是量词：设备论台，成员和 Key 论个。
func (r BatchResult) summary(what, unit string) string {
	if len(r.Failed) == 0 {
		return fmt.Sprintf("%s %d %s", what, r.Done, unit)
	}
	return fmt.Sprintf("%s %d %s，%d %s失败", what, r.Done, unit, len(r.Failed), unit)
}

func (r BatchResult) asError(unit string) error {
	if len(r.Failed) == 0 {
		return nil
	}
	first := r.Failed[0]
	return fmt.Errorf("%d %s失败，第一项「%s」：%s", len(r.Failed), unit, first.Name, first.Error)
}

// 设备列表只取一次：批量打标签要先读原有标签再合并，
// headscale 的 tags 接口是整体覆盖，写之前不合并会把已有的冲掉。
func (s *WeftService) deviceIndex() (map[string]model.Device, error) {
	list, err := s.cp.Devices("")
	if err != nil {
		return nil, err
	}
	out := make(map[string]model.Device, len(list))
	for _, d := range list {
		out[d.ID] = d
	}
	return out, nil
}

func (s *WeftService) TagDevices(ctx context.Context, ids []string, tags string, add bool) (string, error) {
	if !s.callerAllowed(ctx) {
		return "", errNotMainWindow
	}
	want := normalizeTags(tags)
	res := BatchResult{Failed: []BatchFail{}}
	index, err := s.deviceIndex()
	if err != nil {
		s.audit("批量打标签", fmt.Sprintf("%d 台", len(ids)), err)
		return "", err
	}
	for _, id := range ids {
		d, ok := index[id]
		if !ok {
			res.Failed = append(res.Failed, BatchFail{ID: id, Error: "设备已经不在了"})
			continue
		}
		next := mergeTags(splitList(d.Tags), want, add)
		if err := s.cp.SetDeviceTags(id, next); err != nil {
			res.Failed = append(res.Failed, BatchFail{ID: id, Name: d.Name, Error: err.Error()})
			continue
		}
		res.Done++
	}
	s.audit("批量打标签", res.summary("处理", "台"), res.asError("台"))
	return wrapOne(res)
}

func (s *WeftService) BatchExpiry(ctx context.Context, ids []string, expire bool) (string, error) {
	if !s.callerAllowed(ctx) {
		return "", errNotMainWindow
	}
	action := "批量取消到期"
	if expire {
		action = "批量登出设备"
	}
	res := BatchResult{Failed: []BatchFail{}}
	index, err := s.deviceIndex()
	if err != nil {
		s.audit(action, fmt.Sprintf("%d 台", len(ids)), err)
		return "", err
	}
	for _, id := range ids {
		d, ok := index[id]
		if !ok {
			res.Failed = append(res.Failed, BatchFail{ID: id, Error: "设备已经不在了"})
			continue
		}
		var err error
		if expire {
			err = s.cp.ExpireDevice(id)
		} else {
			err = s.cp.DisableExpiry(id)
		}
		if err != nil {
			res.Failed = append(res.Failed, BatchFail{ID: id, Name: d.Name, Error: err.Error()})
			continue
		}
		res.Done++
	}
	s.audit(action, res.summary("处理", "台"), res.asError("台"))
	return wrapOne(res)
}

package desktop

import (
	"context"
	"fmt"
	"strings"

	"github.com/boothua/weft/internal/model"
)

// 成员与密钥：用户、授权 Key、管理 Key、ACL 策略、服务端健康。

func (s *WeftService) ListUsers() (string, error) {
	if msg := s.manageErr(); msg != "" {
		return toJSON(model.Cached[model.User]{Items: []model.User{}, Source: "none", Error: msg})
	}
	return cachedList(s.store, s.cacheKey("users"), s.cp.Users)
}

func (s *WeftService) CreateUser(ctx context.Context, name, displayName, email, pictureURL string) (string, error) {
	if !s.callerAllowed(ctx) {
		return "", errNotMainWindow
	}
	u, err := s.cp.CreateUser(name, displayName, email, pictureURL)
	if err != nil {
		return "", err
	}
	s.store.AddAudit("创建成员", u.RawName)
	return wrapOne(u)
}

func (s *WeftService) RenameUser(ctx context.Context, id, name string) error {
	if !s.callerAllowed(ctx) {
		return errNotMainWindow
	}
	if err := s.cp.RenameUser(id, name); err != nil {
		return err
	}
	s.store.AddAudit("重命名成员", id+" → "+name)
	return nil
}

func (s *WeftService) DeleteUser(ctx context.Context, id string) error {
	if !s.callerAllowed(ctx) {
		return errNotMainWindow
	}
	if err := s.cp.DeleteUser(id); err != nil {
		return err
	}
	s.store.AddAudit("删除成员", id)
	return nil
}

// ---------- 授权 Key ----------

func (s *WeftService) ListAuthKeys() (string, error) {
	if msg := s.manageErr(); msg != "" {
		return toJSON(model.Cached[model.AuthKey]{Items: []model.AuthKey{}, Source: "none", Error: msg})
	}
	return cachedList(s.store, s.cacheKey("authkeys"), s.cp.AuthKeys)
}

func (s *WeftService) CreateAuthKey(ctx context.Context, user string, reusable, ephemeral bool, days int, tags string) (string, error) {
	if !s.callerAllowed(ctx) {
		return "", errNotMainWindow
	}
	key, err := s.cp.CreateAuthKey(user, reusable, ephemeral, days, normalizeTags(tags))
	if err != nil {
		return "", err
	}
	s.store.AddAudit("生成授权 Key", user)
	return key, nil
}

func (s *WeftService) ExpireAuthKey(id string) error {
	if err := s.cp.ExpireAuthKey(id); err != nil {
		return err
	}
	s.store.AddAudit("失效授权 Key", id)
	return nil
}

func (s *WeftService) DeleteAuthKey(ctx context.Context, id string) error {
	if !s.callerAllowed(ctx) {
		return errNotMainWindow
	}
	if err := s.cp.DeleteAuthKey(id); err != nil {
		return err
	}
	s.store.AddAudit("删除授权 Key", id)
	return nil
}

// ---------- API Key ----------

func (s *WeftService) ListAPIKeys() (string, error) {
	if msg := s.manageErr(); msg != "" {
		return toJSON(model.Cached[model.APIKey]{Items: []model.APIKey{}, Source: "none", Error: msg})
	}
	return cachedList(s.store, s.cacheKey("apikeys"), s.cp.APIKeys)
}

func (s *WeftService) CreateAPIKey(ctx context.Context, days int) (string, error) {
	if !s.callerAllowed(ctx) {
		return "", errNotMainWindow
	}
	key, err := s.cp.CreateAPIKey(days)
	if err != nil {
		return "", err
	}
	s.store.AddAudit("创建 API Key", fmt.Sprintf("%d 天", days))
	return key, nil
}

func (s *WeftService) ExpireAPIKey(prefix, id string) error {
	if err := s.cp.ExpireAPIKey(prefix, id); err != nil {
		return err
	}
	s.store.AddAudit("失效 API Key", prefix)
	return nil
}

func (s *WeftService) DeleteAPIKey(ctx context.Context, prefix, id string) error {
	if !s.callerAllowed(ctx) {
		return errNotMainWindow
	}
	if err := s.cp.DeleteAPIKey(prefix, id); err != nil {
		return err
	}
	s.store.AddAudit("删除 API Key", prefix)
	return nil
}

// ---------- 策略 ----------

func (s *WeftService) GetPolicy() (string, error) {
	p, err := s.cp.Policy()
	if err != nil {
		return "", err
	}
	return wrapOne(p)
}

func (s *WeftService) SavePolicy(ctx context.Context, content string) error {
	if !s.callerAllowed(ctx) {
		return errNotMainWindow
	}
	// 先把线上现有的策略记下来，再覆盖。历史里存的是「曾经生效过」的版本，
	// 存新版本没有意义——它就在服务端上。
	old := ""
	if p, err := s.cp.Policy(); err == nil {
		old = p.Content
	}
	if err := s.cp.SavePolicy(content); err != nil {
		return err
	}
	s.store.AddAudit("保存 ACL 策略", fmt.Sprintf("%d 字节", len(content)))
	if old != "" && strings.TrimSpace(old) != strings.TrimSpace(content) {
		_ = s.store.AddPolicyHistory(old, "")
		_ = s.store.PrunePolicyHistory(20)
	}
	return nil
}

// ListPolicyHistory 返回最近保存前的版本，新的在前。
func (s *WeftService) ListPolicyHistory() (string, error) {
	items, err := s.store.ListPolicyHistory(20)
	if err != nil {
		return "", err
	}
	return wrapOne(items)
}

// GetPolicyVersion 取某一版原文，用于查看和对比。
func (s *WeftService) GetPolicyVersion(id int64) (string, error) {
	body, err := s.store.GetPolicyVersion(id)
	if err != nil {
		return "", err
	}
	return wrapOne(model.Policy{Content: body})
}

// RestorePolicy 把历史版本写回服务端。写之前当前版本会进历史，所以回滚也能后悔。
func (s *WeftService) RestorePolicy(ctx context.Context, id int64) error {
	if !s.callerAllowed(ctx) {
		return errNotMainWindow
	}
	body, err := s.store.GetPolicyVersion(id)
	if err != nil {
		return err
	}
	if err := s.SavePolicy(ctx, body); err != nil {
		return err
	}
	s.store.AddAudit("回滚 ACL 策略", fmt.Sprintf("回到第 %d 版", id))
	return nil
}

// CheckPolicy 只校验语法，不写入服务端。
func (s *WeftService) CheckPolicy(content string) error {
	return s.cp.CheckPolicy(content)
}

// ---------- 服务端状态 ----------

func (s *WeftService) GetHealth() (string, error) {
	return wrap(s.cp.Health())
}

package desktop

import (
	"context"
	"fmt"
	"strings"

	"github.com/boothua/weft/internal/model"
	"github.com/boothua/weft/internal/store"
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
		s.audit("创建成员", name, err)
		return "", err
	}
	s.audit("创建成员", u.RawName, nil)
	return wrapOne(u)
}

func (s *WeftService) RenameUser(ctx context.Context, id, name string) error {
	if !s.callerAllowed(ctx) {
		return errNotMainWindow
	}
	if err := s.cp.RenameUser(id, name); err != nil {
		s.audit("重命名成员", id+" → "+name, err)
		return err
	}
	s.audit("重命名成员", id+" → "+name, nil)
	return nil
}

func (s *WeftService) DeleteUser(ctx context.Context, id string) error {
	if !s.callerAllowed(ctx) {
		return errNotMainWindow
	}
	if err := s.cp.DeleteUser(id); err != nil {
		s.audit("删除成员", id, err)
		return err
	}
	s.audit("删除成员", id, nil)
	return nil
}

// DeleteUsers 批量删除成员。headscale 没有批量接口，逐个删；
// 某个删不动（名下还有设备）就记下来继续，最后一起说。
func (s *WeftService) DeleteUsers(ctx context.Context, ids []string) (string, error) {
	if !s.callerAllowed(ctx) {
		return "", errNotMainWindow
	}
	res := BatchResult{Failed: []BatchFail{}}
	names := map[string]string{}
	if list, err := s.cp.Users(); err == nil {
		for _, u := range list {
			names[u.ID] = u.RawName
			if names[u.ID] == "" {
				names[u.ID] = u.Name
			}
		}
	}
	for _, id := range ids {
		if err := s.cp.DeleteUser(id); err != nil {
			res.Failed = append(res.Failed, BatchFail{ID: id, Name: names[id], Error: err.Error()})
			continue
		}
		res.Done++
	}
	s.audit("批量删除成员", res.summary("处理", "个"), res.asError("个"))
	return wrapOne(res)
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
		s.audit("生成授权 Key", user, err)
		return "", err
	}
	s.audit("生成授权 Key", user, nil)
	return key, nil
}

func (s *WeftService) ExpireAuthKey(id string) error {
	if err := s.cp.ExpireAuthKey(id); err != nil {
		s.audit("失效授权 Key", id, err)
		return err
	}
	s.audit("失效授权 Key", id, nil)
	return nil
}

func (s *WeftService) DeleteAuthKey(ctx context.Context, id string) error {
	if !s.callerAllowed(ctx) {
		return errNotMainWindow
	}
	if err := s.cp.DeleteAuthKey(id); err != nil {
		s.audit("删除授权 Key", id, err)
		return err
	}
	s.audit("删除授权 Key", id, nil)
	return nil
}

// BatchAuthKeys 批量吊销 / 删除授权 Key。op 取 expire 或 delete，
// 界面按下的按钮不一样，做的事就是单台那两个接口，逐个来。
func (s *WeftService) BatchAuthKeys(ctx context.Context, ids []string, op string) (string, error) {
	if !s.callerAllowed(ctx) {
		return "", errNotMainWindow
	}
	if op != "expire" && op != "delete" {
		return "", fmt.Errorf("不认识的操作：%s", op)
	}
	action := "批量吊销授权 Key"
	if op == "delete" {
		action = "批量删除授权 Key"
	}
	res := BatchResult{Failed: []BatchFail{}}
	keys := map[string]string{}
	if list, err := s.cp.AuthKeys(); err == nil {
		for _, k := range list {
			keys[k.ID] = k.Key
		}
	}
	for _, id := range ids {
		var err error
		if op == "expire" {
			err = s.cp.ExpireAuthKey(id)
		} else {
			err = s.cp.DeleteAuthKey(id)
		}
		if err != nil {
			res.Failed = append(res.Failed, BatchFail{ID: id, Name: keys[id], Error: err.Error()})
			continue
		}
		res.Done++
	}
	s.audit(action, res.summary("处理", "个"), res.asError("个"))
	return wrapOne(res)
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
		s.audit("创建 API Key", fmt.Sprintf("%d 天", days), err)
		return "", err
	}
	s.audit("创建 API Key", fmt.Sprintf("%d 天", days), nil)
	return key, nil
}

func (s *WeftService) ExpireAPIKey(prefix, id string) error {
	if err := s.cp.ExpireAPIKey(prefix, id); err != nil {
		s.audit("失效 API Key", prefix, err)
		return err
	}
	s.audit("失效 API Key", prefix, nil)
	return nil
}

func (s *WeftService) DeleteAPIKey(ctx context.Context, prefix, id string) error {
	if !s.callerAllowed(ctx) {
		return errNotMainWindow
	}
	if err := s.cp.DeleteAPIKey(prefix, id); err != nil {
		s.audit("删除 API Key", prefix, err)
		return err
	}
	s.audit("删除 API Key", prefix, nil)
	return nil
}

// KeyRef 是批量操作要用的最小信息：吊销认 id，删除认前缀，名字只为报错好认。
type KeyRef struct {
	Prefix string `json:"prefix"`
	ID     string `json:"id"`
	Name   string `json:"name"`
}

// BatchAPIKeys 批量吊销 / 删除 API Key。吊销优先用 id，删除只能用前缀，
// 所以界面把两个都传上来，别在这里再猜一次。
func (s *WeftService) BatchAPIKeys(ctx context.Context, keys []KeyRef, op string) (string, error) {
	if !s.callerAllowed(ctx) {
		return "", errNotMainWindow
	}
	if op != "expire" && op != "delete" {
		return "", fmt.Errorf("不认识的操作：%s", op)
	}
	action := "批量吊销 API Key"
	if op == "delete" {
		action = "批量删除 API Key"
	}
	res := BatchResult{Failed: []BatchFail{}}
	for _, k := range keys {
		var err error
		if op == "expire" {
			err = s.cp.ExpireAPIKey(k.Prefix, k.ID)
		} else {
			err = s.cp.DeleteAPIKey(k.Prefix, k.ID)
		}
		if err != nil {
			res.Failed = append(res.Failed, BatchFail{ID: k.ID, Name: k.Name, Error: err.Error()})
			continue
		}
		res.Done++
	}
	s.audit(action, res.summary("处理", "个"), res.asError("个"))
	return wrapOne(res)
}

// ---------- 策略 ----------

func (s *WeftService) GetPolicy() (string, error) {
	p, err := s.cp.Policy()
	if err != nil {
		// 服务端连不上时给最后一次拿到的原文。策略是改坏代价最大的东西，
		// 界面上宁可标着「这是某时的缓存」，也别让人对着空白页发呆
		var cached model.Policy
		if ts, ok := s.store.CacheRead(s.cacheKey("policy"), &cached); ok {
			cached.UpdatedAt = fmtTime(parseTS(ts))
			cached.Source = "cache"
			return wrapOne(cached)
		}
		return "", err
	}
	p.Source = "live"
	_ = s.store.CachePut(s.cacheKey("policy"), p)
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
		s.audit("保存 ACL 策略", fmt.Sprintf("%d 字节", len(content)), err)
		return err
	}
	s.audit("保存 ACL 策略", fmt.Sprintf("%d 字节", len(content)), nil)
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
		s.audit("回滚 ACL 策略", fmt.Sprintf("回到第 %d 版", id), err)
		return err
	}
	s.audit("回滚 ACL 策略", fmt.Sprintf("回到第 %d 版", id), nil)
	return nil
}

// CheckPolicy 只校验语法，不写入服务端。
func (s *WeftService) CheckPolicy(content string) error {
	return s.cp.CheckPolicy(content)
}

// ---------- 策略模板 ----------

// ListPolicyTemplates 返回全部模板，带原文：模板不大，一次取回去，
// 应用的时候不用再发一次请求。
func (s *WeftService) ListPolicyTemplates() (string, error) {
	items, err := s.store.ListPolicyTemplates()
	if err != nil {
		return "", err
	}
	return wrapOne(items)
}

// SavePolicyTemplate 把当前策略（含未保存的改动）存成模板。同名直接覆盖。
func (s *WeftService) SavePolicyTemplate(ctx context.Context, name, content string) (string, error) {
	if !s.callerAllowed(ctx) {
		return "", errNotMainWindow
	}
	if strings.TrimSpace(content) == "" {
		return "", fmt.Errorf("策略是空的，没什么可存的")
	}
	id, err := s.store.SavePolicyTemplate(name, content, s.profileName())
	if err != nil {
		s.audit("保存策略模板", name, err)
		return "", err
	}
	s.audit("保存策略模板", name, nil)
	return wrapOne(map[string]any{"id": id, "name": strings.TrimSpace(name)})
}

func (s *WeftService) DeletePolicyTemplate(ctx context.Context, id int64) error {
	if !s.callerAllowed(ctx) {
		return errNotMainWindow
	}
	if err := s.store.DeletePolicyTemplate(id); err != nil {
		s.audit("删除策略模板", fmt.Sprintf("第 %d 份", id), err)
		return err
	}
	s.audit("删除策略模板", fmt.Sprintf("第 %d 份", id), nil)
	return nil
}

// profileName 是当前服务端条目的显示名，没起名字就用域名。
func (s *WeftService) profileName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.cfg.Profiles {
		if p.ID == s.cfg.ActiveID {
			return p.Name
		}
	}
	return store.ProfileLabel(s.cfg.BaseURL, "默认")
}

// ---------- 服务端状态 ----------

func (s *WeftService) GetHealth() (string, error) {
	return wrap(s.cp.Health())
}

package desktop

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/boothua/weft/internal/controlplane"
	"github.com/boothua/weft/internal/model"
)

// 假 headscale：GET 给当前策略，PUT 换掉。
type fakePolicyServer struct {
	content string
	saves   int
}

func (f *fakePolicyServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/policy", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]string{
				"policy": f.content, "updatedAt": "2026-11-28T10:00:00Z",
			})
		case http.MethodPut:
			var body struct {
				Policy string `json:"policy"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f.content = body.Policy
			f.saves++
		}
	})
	return mux
}

func newPolicyTestService(t *testing.T, current string) (*WeftService, *fakePolicyServer) {
	t.Helper()
	fake := &fakePolicyServer{content: current}
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)

	store := newTestStore(t)
	cfg := model.Config{BaseURL: srv.URL, ControlPlane: srv.URL}
	svc := &WeftService{store: store, cfg: cfg, cp: controlplane.New(cfg)}
	return svc, fake
}

func TestSavePolicyKeepsPreviousVersion(t *testing.T) {
	oldBody := `{"acls":[{"action":"accept","src":["*"],"dst":["*:*"]}]}`
	svc, fake := newPolicyTestService(t, oldBody)

	newBody := "{\n  \"acls\": []\n}\n"
	if err := svc.SavePolicy(context.Background(), newBody); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if fake.content != newBody {
		t.Fatalf("服务端内容没换: %q", fake.content)
	}

	items, err := svc.store.ListPolicyHistory(20)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("历史应该只有 1 版, 有 %d", len(items))
	}
	if items[0].Size != len(oldBody) || items[0].Lines != 1 {
		t.Fatalf("摘要不对: %+v", items[0])
	}
	got, err := svc.store.GetPolicyVersion(items[0].ID)
	if err != nil || got != oldBody {
		t.Fatalf("取回的旧版本不对: %q %v", got, err)
	}
}

func TestSavePolicySameContentNoHistory(t *testing.T) {
	body := "{\"acls\":[]}"
	svc, _ := newPolicyTestService(t, body)
	if err := svc.SavePolicy(context.Background(), body); err != nil {
		t.Fatal(err)
	}
	items, _ := svc.store.ListPolicyHistory(20)
	if len(items) != 0 {
		t.Fatalf("内容没变不该记历史, 有 %d 版", len(items))
	}
}

func TestRestorePolicy(t *testing.T) {
	first := "{\"acls\":[{\"action\":\"accept\",\"src\":[\"*\"],\"dst\":[\"*:*\"]}]}"
	svc, fake := newPolicyTestService(t, first)

	// 改成空策略，旧的那版进历史
	if err := svc.SavePolicy(context.Background(), "{\"acls\":[]}"); err != nil {
		t.Fatal(err)
	}
	items, _ := svc.store.ListPolicyHistory(20)
	if len(items) != 1 {
		t.Fatalf("应该有一版历史, 有 %d", len(items))
	}

	// 回滚
	if err := svc.RestorePolicy(context.Background(), items[0].ID); err != nil {
		t.Fatalf("回滚失败: %v", err)
	}
	if strings.TrimSpace(fake.content) != first {
		t.Fatalf("服务端没回到旧策略: %q", fake.content)
	}
	// 回滚前的空策略也应该进历史，回滚本身可以后悔
	items, _ = svc.store.ListPolicyHistory(20)
	if len(items) != 2 {
		t.Fatalf("回滚后应该有 2 版历史, 有 %d", len(items))
	}
}

func TestPrunePolicyHistory(t *testing.T) {
	store := newTestStore(t)
	for i := 0; i < 25; i++ {
		if err := store.AddPolicyHistory("v"+string(rune('a'+i)), ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PrunePolicyHistory(20); err != nil {
		t.Fatal(err)
	}
	items, _ := store.ListPolicyHistory(100)
	if len(items) != 20 {
		t.Fatalf("应该留 20 版, 留了 %d", len(items))
	}
	// 最老的那批被删掉，最新的还在
	body, err := store.GetPolicyVersion(items[0].ID)
	if err != nil || body != "vy" {
		t.Fatalf("最新一版不对: %q %v", body, err)
	}
}

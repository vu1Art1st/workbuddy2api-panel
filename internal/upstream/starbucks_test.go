package upstream

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

func TestStarbucksTasksParsesProgressAndInvite(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/portal/activity/tasks" {
			t.Errorf("path=%q want /portal/activity/tasks", r.URL.Path)
		}
		if got := r.URL.Query().Get("activity_code"); got != starbucksActivityCode {
			t.Errorf("activity_code=%q want %q", got, starbucksActivityCode)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer at" {
			t.Errorf("Authorization=%q", got)
		}
		if got := r.Header.Get("X-User-Id"); got != "u1" {
			t.Errorf("X-User-Id=%q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0, "msg": "OK",
			"data": map[string]any{
				"tasks": []map[string]any{
					{"task_code": "ai_chat", "title": "简单任务，AI 搞定", "target_count": 3, "enabled": 1},
					{"task_code": "invite", "title": "邀请好友", "target_count": 1, "enabled": 1},
				},
				"progress": []map[string]any{
					{"task_code": "ai_chat", "progress": 2, "viewed_at": "2026-11-12T12:00:00+08:00"},
				},
				"invite": map[string]any{"count": 1, "need": 3, "open": true},
			},
		})
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), BillingBaseCN: srv.URL}
	tasks, err := c.StarbucksTasks(&auth.Auth{UID: "u1", AccessToken: "at"})
	if err != nil {
		t.Fatalf("StarbucksTasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("tasks=%d want 2", len(tasks))
	}
	if tasks[0].TaskCode != "ai_chat" || tasks[0].Current != 2 || tasks[0].Target != 3 || !tasks[0].Viewed {
		t.Errorf("ai_chat=%+v", tasks[0])
	}
	if tasks[1].TaskCode != "invite" || tasks[1].Current != 1 || tasks[1].Target != 3 || tasks[1].Locked {
		t.Errorf("invite=%+v", tasks[1])
	}
}

func TestStarbucksConfigReadsActivityWindow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/portal/activity/config" {
			t.Errorf("path=%q", r.URL.Path)
		}
		if got := r.URL.Query().Get("activity_code"); got != starbucksActivityCode {
			t.Errorf("activity_code=%q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0, "msg": "OK",
			"data": map[string]any{"config": map[string]any{
				"activity_code": starbucksActivityCode,
				"title":         "星巴克协作",
				"in_period":     true,
			}},
		})
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), BillingBaseCN: srv.URL}
	cfg, err := c.StarbucksConfig()
	if err != nil {
		t.Fatalf("StarbucksConfig: %v", err)
	}
	if !cfg.InPeriod || cfg.ActivityCode != starbucksActivityCode || cfg.Title != "星巴克协作" {
		t.Errorf("config=%+v", cfg)
	}
}

func TestStarbucksTaskClaimUsesActivityEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method=%s want POST", r.Method)
		}
		if r.URL.Path != "/portal/activity/tasks/expert/claim" {
			t.Errorf("path=%q", r.URL.Path)
		}
		if got := r.URL.Query().Get("activity_code"); got != starbucksActivityCode {
			t.Errorf("activity_code=%q", got)
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"OK","data":{}}`))
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), BillingBaseCN: srv.URL}
	if err := c.StarbucksTaskClaim(&auth.Auth{UID: "u1", AccessToken: "at"}, "expert"); err != nil {
		t.Fatalf("StarbucksTaskClaim: %v", err)
	}
}

func TestStarbucksChatEventUsesLocalMiniProgramShape(t *testing.T) {
	ev := StarbucksChatEvent("conv-1")
	if ev["eventCode"] != "chat_request_send" {
		t.Errorf("eventCode=%v", ev["eventCode"])
	}
	if ev["ideName"] != "wx_app_local" || ev["source"] != "mini_program" {
		t.Errorf("fingerprint ideName=%v source=%v", ev["ideName"], ev["source"])
	}
	if ev["conversationId"] != "conv-1" || ev["requestId"] == "" || ev["traceId"] != ev["requestId"] {
		t.Errorf("conversation/request fields=%v", ev)
	}
	if ev["requestModelId"] != "" || ev["requestModelName"] != "" {
		t.Errorf("model fields=%v/%v", ev["requestModelId"], ev["requestModelName"])
	}
}

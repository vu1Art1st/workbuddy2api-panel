package panel

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// TestTasksScanAllIncludesManualActivityTasks 回归两件事：
//   - 新活动任务即使没有自动动作，也必须出现在扫描待办中；
//   - token_source=enterprise_switch 只是取证渠道标记，不能把个人账号从扫描中跳过。
func TestTasksScanAllIncludesManualActivityTasks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/activity/growth/tasks":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "msg": "OK",
				"data": map[string]any{"tasks": []map[string]any{
					{"task_code": "expert_use_douyinyin", "title": "体验「抖音策略师」", "target": 1,
						"current": 0, "accept_status": "accepted"},
					{"task_code": "starbucks_activity", "title": "星巴克活动任务", "target": 1,
						"current": 0, "accept_status": "accepted"},
					{"task_code": "chat_5", "title": "和 AI 聊天 5 次", "target": 5,
						"current": 0, "accept_status": "accepted"},
					{"task_code": "already_claimed", "target": 1, "current": 1, "accept_status": "claimed"},
					{"task_code": "daily_locked", "target": 1, "current": 0, "accept_status": "accepted", "locked": true},
				}},
			})
		case "/portal/activity/config":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "msg": "OK",
				"data": map[string]any{"config": map[string]any{
					"activity_code": "campaign_starbucks_2026",
					"in_period":     true,
				}},
			})
		case "/portal/activity/tasks":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "msg": "OK",
				"data": map[string]any{
					"tasks": []map[string]any{
						{"task_code": "ai_chat", "title": "简单任务，AI 搞定", "target_count": 3, "reward_credit": 100, "need_view": true, "enabled": 1},
						{"task_code": "expert", "title": "复杂任务，专家帮忙", "target_count": 1, "reward_credit": 100, "need_view": true, "enabled": 1},
						{"task_code": "app", "title": "解锁 App 强大能力", "target_count": 1, "reward_credit": 100, "need_view": true, "enabled": 1},
						{"task_code": "invite", "title": "邀请好友", "target_count": 1, "enabled": 1},
					},
					"progress": []map[string]any{
						{"task_code": "expert", "progress": 1, "completed_at": "2026-11-12T12:00:00+08:00"},
					},
					"invite": map[string]any{"count": 0, "need": 3, "open": false},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	pl := pool.New("")
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"token_source":"enterprise_switch"}`))
	pl.Add(&auth.Auth{UID: "u1", Nickname: "n1", AccessToken: "e30." + payload + ".sig"})
	p := &Panel{cfg: Config{
		Pool: pl,
		Upstream: &upstream.Client{
			HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL, WebBaseCN: srv.URL,
		},
	}}

	rec := httptest.NewRecorder()
	p.tasksScanAll(rec, httptest.NewRequest(http.MethodPost, "/panel/api/tasks/scan_all", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	var got struct {
		Accounts []scanAccountItem `json:"accounts"`
		Pending  int               `json:"pending_count"`
		Auto     int               `json:"executable_count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Pending != 6 || got.Auto != 1 {
		t.Fatalf("pending/auto=%d/%d, want 6/1; body=%s", got.Pending, got.Auto, rec.Body.String())
	}
	if len(got.Accounts) != 1 || len(got.Accounts[0].Growth) != 3 || len(got.Accounts[0].Starbucks) != 3 {
		t.Fatalf("accounts=%+v, want one account with 3 growth + 3 starbucks tasks", got.Accounts)
	}

	seen := map[string]scanTaskView{}
	for _, task := range got.Accounts[0].Growth {
		seen[task.TaskCode] = task
	}
	for _, code := range []string{"expert_use_douyinyin", "starbucks_activity"} {
		task, ok := seen[code]
		if !ok {
			t.Errorf("扫描漏掉手动任务 %s; got=%v", code, seen)
			continue
		}
		if task.Executable {
			t.Errorf("%s 不应被标记为可自动执行", code)
		}
	}
	if task, ok := seen["chat_5"]; !ok || !task.Executable {
		t.Errorf("chat_5 应保留且 executable=true; got=%+v", task)
	}
	if _, ok := seen["already_claimed"]; ok {
		t.Error("已领取任务不应出现在待办")
	}
	if _, ok := seen["daily_locked"]; ok {
		t.Error("locked 任务不应出现在待办")
	}

	sb := map[string]scanStarbucksView{}
	for _, task := range got.Accounts[0].Starbucks {
		sb[task.TaskCode] = task
	}
	for _, code := range []string{"ai_chat", "app"} {
		if task, ok := sb[code]; !ok || task.Executable {
			t.Errorf("星巴克手动任务 %s 应展示且不可自动执行; got=%+v", code, task)
		}
	}
	if task, ok := sb["expert"]; !ok || task.Executable || !task.Completed {
		t.Errorf("星巴克 expert 已完成任务应可手动领奖且不进入队列; got=%+v", task)
	}
	if _, ok := sb["invite"]; ok {
		t.Error("未开放的星巴克邀请任务不应出现在待办")
	}
}

// TestStartGrowthQueueSkipsManualOnlyTasks 扫描展示手动任务，但不能把它们塞进
// 自动执行队列（否则 runGrowthQueued 会因无动作直接报错）。
func TestStartGrowthQueueSkipsManualOnlyTasks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/activity/growth/tasks" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0, "msg": "OK",
			"data": map[string]any{"tasks": []map[string]any{
				{"task_code": "expert_use_douyinyin", "target": 1, "current": 0, "accept_status": "accepted"},
			}},
		})
	}))
	defer srv.Close()

	pl := pool.New("")
	pl.Add(&auth.Auth{UID: "u1", Nickname: "n1", AccessToken: "at"})
	p := &Panel{cfg: Config{
		Pool: pl,
		Upstream: &upstream.Client{
			HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL, WebBaseCN: srv.URL,
		},
	}}

	started, total, seq, msg := p.startGrowthQueue(1, true)
	if started || total != 0 || seq != 0 {
		t.Fatalf("started/total/seq=%v/%d/%d, want false/0/0", started, total, seq)
	}
	if !strings.Contains(msg, "可自动执行") {
		t.Fatalf("message=%q, 应明确说明没有可自动执行待办", msg)
	}
}

func TestClaimStarbucksTaskClaimsCompletedTask(t *testing.T) {
	var lists int32
	var claims int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/portal/activity/tasks":
			n := atomic.AddInt32(&lists, 1)
			progress := map[string]any{"task_code": "expert", "progress": 1}
			if n > 1 {
				progress["claimed_at"] = "2026-11-12T12:05:00+08:00"
			} else {
				progress["completed_at"] = "2026-11-12T12:00:00+08:00"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "msg": "OK",
				"data": map[string]any{
					"tasks": []map[string]any{
						{"task_code": "expert", "title": "复杂任务，专家帮忙", "target_count": 1,
							"reward_credit": 100, "enabled": 1},
					},
					"progress": []map[string]any{progress},
				},
			})
		case r.URL.Path == "/portal/activity/tasks/expert/claim":
			atomic.AddInt32(&claims, 1)
			_, _ = w.Write([]byte(`{"code":0,"msg":"OK","data":{}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	p := &Panel{cfg: Config{Upstream: &upstream.Client{
		HTTP: srv.Client(), BillingBaseCN: srv.URL,
	}}}
	credit, chance, claimed, msg, err := p.claimStarbucksTask(&auth.Auth{UID: "u1", AccessToken: "at"}, "expert")
	if err != nil {
		t.Fatalf("claimStarbucksTask: %v", err)
	}
	if got := atomic.LoadInt32(&claims); got != 1 {
		t.Fatalf("claim calls=%d want 1", got)
	}
	if !strings.Contains(msg, "已领取") || !strings.Contains(msg, "100") {
		t.Fatalf("message=%q, 应包含领奖结果与积分", msg)
	}
	if !claimed || credit != 100 || chance != 0 {
		t.Fatalf("claimed/credit/chance=%v/%d/%d, want true/100/0", claimed, credit, chance)
	}
}

func TestValorantExpertActionRegistered(t *testing.T) {
	codes := []string{
		"WorkBuddy_Valorant",
		"expert_use_maidehao",
		"expert_use_douyinyin",
		"expert_use_shangjia_xuanchuan",
		"expert_use_bowangwang",
	}
	for _, code := range codes {
		act := autoActionFor(code)
		if act == nil || act.run == nil {
			t.Fatalf("%s 必须注册为可手动执行任务", code)
		}
		if !act.ManualOnly {
			t.Errorf("%s 必须标记 ManualOnly", code)
		}
	}
	if act := autoActionFor("skill_share"); act == nil || !act.ManualOnly || act.run == nil {
		t.Fatal("skill_share 必须注册为手动执行任务")
	}
	act := autoActionFor("WorkBuddy_Valorant")
	if !strings.Contains(act.Desc, "无畏契约拼豆专家") {
		t.Fatalf("desc=%q", act.Desc)
	}
	jump := "https://www.workbuddy.cn/work/launch/?deeplink=workbuddy%3A%2F%2Fexperts%3FexpertId%3Dex_FbY5nlWTqDod&buddy_type=workbuddy"
	if got := expertIDFromJumpURL(jump); got != "ex_FbY5nlWTqDod" {
		t.Fatalf("expertIDFromJumpURL=%q", got)
	}
}

func TestManualOnlyExpertTasksExcludedFromQueue(t *testing.T) {
	for _, code := range []string{"WorkBuddy_Valorant", "expert_use_douyinyin"} {
		task := upstream.Task{TaskCode: code, Target: 1, Current: 0, AcceptStatus: "accepted"}
		if growthAuto(task) {
			t.Errorf("%s 不应进入自动队列", code)
		}
		if !isManualOnlyTask(code) {
			t.Errorf("%s 应标记为 manual-only", code)
		}
	}
}

func TestRunStarbucksExpertTask(t *testing.T) {
	oldGap, oldAttempts := starbucksPollGap, starbucksPollAttempts
	starbucksPollGap, starbucksPollAttempts = 0, 1
	t.Cleanup(func() { starbucksPollGap, starbucksPollAttempts = oldGap, oldAttempts })

	var viewed, reports int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/portal/activity/tasks/expert/viewed":
			atomic.AddInt32(&viewed, 1)
			_, _ = w.Write([]byte(`{"code":0,"msg":"OK","data":{}}`))
		case r.URL.Path == "/portal/operation-platform/market/expert/list":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "msg": "OK",
				"data": map[string]any{"experts": []map[string]any{
					{"expert_id": "ex_test", "expert_type": "agent", "display_name_zh": "测试专家", "version": "1.0.0"},
				}},
			})
		case r.URL.Path == "/v2/report":
			atomic.AddInt32(&reports, 1)
			_, _ = w.Write([]byte(`{"code":0,"msg":"OK","data":{}}`))
		case r.URL.Path == "/portal/activity/tasks":
			cur := int64(0)
			if atomic.LoadInt32(&reports) > 0 {
				cur = 1
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "msg": "OK",
				"data": map[string]any{
					"tasks":    []map[string]any{{"task_code": "expert", "title": "专家", "target_count": 1, "enabled": 1}},
					"progress": []map[string]any{{"task_code": "expert", "progress": cur, "completed_at": "2026-11-12T12:00:00+08:00"}},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	p := &Panel{cfg: Config{Upstream: &upstream.Client{
		HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL,
	}}}
	msg, err := p.runStarbucksTask(&auth.Auth{UID: "u1", AccessToken: "at", Nickname: "n1"},
		&upstream.StarbucksTask{TaskCode: "expert", Current: 0, Target: 1})
	if err != nil {
		t.Fatalf("runStarbucksTask(expert): %v", err)
	}
	if atomic.LoadInt32(&viewed) != 1 || atomic.LoadInt32(&reports) != 1 {
		t.Fatalf("viewed/reports=%d/%d want 1/1", viewed, reports)
	}
	if !strings.Contains(msg, "专家") {
		t.Fatalf("message=%q", msg)
	}
}

func TestRunStarbucksAIChatTask(t *testing.T) {
	oldGap, oldJitter, oldAttempts := mpChatEventGap, mpChatJitter, starbucksPollAttempts
	oldPollGap := starbucksPollGap
	mpChatEventGap, mpChatJitter, starbucksPollGap, starbucksPollAttempts = 0, 0, 0, 1
	t.Cleanup(func() {
		mpChatEventGap, mpChatJitter, starbucksPollGap, starbucksPollAttempts = oldGap, oldJitter, oldPollGap, oldAttempts
	})

	var viewed, reports int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/portal/activity/tasks/ai_chat/viewed":
			atomic.AddInt32(&viewed, 1)
			_, _ = w.Write([]byte(`{"code":0,"msg":"OK","data":{}}`))
		case r.URL.Path == "/v2/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"id\":\"cmb-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\"}\n\n"))
		case r.URL.Path == "/v2/report":
			atomic.AddInt32(&reports, 1)
			_, _ = w.Write([]byte(`{"code":0,"msg":"OK","data":{}}`))
		case r.URL.Path == "/portal/activity/tasks":
			cur := atomic.LoadInt32(&reports)
			progress := map[string]any{"task_code": "ai_chat", "progress": cur}
			if cur >= 3 {
				progress["completed_at"] = "2026-11-12T12:00:00+08:00"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "msg": "OK",
				"data": map[string]any{
					"tasks":    []map[string]any{{"task_code": "ai_chat", "title": "对话", "target_count": 3, "enabled": 1}},
					"progress": []map[string]any{progress},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	p := &Panel{cfg: Config{Upstream: &upstream.Client{
		HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL,
	}}}
	msg, err := p.runStarbucksTask(&auth.Auth{UID: "u1", AccessToken: "at", Nickname: "n1"},
		&upstream.StarbucksTask{TaskCode: "ai_chat", Current: 0, Target: 3})
	if err != nil {
		t.Fatalf("runStarbucksTask(ai_chat): %v", err)
	}
	if atomic.LoadInt32(&viewed) != 1 || atomic.LoadInt32(&reports) != 3 {
		t.Fatalf("viewed/reports=%d/%d want 1/3", viewed, reports)
	}
	if !strings.Contains(msg, "3 次") {
		t.Fatalf("message=%q", msg)
	}
}

func TestAccountTaskAutoExecutesWithoutClaiming(t *testing.T) {
	oldCooldown, oldReportGap := taskActionCooldown, reportGap
	taskActionCooldown, reportGap = 0, 0
	t.Cleanup(func() { taskActionCooldown, reportGap = oldCooldown, oldReportGap })

	var reports, claims int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/activity/growth/tasks":
			cur := int64(0)
			if atomic.LoadInt32(&reports) > 0 {
				cur = 5
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "msg": "OK",
				"data": map[string]any{"tasks": []map[string]any{
					{"task_code": "chat_5", "title": "和 AI 聊天 5 次", "target": 5,
						"current": cur, "accept_status": "accepted", "reward_credit": 100, "reward_energy": 5},
				}},
			})
		case r.URL.Path == "/v2/report":
			atomic.AddInt32(&reports, 1)
			_, _ = w.Write([]byte(`{"code":0,"msg":"OK","data":{}}`))
		case strings.HasSuffix(r.URL.Path, "/claim"):
			atomic.AddInt32(&claims, 1)
			_, _ = w.Write([]byte(`{"code":0,"msg":"OK","data":{"credit":100,"energy":5}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	pl := pool.New("")
	pl.Add(&auth.Auth{UID: "u1", Nickname: "n1", AccessToken: "at"})
	p := &Panel{cfg: Config{
		Pool: pl,
		Upstream: &upstream.Client{
			HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL, WebBaseCN: srv.URL,
		},
	}}
	req := httptest.NewRequest(http.MethodPost, "/panel/api/accounts/u1/tasks/auto",
		strings.NewReader(`{"task_code":"chat_5","source":"growth"}`))
	req.SetPathValue("uid", "u1")
	rec := httptest.NewRecorder()
	p.accountTaskAuto(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := atomic.LoadInt32(&claims); got != 0 {
		t.Fatalf("单任务执行不应自动领奖，claim calls=%d", got)
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["claimable"] != true {
		t.Fatalf("claimable=%v, want true; resp=%s", resp["claimable"], rec.Body.String())
	}
	if _, ok := resp["claimed"]; ok {
		t.Fatalf("响应不应包含 claimed: %s", rec.Body.String())
	}
}

func TestAccountTasksIncludesStarbucksActivity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/activity/growth/tasks":
			_, _ = w.Write([]byte(`{"code":0,"msg":"OK","data":{"tasks":[]}}`))
		case "/portal/activity/config":
			_, _ = w.Write([]byte(`{"code":0,"msg":"OK","data":{"config":{"in_period":true}}}`))
		case "/portal/activity/tasks":
			_, _ = w.Write([]byte(`{"code":0,"msg":"OK","data":{"tasks":[{"task_code":"ai_chat","title":"简单任务，AI 搞定","target_count":3,"reward_credit":100,"enabled":1}],"progress":[]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	pl := pool.New("")
	pl.Add(&auth.Auth{UID: "u1", Nickname: "n1", AccessToken: "at"})
	p := &Panel{cfg: Config{
		Pool: pl,
		Upstream: &upstream.Client{
			HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL, WebBaseCN: srv.URL,
		},
	}}
	req := httptest.NewRequest(http.MethodGet, "/panel/api/accounts/u1/tasks", nil)
	req.SetPathValue("uid", "u1")
	rec := httptest.NewRecorder()
	p.accountTasks(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Starbucks []accountStarbucksTaskView `json:"starbucks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Starbucks) != 1 || resp.Starbucks[0].TaskCode != "ai_chat" || !resp.Starbucks[0].Executable {
		t.Fatalf("starbucks=%+v", resp.Starbucks)
	}
}

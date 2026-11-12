package scheduler

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// sessionTestScheduler 与 enterpriseTestScheduler 同构：switched=true 时账号的
// AccessToken 为带 token_source=enterprise_switch 的 JWT，enterpriseId 为空。
// 该标记只表示登录取证渠道，不代表账号当前是企业版。
func sessionTestScheduler(t *testing.T, switched bool) (*Scheduler, *recorderUpstream) {
	t.Helper()
	r := &recorderUpstream{}
	srv := r.server()
	t.Cleanup(srv.Close)

	token := "at"
	if switched {
		payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"u1","token_source":"enterprise_switch"}`))
		token = "hdr." + payload + ".sig"
	}
	a := &auth.Auth{UID: "u1", AccessToken: token, RefreshToken: "rt", ExpiresAt: 9999999999}
	p := pool.New("")
	p.Add(a)

	up := &upstream.Client{
		HTTP:          srv.Client(),
		ChatBaseCN:    srv.URL,
		BillingBaseCN: srv.URL,
		WebBaseCN:     srv.URL,
	}
	s := New(Config{Pool: p, Upstream: up})
	return s, r
}

// TestEnterpriseSwitchTokenSourceDoesNotGateBehaviorTasks 回归：
// token_source=enterprise_switch 不能单独触发企业门控。
// 2026-11-12 实测该形态的个人号 /v2/report 返回 200 后 streak 从 0 增至 1；
// 若继续按 JWT 标记跳过，活跃上报会被网关自己吞掉。
func TestEnterpriseSwitchTokenSourceDoesNotGateBehaviorTasks(t *testing.T) {
	// 与 TestEnterpriseGateSkipsGrowthTasks 同款：把夜猫子窗口钉在窗口内。
	restoreClock := nowForNightWindow
	nowForNightWindow = func() time.Time { return time.Date(2026, 10, 12, 23, 30, 0, 0, time.Local) }
	t.Cleanup(func() { nowForNightWindow = restoreClock })

	cases := []struct {
		name string
		run  func(*Scheduler)
		// endpoint 是该任务在个人号上必然发起的上游路径。
		endpoint string
	}{
		{"签到", func(s *Scheduler) { s.RunCheckinNow() }, "/daily-checkin"},
		{"活跃上报", func(s *Scheduler) { s.RunActivityNow() }, "/v2/report"},
		{"猫猫旅行", func(s *Scheduler) { s.RunTravelNow() }, "/activity/growth/buddy/info"},
		{"夜猫子", func(s *Scheduler) { s.RunBlackcatNow() }, "/v2/activity/growth/tasks"},
		{"连登管家", func(s *Scheduler) { s.RunStreakBonusNow() }, "/activity/growth/streak"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sSess, rSess := sessionTestScheduler(t, true)
			c.run(sSess)
			if !rSess.has(c.endpoint) {
				t.Errorf("仅 token_source=enterprise_switch 不应跳过 %s，实际请求=%v", c.endpoint, rSess.snapshot())
			}

			sPer, rPer := sessionTestScheduler(t, false)
			c.run(sPer)
			if !rPer.has(c.endpoint) {
				t.Errorf("个人号对照应发起 %s，实际请求=%v", c.endpoint, rPer.snapshot())
			}
		})
	}
}

// TestEnterpriseSwitchTokenSourceKeepsKeepalive 切换会话标记照常保活。
func TestEnterpriseSwitchTokenSourceKeepsKeepalive(t *testing.T) {
	s, r := sessionTestScheduler(t, true)
	s.RunKeepaliveNow()
	if !r.has("/token/refresh") {
		t.Errorf("token_source=enterprise_switch 必须照常保活，实际请求=%v", r.snapshot())
	}
}

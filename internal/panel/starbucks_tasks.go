package panel

import (
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

var (
	starbucksPollAttempts = 3
	starbucksPollGap      = 3 * time.Second
)

// accountStarbucksTaskView 星巴克活动任务在单账号任务列表中的视图。
type accountStarbucksTaskView struct {
	upstream.StarbucksTask
	Source     string `json:"source"`
	Executable bool   `json:"executable"`
	Action     string `json:"action,omitempty"`
}

var starbucksTaskActions = map[string]string{
	"ai_chat": "逐步上报 3 次小程序对话，每次回读进度",
	"expert":  "搜索真实专家并上报小程序专家使用事件",
}

// accountStarbucksTasks 返回活动期内可展示的任务；不支持的动作也展示，方便
// 用户看到活动进度，但 executable=false。
func (p *Panel) accountStarbucksTasks(a *auth.Auth) ([]accountStarbucksTaskView, bool, error) {
	if a.IsGlobal() || a.IsEnterprise() {
		return nil, false, nil
	}
	cfg, err := p.cfg.Upstream.StarbucksConfig()
	if err != nil {
		return nil, false, err
	}
	if !cfg.InPeriod {
		return nil, false, nil
	}
	tasks, err := p.cfg.Upstream.StarbucksTasks(a)
	if err != nil {
		return nil, true, err
	}
	out := make([]accountStarbucksTaskView, 0, len(tasks))
	for _, t := range tasks {
		action, supported := starbucksTaskActions[t.TaskCode]
		if !supported {
			action = "该活动任务暂时只展示，需在小程序活动页手动完成"
		}
		out = append(out, accountStarbucksTaskView{
			StarbucksTask: t,
			Source:        "starbucks",
			Executable:    supported && !t.Claimed && !t.Locked,
			Action:        action,
		})
	}
	return out, true, nil
}

// executeStarbucksAccountTask 执行单个星巴克活动任务并回读，不领奖。
func (p *Panel) executeStarbucksAccountTask(w http.ResponseWriter, a *auth.Auth, code string) {
	before, err := p.starbucksTaskByCode(a, code)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "list starbucks tasks: "+err.Error())
		return
	}
	if before == nil {
		writeErr(w, http.StatusNotFound, "该账号没有此星巴克活动任务")
		return
	}
	if before.Claimed {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "skipped": true, "message": "该活动奖励已领取"})
		return
	}
	if before.Locked {
		writeErr(w, http.StatusUnprocessableEntity, "该活动任务暂未开放")
		return
	}
	if _, ok := starbucksTaskActions[code]; !ok {
		writeErr(w, http.StatusNotImplemented, "该星巴克活动任务暂不支持自动执行")
		return
	}

	msg, err := p.runStarbucksTask(a, before)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "执行失败: "+err.Error())
		return
	}
	after, _ := p.starbucksTaskByCode(a, code)
	resp := map[string]any{
		"ok":              true,
		"message":         msg,
		"progress_before": starbucksProgressText(before),
		"progress_after":  starbucksProgressText(after),
		"claimable":       after != nil && after.Completed && !after.Claimed,
		"source":          "starbucks",
	}
	if after != nil && after.Completed {
		resp["message"] = msg + "；任务已达标，请手动点击「领奖」"
	}
	log.Printf("panel: 星巴克任务执行 uid=%s code=%s progress %s -> %s claimable=%v",
		a.UID, code, starbucksProgressText(before), starbucksProgressText(after), resp["claimable"])
	writeJSON(w, http.StatusOK, resp)
}

func (p *Panel) runStarbucksTask(a *auth.Auth, task *upstream.StarbucksTask) (string, error) {
	if err := p.cfg.Upstream.StarbucksTaskViewed(a, task.TaskCode); err != nil {
		return "", fmt.Errorf("标记任务已开始: %w", err)
	}
	switch task.TaskCode {
	case "expert":
		experts, err := p.cfg.Upstream.MarketExpertList(a, "agent")
		if err != nil {
			return "", fmt.Errorf("拉取专家市场: %w", err)
		}
		if len(experts) == 0 {
			return "", fmt.Errorf("专家市场列表为空")
		}
		e := experts[0]
		ev := upstream.MiniExpertUseEvent(e.ExpertID, e.DisplayNameZH, e.ExpertType)
		if err := p.cfg.Upstream.ReportMPEvent(a, ev); err != nil {
			return "", fmt.Errorf("上报小程序专家使用事件: %w", err)
		}
		after, err := p.waitStarbucksProgress(a, task.TaskCode, task.Current)
		if err != nil {
			return "", err
		}
		if after.Current <= task.Current {
			return "已上报专家使用事件，但活动进度尚未变化", nil
		}
		return fmt.Sprintf("已使用专家「%s」并推进活动进度", e.DisplayNameZH), nil

	case "ai_chat":
		target := task.Target
		if target <= 0 {
			target = 3
		}
		current := task.Current
		for current < target {
			if mpChatJitter > 0 {
				time.Sleep(mpChatEventGap + time.Duration(rand.Int64N(int64(mpChatJitter))))
			} else {
				time.Sleep(mpChatEventGap)
			}
			conv, _, err := p.cfg.Upstream.DesktopChatWithExpert(a, "")
			if err != nil {
				return fmt.Sprintf("已推进 %d/%d，第 %d 次真实对话失败: %v", current, target, current+1, err), nil
			}
			if err := p.cfg.Upstream.ReportMPEvent(a, upstream.StarbucksChatEvent(conv)); err != nil {
				return fmt.Sprintf("已推进 %d/%d，下一次上报失败: %v", current, target, err), nil
			}
			after, err := p.waitStarbucksProgress(a, task.TaskCode, current)
			if err != nil {
				return "", err
			}
			if after.Current <= current {
				return fmt.Sprintf("已上报第 %d 次对话，但活动进度仍为 %d/%d；已停止，等待后续人工重试", current+1, after.Current, target), nil
			}
			current = after.Current
		}
		return fmt.Sprintf("已逐步完成 %d 次小程序对话", target), nil
	}
	return "", fmt.Errorf("不支持的活动任务 %s", task.TaskCode)
}

func (p *Panel) waitStarbucksProgress(a *auth.Auth, code string, previous int64) (*upstream.StarbucksTask, error) {
	var last *upstream.StarbucksTask
	for i := 0; i < starbucksPollAttempts; i++ {
		time.Sleep(starbucksPollGap)
		t, err := p.starbucksTaskByCode(a, code)
		if err != nil {
			return nil, err
		}
		if t == nil {
			return nil, fmt.Errorf("活动任务消失")
		}
		last = t
		if t.Current > previous || t.Claimed || t.Completed {
			return t, nil
		}
	}
	return last, nil
}

func (p *Panel) starbucksTaskByCode(a *auth.Auth, code string) (*upstream.StarbucksTask, error) {
	tasks, err := p.cfg.Upstream.StarbucksTasks(a)
	if err != nil {
		return nil, err
	}
	for i := range tasks {
		if tasks[i].TaskCode == code {
			return &tasks[i], nil
		}
	}
	return nil, nil
}

func (p *Panel) claimStarbucksTask(a *auth.Auth, code string) (credit, chance int64, claimed bool, msg string, err error) {
	before, err := p.starbucksTaskByCode(a, code)
	if err != nil {
		return 0, 0, false, "", err
	}
	if before == nil {
		return 0, 0, false, "", fmt.Errorf("该账号没有此星巴克活动任务")
	}
	if before.Claimed {
		return 0, 0, false, "该活动奖励此前已领取", nil
	}
	if !before.Completed {
		return 0, 0, false, "", fmt.Errorf("活动任务尚未完成")
	}
	if err := p.cfg.Upstream.StarbucksTaskClaim(a, code); err != nil {
		return 0, 0, false, "", err
	}
	after, _ := p.starbucksTaskByCode(a, code)
	if after != nil && after.Claimed {
		return before.RewardCredit, before.RewardChance, true,
			fmt.Sprintf("已领取星巴克活动奖励（+%d 积分）", before.RewardCredit), nil
	}
	return before.RewardCredit, before.RewardChance, false,
		"领奖请求已提交，活动页尚未回读为已领取", nil
}

func starbucksProgressText(t *upstream.StarbucksTask) string {
	if t == nil {
		return "?"
	}
	return fmt.Sprintf("%d/%d", t.Current, t.Target)
}

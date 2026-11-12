// starbucks.go 星巴克协作活动（campaign_starbucks_2026）。
//
// 该活动独立于 growth 成长任务：任务列表与进度走 codebuddy.cn 的
// /portal/activity 接口，不要求 X-Client-Platform: miniprogram。
package upstream

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

const starbucksActivityCode = "campaign_starbucks_2026"

// StarbucksActivityConfig 星巴克活动窗口。config 为公开只读接口。
type StarbucksActivityConfig struct {
	ActivityCode string `json:"activity_code"`
	Title        string `json:"title"`
	StartAt      string `json:"start_at"`
	EndAt        string `json:"end_at"`
	InPeriod     bool   `json:"in_period"`
	IsGrayUser   bool   `json:"is_gray_user"`
	ServerTime   string `json:"server_time"`
}

// StarbucksTask 活动任务及当前账号进度。
type StarbucksTask struct {
	TaskCode      string `json:"task_code"`
	Title         string `json:"title"`
	Description   string `json:"description,omitempty"`
	Target        int64  `json:"target"`
	Current       int64  `json:"current"`
	RewardCredit  int64  `json:"reward_credit,omitempty"`
	RewardChance  int64  `json:"reward_chance,omitempty"`
	NeedView      bool   `json:"need_view,omitempty"`
	Viewed        bool   `json:"viewed,omitempty"`
	Completed     bool   `json:"completed,omitempty"`
	Claimed       bool   `json:"claimed,omitempty"`
	Locked        bool   `json:"locked,omitempty"`
	ClaimMode     string `json:"claim_mode,omitempty"`
	JumpURL       string `json:"jump_url,omitempty"`
	Enabled       int    `json:"enabled,omitempty"`
	Prerequisites any    `json:"prerequisites,omitempty"`
}

// StarbucksConfig 拉取活动窗口与开放状态。
func (c *Client) StarbucksConfig() (*StarbucksActivityConfig, error) {
	path := "/portal/activity/config?activity_code=" + url.QueryEscape(starbucksActivityCode)
	req, err := http.NewRequest(http.MethodGet, c.BillingBaseCN+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "WorkBuddy-MP/2.4.0")
	data, err := c.doJSON(req)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Config StarbucksActivityConfig `json:"config"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &resp.Config, nil
}

// StarbucksTasks 拉取活动任务列表；activityCode 固定为当前活动，
// 后续新活动可另加参数版本。
func (c *Client) StarbucksTasks(a *auth.Auth) ([]StarbucksTask, error) {
	path := "/portal/activity/tasks?activity_code=" + url.QueryEscape(starbucksActivityCode)
	req, err := http.NewRequest(http.MethodGet, c.BillingBaseCN+path, nil)
	if err != nil {
		return nil, err
	}
	c.starbucksHeaders(req, a)
	data, err := c.doJSON(req)
	if err != nil {
		return nil, err
	}
	return parseStarbucksTasks(data)
}

// StarbucksTaskViewed 标记任务已开始。任务页在跳转前调用；need_view 任务
// 未调用该接口时后续行为可能不计入活动进度。
func (c *Client) StarbucksTaskViewed(a *auth.Auth, taskCode string) error {
	path := "/portal/activity/tasks/" + url.PathEscape(taskCode) +
		"/viewed?activity_code=" + url.QueryEscape(starbucksActivityCode)
	req, err := http.NewRequest(http.MethodPost, c.BillingBaseCN+path, bytes.NewReader(nil))
	if err != nil {
		return err
	}
	c.starbucksHeaders(req, a)
	_, err = c.doJSON(req)
	return err
}

// StarbucksTaskClaim 领取已完成的活动任务奖励。
func (c *Client) StarbucksTaskClaim(a *auth.Auth, taskCode string) error {
	path := "/portal/activity/tasks/" + url.PathEscape(taskCode) +
		"/claim?activity_code=" + url.QueryEscape(starbucksActivityCode)
	req, err := http.NewRequest(http.MethodPost, c.BillingBaseCN+path, bytes.NewReader(nil))
	if err != nil {
		return err
	}
	c.starbucksHeaders(req, a)
	_, err = c.doJSON(req)
	return err
}

func (c *Client) starbucksHeaders(req *http.Request, a *auth.Auth) {
	req.Header.Set("Authorization", "Bearer "+a.AccessTokenValue())
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-Id", a.UID)
	req.Header.Set("X-Client-Platform", "miniprogram")
	req.Header.Set("X-Client-Product", "workbuddy-mp")
	req.Header.Set("User-Agent", "WorkBuddy-MP/2.4.0")
}

// StarbucksChatEvent 构造星巴克 ai_chat 使用的小程序本地对话事件。
// 形状对齐 mpsrc 29462 模块：发送成功后由小程序以 wx_app_local +
// source=mini_program 上报，而不是云端的 wx_app_cloud 指纹。
func StarbucksChatEvent(conversationID string) map[string]any {
	rid := "wb2api-" + clientToken()
	if conversationID == "" {
		conversationID = rid
	}
	return map[string]any{
		"eventCode":      "chat_request_send",
		"timestamp":      time.Now().UnixMilli(),
		"ideName":        "wx_app_local",
		"source":         "mini_program",
		"conversationId": conversationID,
		"requestId":      rid,
		"inputLength":    14,
		"requestModelId": "", "requestModelName": "",
		"mentionContexts": []any{}, "mentionContextCount": 0,
		"command": "", "traceId": rid,
	}
}

func parseStarbucksTasks(data json.RawMessage) ([]StarbucksTask, error) {
	var resp struct {
		Tasks []struct {
			TaskCode      string `json:"task_code"`
			Title         string `json:"title"`
			Description   string `json:"description"`
			Target        int64  `json:"target_count"`
			RewardCredit  int64  `json:"reward_credit"`
			RewardChance  int64  `json:"reward_chance"`
			NeedView      bool   `json:"need_view"`
			ClaimMode     string `json:"claim_mode"`
			JumpURL       string `json:"jump_url_mp"`
			Enabled       int    `json:"enabled"`
			Prerequisites any    `json:"prerequisites"`
		} `json:"tasks"`
		Progress []struct {
			TaskCode    string          `json:"task_code"`
			Progress    int64           `json:"progress"`
			ViewedAt    json.RawMessage `json:"viewed_at"`
			CompletedAt json.RawMessage `json:"completed_at"`
			ClaimedAt   json.RawMessage `json:"claimed_at"`
		} `json:"progress"`
		Invite struct {
			Count int64 `json:"count"`
			Need  int64 `json:"need"`
			Open  bool  `json:"open"`
		} `json:"invite"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}

	prog := make(map[string]struct {
		current   int64
		viewed    bool
		completed bool
		claimed   bool
	}, len(resp.Progress))
	for _, p := range resp.Progress {
		prog[p.TaskCode] = struct {
			current   int64
			viewed    bool
			completed bool
			claimed   bool
		}{
			current:   p.Progress,
			viewed:    rawTimeSet(p.ViewedAt),
			completed: rawTimeSet(p.CompletedAt),
			claimed:   rawTimeSet(p.ClaimedAt),
		}
	}

	out := make([]StarbucksTask, 0, len(resp.Tasks))
	for _, t := range resp.Tasks {
		if t.Enabled == 0 {
			continue
		}
		p := prog[t.TaskCode]
		task := StarbucksTask{
			TaskCode:      t.TaskCode,
			Title:         t.Title,
			Description:   t.Description,
			Target:        t.Target,
			Current:       p.current,
			RewardCredit:  t.RewardCredit,
			RewardChance:  t.RewardChance,
			NeedView:      t.NeedView,
			Viewed:        p.viewed,
			Completed:     p.completed,
			Claimed:       p.claimed,
			ClaimMode:     t.ClaimMode,
			JumpURL:       t.JumpURL,
			Enabled:       t.Enabled,
			Prerequisites: t.Prerequisites,
		}
		if t.TaskCode == "invite" {
			task.Current = resp.Invite.Count
			if resp.Invite.Need > 0 {
				task.Target = resp.Invite.Need
			}
			task.Locked = !resp.Invite.Open
		}
		if task.Completed || (task.Target > 0 && task.Current >= task.Target) {
			task.Completed = true
		}
		out = append(out, task)
	}
	return out, nil
}

func rawTimeSet(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s != "" && s != "null" && s != `""`
}

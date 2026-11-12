package upstream

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// CustomSkill 是用户已导入的自定义技能。
type CustomSkill struct {
	SkillUID    string   `json:"skillUid"`
	Name        string   `json:"name"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	IconURL     string   `json:"iconUrl"`
	Version     string   `json:"version"`
	Status      string   `json:"status"`
	UpdatedAt   string   `json:"updatedAt"`
}

// CustomSkillShare 创建成功后的分享渠道凭据。
type CustomSkillShare struct {
	ShareCode string   `json:"share_code"`
	Sig       string   `json:"sig"`
	Ways      []string `json:"ways"`
	ExpiresAt string   `json:"expires_at"`
}

type skillBundle struct {
	DownloadURL string `json:"downloadUrl"`
	Size        int64  `json:"size"`
}

type shareUpload struct {
	UploadID      string            `json:"upload_id"`
	UploadURL     string            `json:"upload_url"`
	UploadHeaders map[string]string `json:"upload_headers"`
}

var skillSharePollGap = 3 * time.Second
var skillSharePollAttempts = 100

// CustomSkills 查询当前账号已导入的自定义技能（最多 100 个）。
func (c *Client) CustomSkills(a *auth.Auth) ([]CustomSkill, error) {
	req, err := http.NewRequest(http.MethodGet,
		c.BillingBaseCN+"/v2/user-asset/custom-skills?page=1&page_size=100", nil)
	if err != nil {
		return nil, err
	}
	c.skillShareHeaders(req, a)
	data, err := c.doJSON(req)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Items []CustomSkill `json:"items"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

// CreateCustomSkillShare 为已导入技能生成 link 分享渠道。流程与小程序一致：
// 获取技能包 → 申请上传 → PUT 上传 → 安全检测 → 创建 c2c 分享渠道。
func (c *Client) CreateCustomSkillShare(a *auth.Auth, skill CustomSkill) (*CustomSkillShare, error) {
	uid := strings.TrimSpace(skill.SkillUID)
	if uid == "" {
		return nil, fmt.Errorf("技能 uid 为空")
	}
	contentReq, err := http.NewRequest(http.MethodGet,
		c.BillingBaseCN+"/v2/user-asset/custom-skills/"+uid+"/content", nil)
	if err != nil {
		return nil, err
	}
	c.skillShareHeaders(contentReq, a)
	data, err := c.doJSON(contentReq)
	if err != nil {
		return nil, fmt.Errorf("获取技能包: %w", err)
	}
	var bundle skillBundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(bundle.DownloadURL, "https://") || bundle.Size <= 0 || bundle.Size > 3<<20 {
		return nil, fmt.Errorf("技能包不可用或超过 3MB")
	}
	download, err := c.HTTP.Get(bundle.DownloadURL)
	if err != nil {
		return nil, fmt.Errorf("下载技能包: %w", err)
	}
	raw, err := io.ReadAll(io.LimitReader(download.Body, 3<<20+1))
	download.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("读取技能包: %w", err)
	}
	if int64(len(raw)) != bundle.Size {
		return nil, fmt.Errorf("技能包校验失败: size=%d want=%d", len(raw), bundle.Size)
	}

	upload, err := c.createShareUpload(a, int64(len(raw)))
	if err != nil {
		return nil, err
	}
	if err := c.putShareUpload(upload, raw); err != nil {
		return nil, err
	}
	if err := c.reviewShareUpload(a, upload.UploadID); err != nil {
		return nil, err
	}
	if err := c.waitShareUploadApproved(a, upload.UploadID); err != nil {
		return nil, err
	}
	return c.createShareChannel(a, upload.UploadID, skill)
}

func (c *Client) createShareUpload(a *auth.Auth, size int64) (*shareUpload, error) {
	body, _ := json.Marshal(map[string]any{"asset_kind": "skill", "file_size": size})
	req, err := http.NewRequest(http.MethodPost, c.openPlatformBase()+"/api/v2/open-platform/asset-shares/uploads",
		bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.skillShareHeaders(req, a)
	data, err := c.doJSON(req)
	if err != nil {
		return nil, fmt.Errorf("申请分享上传: %w", err)
	}
	var up shareUpload
	if err := json.Unmarshal(data, &up); err != nil {
		return nil, err
	}
	if up.UploadID == "" || !strings.HasPrefix(up.UploadURL, "https://") || len(up.UploadHeaders) == 0 {
		return nil, fmt.Errorf("分享上传票据无效")
	}
	return &up, nil
}

func (c *Client) putShareUpload(up *shareUpload, raw []byte) error {
	req, err := http.NewRequest(http.MethodPut, up.UploadURL, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	for k, v := range up.UploadHeaders {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("上传技能包: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("上传技能包 HTTP %d: %s", resp.StatusCode, b)
	}
	return nil
}

func (c *Client) reviewShareUpload(a *auth.Auth, uploadID string) error {
	req, err := http.NewRequest(http.MethodPost,
		c.openPlatformBase()+"/api/v2/open-platform/asset-shares/uploads/"+uploadID+"/review", nil)
	if err != nil {
		return err
	}
	c.skillShareHeaders(req, a)
	if _, err := c.doJSON(req); err != nil {
		return fmt.Errorf("提交技能安全检测: %w", err)
	}
	return nil
}

func (c *Client) waitShareUploadApproved(a *auth.Auth, uploadID string) error {
	for i := 0; i < skillSharePollAttempts; i++ {
		req, err := http.NewRequest(http.MethodGet,
			c.openPlatformBase()+"/api/v2/open-platform/asset-shares/uploads/"+uploadID, nil)
		if err != nil {
			return err
		}
		c.skillShareHeaders(req, a)
		data, err := c.doJSON(req)
		if err != nil {
			return fmt.Errorf("查询技能安全检测: %w", err)
		}
		var status struct {
			Status        string `json:"status"`
			ReviewMessage string `json:"review_message"`
		}
		if err := json.Unmarshal(data, &status); err != nil {
			return err
		}
		switch status.Status {
		case "approved":
			return nil
		case "rejected":
			return fmt.Errorf("技能安全检测未通过: %s", status.ReviewMessage)
		case "failed":
			return fmt.Errorf("技能安全检测失败: %s", status.ReviewMessage)
		case "uploading", "reviewing":
		default:
			return fmt.Errorf("技能安全检测状态异常: %s", status.Status)
		}
		if i+1 < skillSharePollAttempts {
			time.Sleep(skillSharePollGap)
		}
	}
	return fmt.Errorf("技能安全检测超时")
}

func (c *Client) createShareChannel(a *auth.Auth, uploadID string, skill CustomSkill) (*CustomSkillShare, error) {
	name := strings.TrimSpace(skill.Name)
	if name == "" {
		name = strings.TrimSpace(skill.Title)
	}
	if name == "" {
		name = skill.SkillUID
	}
	extra := map[string]any{
		"name":        name,
		"description": skill.Description,
		"tags":        skill.Tags,
		"shareLocale": "zh",
	}
	if skill.IconURL != "" {
		extra["iconUrl"] = skill.IconURL
	}
	if skill.Version != "" {
		extra["version"] = skill.Version
	}
	ref, _ := json.Marshal(map[string]string{"upload_id": uploadID})
	body, _ := json.Marshal(map[string]any{
		"asset_kind":   "skill",
		"asset_source": "custom",
		"asset_ref":    string(ref),
		"ways":         []string{"link"},
		"extra":        extra,
	})
	req, err := http.NewRequest(http.MethodPost,
		c.openPlatformBase()+"/api/v2/open-platform/asset-shares/c2c/channels", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.skillShareHeaders(req, a)
	data, err := c.doJSON(req)
	if err != nil {
		return nil, fmt.Errorf("创建技能分享: %w", err)
	}
	var share CustomSkillShare
	if err := json.Unmarshal(data, &share); err != nil {
		return nil, err
	}
	if share.ShareCode == "" || share.Sig == "" || len(share.Ways) == 0 {
		return nil, fmt.Errorf("创建技能分享失败: 响应缺少 share_code/sig")
	}
	return &share, nil
}

func (c *Client) openPlatformBase() string {
	if c.OpenPlatformBaseCN != "" {
		return strings.TrimRight(c.OpenPlatformBaseCN, "/")
	}
	return "https://open.workbuddy.cn"
}

func (c *Client) skillShareHeaders(req *http.Request, a *auth.Auth) {
	req.Header.Set("Authorization", "Bearer "+a.AccessTokenValue())
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-Id", a.UID)
	req.Header.Set("X-Submitted-From", "mp")
	req.Header.Set("User-Agent", c.userAgent(a))
	if a.EnterpriseID != "" {
		req.Header.Set("X-Enterprise-Id", a.EnterpriseID)
		req.Header.Set("X-Tenant-Id", a.EnterpriseID)
	}
}

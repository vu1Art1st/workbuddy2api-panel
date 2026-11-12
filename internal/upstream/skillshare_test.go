package upstream

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

func TestCreateCustomSkillShareFlow(t *testing.T) {
	oldGap, oldAttempts := skillSharePollGap, skillSharePollAttempts
	skillSharePollGap, skillSharePollAttempts = 0, 1
	t.Cleanup(func() { skillSharePollGap, skillSharePollAttempts = oldGap, oldAttempts })

	bundle := []byte("test-skill-bundle")
	var putOK, channelOK int32
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/user-asset/custom-skills/sk1/content":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "msg": "ok",
				"data": map[string]any{"downloadUrl": srv.URL + "/bundle", "size": len(bundle)},
			})
		case r.URL.Path == "/bundle":
			_, _ = w.Write(bundle)
		case r.URL.Path == "/api/v2/open-platform/asset-shares/uploads":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "msg": "ok",
				"data": map[string]any{
					"upload_id": "up1", "upload_url": srv.URL + "/put",
					"upload_headers": map[string]string{"x-test": "yes"},
				},
			})
		case r.URL.Path == "/put":
			if r.Method != http.MethodPut || r.Header.Get("x-test") != "yes" {
				t.Errorf("bad put request method/header: %s/%q", r.Method, r.Header.Get("x-test"))
			}
			got, _ := io.ReadAll(r.Body)
			if string(got) != string(bundle) {
				t.Errorf("upload body=%q", got)
			}
			atomic.StoreInt32(&putOK, 1)
		case r.URL.Path == "/api/v2/open-platform/asset-shares/uploads/up1/review":
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{}}`))
		case r.URL.Path == "/api/v2/open-platform/asset-shares/uploads/up1":
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"status":"approved"}}`))
		case r.URL.Path == "/api/v2/open-platform/asset-shares/c2c/channels":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["asset_kind"] != "skill" || body["asset_source"] != "custom" {
				t.Errorf("channel body=%v", body)
			}
			extra, _ := body["extra"].(map[string]any)
			if extra["name"] != "测试技能" {
				t.Errorf("extra=%v", extra)
			}
			atomic.StoreInt32(&channelOK, 1)
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"share_code":"sh1","sig":"sig1","ways":["link"]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), BillingBaseCN: srv.URL, OpenPlatformBaseCN: srv.URL}
	share, err := c.CreateCustomSkillShare(&auth.Auth{UID: "u1", AccessToken: "at"}, CustomSkill{
		SkillUID: "sk1", Name: "测试技能", Description: "desc", Tags: []string{"tag"},
	})
	if err != nil {
		t.Fatalf("CreateCustomSkillShare: %v", err)
	}
	if share.ShareCode != "sh1" || share.Sig != "sig1" || len(share.Ways) != 1 {
		t.Fatalf("share=%+v", share)
	}
	if atomic.LoadInt32(&putOK) != 1 || atomic.LoadInt32(&channelOK) != 1 {
		t.Fatalf("put/channel=%d/%d", putOK, channelOK)
	}
}

func TestCustomSkillsList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v2/user-asset/custom-skills") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"total":1,"items":[{"skillUid":"sk1","name":"测试技能","status":"ready"}]}}`))
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), BillingBaseCN: srv.URL}
	items, err := c.CustomSkills(&auth.Auth{UID: "u1", AccessToken: "at"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].SkillUID != "sk1" || items[0].Name != "测试技能" {
		t.Fatalf("items=%+v", items)
	}
}

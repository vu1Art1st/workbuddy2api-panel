package upstream

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

func TestCreditExpirySnapshot(t *testing.T) {
	now := time.Now().In(softRateResetLoc)
	soon := now.Add(24 * time.Hour).Truncate(time.Second)
	later := now.Add(10 * 24 * time.Hour).Truncate(time.Second)
	payload := `{"code":0,"data":{"Response":{"Data":{"Accounts":[` +
		`{"PackageName":"soon-a","CycleCapacitySize":10,"CycleCapacityRemain":10,"CycleCapacityUsed":0,"CycleEndTime":"` + soon.Format(packageEndLayout) + `"},` +
		`{"PackageName":"soon-b","CycleCapacitySize":15,"CycleCapacityRemain":15,"CycleCapacityUsed":0,"CycleEndTime":"` + soon.Format(packageEndLayout) + `"},` +
		`{"PackageName":"later","CycleCapacitySize":20,"CycleCapacityRemain":20,"CycleCapacityUsed":0,"CycleEndTime":"` + later.Format(packageEndLayout) + `"},` +
		`{"PackageName":"unknown","CycleCapacitySize":5,"CycleCapacityRemain":5,"CycleCapacityUsed":0}` +
		`]}}}}`
	c := testClient(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/v2/billing/meter/get-user-resource") {
			return nil, errors.New("wrong path: " + r.URL.Path)
		}
		return jsonResp(200, payload), nil
	})

	remain, total, expiring, earliestAt, earliestRemaining, err := c.UserResourceDetailedWithExpiry(
		&auth.Auth{AccessToken: "at", UID: "u1"}, 48*time.Hour,
	)
	if err != nil {
		t.Fatalf("resource: %v", err)
	}
	if remain != 50 || total != 50 {
		t.Errorf("remain/total=%d/%d want 50/50", remain, total)
	}
	if expiring != 25 {
		t.Errorf("expiring=%d want 25", expiring)
	}
	if earliestRemaining != 25 {
		t.Errorf("earliestRemaining=%d want 25", earliestRemaining)
	}
	if !earliestAt.Equal(soon) {
		t.Errorf("earliestAt=%v want %v", earliestAt, soon)
	}

	packs, sumRemain, sumSize, err := c.CreditPackages(&auth.Auth{AccessToken: "at", UID: "u1"})
	if err != nil {
		t.Fatalf("packages: %v", err)
	}
	if sumRemain != 50 || sumSize != 50 {
		t.Errorf("package sums=%d/%d want 50/50", sumRemain, sumSize)
	}
	var withExpiry, withoutExpiry int
	for _, p := range packs {
		if p.ExpiresAt > 0 {
			withExpiry++
		} else {
			withoutExpiry++
		}
	}
	if withExpiry != 3 || withoutExpiry != 1 {
		t.Errorf("expiry timestamp packages=%d/%d want 3/1", withExpiry, withoutExpiry)
	}
}

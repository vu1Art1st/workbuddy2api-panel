package pool

import (
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

func TestPickPrefersEarliestExpiring(t *testing.T) {
	withNoPickGap(t)
	p := New("")
	p.Add(&auth.Auth{UID: "later"})
	p.Add(&auth.Auth{UID: "soon"})
	p.Add(&auth.Auth{UID: "none"})

	now := time.Now()
	p.SetCreditsDetailed("later", 100, 100, 100, now.Add(48*time.Hour), 100)
	p.SetCreditsDetailed("soon", 10, 10, 10, now.Add(2*time.Hour), 10)
	p.SetCreditsDetailed("none", 1000, 1000, 0, time.Time{}, 0)

	for i := 0; i < 5; i++ {
		got := p.Pick()
		if got == nil || got.UID != "soon" {
			t.Fatalf("pick %d=%v want soon", i, got)
		}
	}
}

func TestPickEarliestExpiryTieBreaksByRemaining(t *testing.T) {
	withNoPickGap(t)
	p := New("")
	p.Add(&auth.Auth{UID: "small"})
	p.Add(&auth.Auth{UID: "large"})

	at := time.Now().Add(time.Hour)
	p.SetCreditsDetailed("small", 10, 10, 10, at, 10)
	p.SetCreditsDetailed("large", 50, 50, 50, at, 50)

	got := p.Pick()
	if got == nil || got.UID != "large" {
		t.Fatalf("pick=%v want large", got)
	}
}

func TestPreferExpiringDisabledRestoresWeight(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "a"})
	p.Add(&auth.Auth{UID: "b"})
	now := time.Now()
	p.SetCreditsDetailed("a", 100, 100, 50, now.Add(time.Hour), 50)
	p.SetCreditsDetailed("b", 100, 100, 0, time.Time{}, 0)
	p.SetPreferExpiring(false)

	p.mu.Lock()
	wa := p.weightOf(p.byUID["a"], 100, now)
	wb := p.weightOf(p.byUID["b"], 100, now)
	p.mu.Unlock()
	if wa != wb {
		t.Fatalf("disabled expiring weights differ: %v/%v", wa, wb)
	}
}

func TestCreditExpirySnapshotConsumptionAndClear(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	now := time.Now()
	p.SetCreditsDetailed("u1", 100, 100, 50, now.Add(time.Hour), 40)

	st, _ := p.Status("u1")
	if st.CreditsExpiring != 50 || st.CreditsEarliestRemaining != 40 || st.CreditsEarliestExpiry.IsZero() {
		t.Fatalf("initial snapshot=%+v", st)
	}

	p.NoteModelCost("u1", "m", 10, 1000)
	st, _ = p.Status("u1")
	if st.Credits != 90 || st.CreditsExpiring != 40 || st.CreditsEarliestRemaining != 30 {
		t.Fatalf("after consume=%+v", st)
	}

	p.NoteModelCost("u1", "m", 40, 1000)
	st, _ = p.Status("u1")
	if st.Credits != 50 || st.CreditsExpiring != 0 || st.CreditsEarliestRemaining != 0 || !st.CreditsEarliestExpiry.IsZero() {
		t.Fatalf("after exhaustion=%+v", st)
	}

	p.SetCreditsDetailed("u1", 50, 50, 10, now.Add(time.Hour), 10)
	p.SetCreditsDetailed("u1", 50, 50, 0, time.Time{}, 0)
	st, _ = p.Status("u1")
	if st.CreditsExpiring != 0 || st.CreditsEarliestRemaining != 0 || !st.CreditsEarliestExpiry.IsZero() {
		t.Fatalf("zero refresh did not clear snapshot=%+v", st)
	}
}

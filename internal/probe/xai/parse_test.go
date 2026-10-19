package xai

import (
	"testing"
	"time"

	"github.com/karlorz/cpa-expiring-first/internal/probe/reading"
)

func TestParseWeeklyLongWindowReset(t *testing.T) {
	body := []byte(`{"config":{"currentPeriod":{"type":"weekly","end":"2026-10-14T12:00:00Z"}}}`)
	reset, errParse := ParseWeeklyLongWindowReset(body)
	if errParse != nil {
		t.Fatal(errParse)
	}
	want := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	if reset == nil || !reset.Equal(want) {
		t.Fatalf("reset = %v, want %v", reset, want)
	}
}

func TestParseWeeklyLongWindowResetIgnoresMonthly(t *testing.T) {
	cases := map[string]string{
		"monthly period": `{"config":{"currentPeriod":{"type":"monthly","end":"2026-11-01T00:00:00Z"}}}`,
		"billing end":    `{"config":{"billingPeriodEnd":"2026-11-01T00:00:00Z"}}`,
		"empty body":     ``,
		"malformed json": `{`,
		"missing period": `{"config":{}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			reset, errParse := ParseWeeklyLongWindowReset([]byte(body))
			if name == "malformed json" {
				if errParse == nil {
					t.Fatalf("errParse = nil, want a decode error")
				}
				return
			}
			if errParse != nil {
				t.Fatalf("errParse = %v, want nil", errParse)
			}
			if reset != nil {
				t.Fatalf("reset = %v, want nil", reset)
			}
		})
	}
}

func TestClassifyPlan(t *testing.T) {
	cases := map[string]struct {
		settings string
		billing  string
		tier     string
		want     PlanClass
	}{
		"jwt paid tier": {
			tier: "1",
			want: PlanClassPaid,
		},
		"settings supergrok": {
			settings: `{"product":"supergrok"}`,
			want:     PlanClassPaid,
		},
		"billing free": {
			billing: `{"plan":"grokfree"}`,
			want:    PlanClassFree,
		},
		"unfetchable": {
			want: PlanClassFree,
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			class, _ := ClassifyPlan([]byte(testCase.settings), []byte(testCase.billing), testCase.tier)
			if class != testCase.want {
				t.Fatalf("ClassifyPlan() = %q, want %q", class, testCase.want)
			}
		})
	}
}

func TestWindowsAppliesToEveryModel(t *testing.T) {
	result := ProbeResult{
		Status: reading.StatusReady,
		Windows: []reading.QuotaWindow{{
			Duration:  7 * 24 * time.Hour,
			Remaining: 100,
			ResetAt:   time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC),
		}},
	}
	windows := Windows(result)
	if len(windows) != 1 {
		t.Fatalf("windows = %+v, want one", windows)
	}
	if len(windows[0].Models) != 0 {
		t.Fatalf("models = %v, want empty so the window applies to every model", windows[0].Models)
	}
	if windows[0].UsedPercent != 0 {
		t.Fatalf("used = %v, want 0", windows[0].UsedPercent)
	}
}

func TestIsUnauthorizedProbe(t *testing.T) {
	if !IsUnauthorizedProbe(401, "") {
		t.Error("status 401 must count as unauthorized")
	}
	if IsUnauthorizedProbe(404, "not found") {
		t.Error("status 404 must not count as unauthorized")
	}
	if !IsUnauthorizedProbe(0, "invalid or expired credentials") {
		t.Error("credential wording must count as unauthorized")
	}
}

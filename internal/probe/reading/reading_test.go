package reading

import (
	"testing"
	"time"
)

func TestWindowsFromFiltersAndScopes(t *testing.T) {
	quotas := []QuotaWindow{
		{Duration: 5 * time.Hour, Remaining: 40},
		{Duration: 0, Remaining: 50},
		{Duration: -time.Hour, Remaining: 50},
	}
	if got := WindowsFrom(StatusProbeFailed, quotas, nil); got != nil {
		t.Fatalf("failed reading = %+v, want nil", got)
	}
	if got := WindowsFrom(StatusReady, nil, nil); got != nil {
		t.Fatalf("empty reading = %+v, want nil", got)
	}
	got := WindowsFrom(StatusReady, quotas, []string{"gemini"})
	if len(got) != 1 || got[0].Minutes != 300 || got[0].UsedPercent != 60 || len(got[0].Models) != 1 || got[0].Models[0] != "gemini" {
		t.Fatalf("windows = %+v, want one scoped five-hour window", got)
	}
}

func TestWindowFromClampsUsedPercent(t *testing.T) {
	cases := []struct {
		remaining int64
		want      float64
	}{
		{remaining: 100, want: 0},
		{remaining: 0, want: 100},
		{remaining: 40, want: 60},
		{remaining: 120, want: 0},
		{remaining: -5, want: 100},
	}
	for _, testCase := range cases {
		got := WindowFrom(QuotaWindow{Duration: time.Hour, Remaining: testCase.remaining}, nil)
		if got.UsedPercent != testCase.want {
			t.Errorf("remaining %d: used = %v, want %v", testCase.remaining, got.UsedPercent, testCase.want)
		}
	}
}

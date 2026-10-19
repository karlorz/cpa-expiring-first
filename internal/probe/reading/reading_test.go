package reading

import (
	"testing"
	"time"
)

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

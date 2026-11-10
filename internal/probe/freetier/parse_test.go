package freetier

import (
	"testing"
	"time"
)

func TestParseBalanceMatchesUpstreamShape(t *testing.T) {
	raw := []byte(`{"code":0,"data":{"Response":{"Data":{"Accounts":[{"PackageName":"基础套餐","CycleCapacitySize":1000,"CycleCapacityRemain":400,"CycleCapacityUsed":600,"CycleEndTime":"2026-11-20 12:00:00"},{"PackageName":"加油包","CapacitySize":500,"CapacityRemain":500}]}}}}`)
	balance, errParse := parseMeterBody(raw)
	if errParse != nil {
		t.Fatal(errParse)
	}
	if balance.Total != 1500 || balance.Remain != 900 {
		t.Fatalf("balance = %v/%v, want 900/1500", balance.Remain, balance.Total)
	}
	now := time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC)
	windows := windowsFromBalance(balance, now)
	if len(windows) != 1 {
		t.Fatalf("windows = %+v", windows)
	}
	if windows[0].usedPercent < 39 || windows[0].usedPercent > 41 {
		t.Fatalf("used = %v, want ~40", windows[0].usedPercent)
	}
	if windows[0].resetsAt.IsZero() {
		t.Fatal("expected package expiry as reset")
	}
}

func TestWindowsTreatZeroUsageAsUnused(t *testing.T) {
	now := time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC)
	windows := windowsFromBalance(balance{Remain: 440, Total: 440, Packages: []balancePackage{{
		Remain: 440, Total: 440, ExpireAt: now.Add(48 * time.Hour).Unix(),
	}}}, now)
	if len(windows) != 1 || windows[0].usedPercent != 0 {
		t.Fatalf("unused windows = %+v", windows)
	}
}

func TestWindowsExhaustEmptyCredits(t *testing.T) {
	now := time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC)
	windows := windowsFromBalance(balance{Remain: 0, Total: 440}, now)
	if len(windows) != 1 || windows[0].usedPercent != 100 || !windows[0].resetsAt.After(now) {
		t.Fatalf("empty credits = %+v, want 100%% used with a future reset", windows)
	}
}

func TestWindowsTreatPartialUsage(t *testing.T) {
	now := time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC)
	windows := windowsFromBalance(balance{Remain: 235, Total: 350}, now)
	if len(windows) != 1 {
		t.Fatal(windows)
	}
	if windows[0].usedPercent < 32 || windows[0].usedPercent > 34 {
		t.Fatalf("used = %v, want ~32.86", windows[0].usedPercent)
	}
}

func TestParseEnterpriseBalance(t *testing.T) {
	raw := []byte(`{"code":0,"data":{"credit":4.11,"limitNum":2000,"cycleResetTime":"2026-12-01 00:00:00"}}`)
	balance, errParse := parseMeterBody(raw)
	if errParse != nil {
		t.Fatal(errParse)
	}
	if !balance.Enterprise || balance.Total != 2000 || balance.Remain < 1995 || balance.Remain > 1996 {
		t.Fatalf("enterprise = %+v", balance)
	}
}

func TestNormalizeWorkBuddy(t *testing.T) {
	if got := Normalize("workbuddyglobal-206cea12.json"); got != "workbuddyglobal" {
		t.Fatalf("Normalize(filename) = %q", got)
	}
	if !IsProvider("workbuddycn") || IsProvider("codex") {
		t.Fatal("provider map")
	}
}

func TestBillingCodeIsAnError(t *testing.T) {
	_, errParse := parseMeterBody([]byte(`{"code":10001,"msg":"fail","data":{}}`))
	if errParse == nil {
		t.Fatal("want billing code error")
	}
}

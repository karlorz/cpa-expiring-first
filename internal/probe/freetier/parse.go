package freetier

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/karlorz/cpa-expiring-first/internal/probe/reading"
)

const (
	statusReady  = "ready"
	statusFailed = "probe_failed"

	packageEndLayout = "2006-01-02 15:04:05"
	meterPath        = "/billing/meter/get-user-resource"
	meterPathV2      = "/v2/billing/meter/get-user-resource"
	enterprisePath   = "/billing/meter/get-enterprise-user-usage"
	enterprisePathV2 = "/v2/billing/meter/get-enterprise-user-usage"
)

var cstZone = time.FixedZone("CST", 8*3600)

type region struct {
	billingBase    string
	origin         string
	acceptLanguage string
	clientVersion  string
	productCode    string
}

func regionOf(provider, domain string) region {
	if Normalize(provider) == "workbuddycn" || strings.Contains(strings.ToLower(domain), "codebuddy.cn") || strings.Contains(strings.ToLower(domain), "workbuddy.cn") {
		return region{
			billingBase:    "https://www.codebuddy.cn",
			origin:         "https://www.workbuddy.cn",
			acceptLanguage: "zh-CN",
			clientVersion:  "5.8.3",
			productCode:    "codebuddy",
		}
	}
	return region{
		billingBase:    "https://www.workbuddy.ai",
		origin:         "https://www.workbuddy.ai",
		acceptLanguage: "en-US",
		clientVersion:  "5.8.4",
		productCode:    "workbuddy",
	}
}

func meterPaths(region region) []string {
	if region.productCode == "workbuddy" {
		return []string{meterPath, meterPathV2}
	}
	return []string{meterPathV2}
}

func enterprisePaths(region region) []string {
	if region.productCode == "workbuddy" {
		return []string{enterprisePath, enterprisePathV2}
	}
	return []string{enterprisePathV2}
}

type balance struct {
	Remain     float64
	Total      float64
	Enterprise bool
	Packages   []balancePackage
}

type balancePackage struct {
	Name     string
	Remain   float64
	Total    float64
	ExpireAt int64
	ResetAt  int64
}

type billingResourceAccount struct {
	PackageName         string `json:"PackageName"`
	CycleEndTime        string `json:"CycleEndTime"`
	CapacitySize        int64  `json:"CapacitySize"`
	CapacityRemain      int64  `json:"CapacityRemain"`
	CapacityUsed        int64  `json:"CapacityUsed"`
	CycleCapacitySize   int64  `json:"CycleCapacitySize"`
	CycleCapacityRemain int64  `json:"CycleCapacityRemain"`
	CycleCapacityUsed   int64  `json:"CycleCapacityUsed"`
}

type billingResourceResponse struct {
	Response struct {
		Data struct {
			Accounts []billingResourceAccount `json:"Accounts"`
		} `json:"Data"`
	} `json:"Response"`
}

type enterpriseUsageResponse struct {
	Credit         *float64 `json:"credit"`
	LimitNum       *float64 `json:"limitNum"`
	CycleEndTime   string   `json:"cycleEndTime"`
	CycleResetTime string   `json:"cycleResetTime"`
}

type apiEnvelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

func parseMeterBody(raw []byte) (balance, error) {
	payload, errEnvelope := unwrapEnvelope(raw)
	if errEnvelope != nil {
		return balance{}, errEnvelope
	}
	if enterprise, okEnterprise := parseEnterpriseBalance(payload); okEnterprise {
		return enterprise, nil
	}
	var personal billingResourceResponse
	if errUnmarshal := json.Unmarshal(payload, &personal); errUnmarshal != nil {
		return balance{}, fmt.Errorf("decode balance failed")
	}
	return parseBalance(personal), nil
}

func unwrapEnvelope(raw []byte) (json.RawMessage, error) {
	var envelope apiEnvelope
	if errUnmarshal := json.Unmarshal(raw, &envelope); errUnmarshal != nil {
		return nil, fmt.Errorf("decode billing body failed")
	}
	var keys map[string]json.RawMessage
	_ = json.Unmarshal(raw, &keys)
	if _, hasCode := keys["code"]; hasCode {
		if envelope.Code != 0 {
			return nil, fmt.Errorf("billing code %d", envelope.Code)
		}
		if len(envelope.Data) == 0 {
			return json.RawMessage(`{}`), nil
		}
		return envelope.Data, nil
	}
	return raw, nil
}

func parseBalance(payload billingResourceResponse) balance {
	out := balance{}
	for _, account := range payload.Response.Data.Accounts {
		remain, _, size := packageRemainUsed(account)
		out.Remain += float64(remain)
		out.Total += float64(size)
		out.Packages = append(out.Packages, balancePackage{
			Name:     strings.TrimSpace(account.PackageName),
			Remain:   float64(remain),
			Total:    float64(size),
			ExpireAt: parsePackageExpiry(account.CycleEndTime),
		})
	}
	return out
}

func packageRemainUsed(account billingResourceAccount) (remain, used, size int64) {
	if account.CycleCapacitySize > 0 {
		remain = account.CycleCapacityRemain
		size = account.CycleCapacitySize
		if remain < 0 {
			remain = 0
		}
		if remain > size {
			remain = size
		}
		used = size - remain
		if account.CycleCapacityUsed > used {
			used = account.CycleCapacityUsed
			if size >= used {
				remain = size - used
			}
		}
		return remain, used, size
	}
	remain = account.CapacityRemain
	used = account.CapacityUsed
	size = account.CapacitySize
	if used == 0 && size > remain {
		used = size - remain
	}
	return remain, used, size
}

func parseEnterpriseBalance(raw json.RawMessage) (balance, bool) {
	var payload enterpriseUsageResponse
	if errUnmarshal := json.Unmarshal(raw, &payload); errUnmarshal != nil || payload.LimitNum == nil {
		return balance{}, false
	}
	total := *payload.LimitNum
	used := 0.0
	if payload.Credit != nil {
		used = *payload.Credit
	}
	remain := total - used
	if remain < 0 {
		remain = 0
	}
	if remain > total {
		remain = total
	}
	return balance{
		Remain:     remain,
		Total:      total,
		Enterprise: true,
		Packages: []balancePackage{{
			Name:    "enterprise",
			Remain:  remain,
			Total:   total,
			ResetAt: parsePackageExpiry(firstNonEmpty(payload.CycleResetTime, payload.CycleEndTime)),
		}},
	}, true
}

func parsePackageExpiry(raw string) int64 {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0
	}
	parsed, errParse := time.ParseInLocation(packageEndLayout, trimmed, cstZone)
	if errParse != nil {
		return 0
	}
	return parsed.Unix()
}

func windowsFromBalance(current balance, now time.Time) []readingWindow {
	if current.Total <= 0 && current.Remain <= 0 {
		return nil
	}
	used := usedPercent(current.Remain, current.Total)
	resetsAt := soonestReset(current.Packages, now, current.Remain > 0)
	if used >= 100 && (resetsAt.IsZero() || !resetsAt.After(now)) {
		// Empty credit packs do not refill when expiry is missing; keep the account
		// exhausted until a later probe sees remaining credits again.
		resetsAt = now.Add(24 * time.Hour)
	}
	minutes := 7 * 24 * 60.0
	if !resetsAt.IsZero() && resetsAt.After(now) {
		minutes = resetsAt.Sub(now).Minutes()
		if minutes < 1 {
			minutes = 1
		}
	}
	return []readingWindow{{minutes: minutes, usedPercent: used, resetsAt: resetsAt}}
}

func soonestReset(packages []balancePackage, now time.Time, remainingOnly bool) time.Time {
	var soonest time.Time
	for _, pkg := range packages {
		if remainingOnly && pkg.Remain <= 0 {
			continue
		}
		at := time.Time{}
		if pkg.ExpireAt > 0 {
			at = time.Unix(pkg.ExpireAt, 0).UTC()
		} else if pkg.ResetAt > 0 {
			at = time.Unix(pkg.ResetAt, 0).UTC()
		}
		if at.IsZero() || !at.After(now) {
			continue
		}
		if soonest.IsZero() || at.Before(soonest) {
			soonest = at
		}
	}
	return soonest
}

// Windows converts a probe result into router windows. FreeTier credits apply to every model.
func Windows(result ProbeResult) []reading.Window {
	if result.Status != statusReady || len(result.Windows) == 0 {
		return nil
	}
	windows := make([]reading.Window, 0, len(result.Windows))
	for _, window := range result.Windows {
		if window.minutes <= 0 {
			continue
		}
		windows = append(windows, reading.Window{
			Minutes:     window.minutes,
			UsedPercent: window.usedPercent,
			ResetsAt:    window.resetsAt,
		})
	}
	return windows
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func usedPercent(remain, total float64) float64 {
	if total <= 0 {
		return 0
	}
	used := 100 * (total - remain) / total
	return math.Min(100, math.Max(0, used))
}

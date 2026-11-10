package freetier

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/karlorz/cpa-expiring-first/internal/probe/host"
)

type httpRawDoer interface {
	HTTPDoRaw(ctx context.Context, req host.HTTPRequest) (host.HTTPResponse, error)
}

// Prober reads FreeTier WorkBuddy credit packages through the host HTTP transport.
type Prober struct {
	host  httpRawDoer
	clock clock
}

// NewProber returns a prober over hostAPI. A nil clock uses the wall clock.
func NewProber(hostAPI httpRawDoer, clockSource clock) Prober {
	if clockSource == nil {
		clockSource = realClock{}
	}
	return Prober{host: hostAPI, clock: clockSource}
}

// ProbeRequest carries the credential fields one WorkBuddy credit probe needs.
type ProbeRequest struct {
	AuthID       string
	AuthIndex    string
	Provider     string
	AccessToken  string
	UID          string
	Domain       string
	EnterpriseID string
	DeviceToken  string
}

// ProbeResult is one FreeTier probe's safe output.
type ProbeResult struct {
	AuthID      string
	AuthIndex   string
	ObservedAt  time.Time
	Remain      float64
	Total       float64
	UsedPercent float64
	ResetsAt    time.Time
	Windows     []readingWindow
	Status      string
	Error       string
}

type readingWindow struct {
	minutes     float64
	usedPercent float64
	resetsAt    time.Time
}

type clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time {
	return time.Now().UTC()
}

// Probe fetches WorkBuddy personal credit packages, then enterprise usage when the
// personal list is empty and the credential has an enterprise id. It retries a path
// only on HTTP 404 (global's unversioned then /v2 meter), never on other errors.
func (p Prober) Probe(ctx context.Context, request ProbeRequest) ProbeResult {
	observedAt := p.clock.Now().UTC()
	region := regionOf(request.Provider, request.Domain)
	personal, errPersonal := p.fetchMeter(ctx, request, region, meterPaths(region), personalMeterBody(observedAt))
	if errPersonal != "" {
		return failedProbe(request, observedAt, errPersonal)
	}
	balance := personal
	if balance.Total <= 0 && strings.TrimSpace(request.EnterpriseID) != "" {
		enterprise, errEnterprise := p.fetchMeter(ctx, request, region, enterprisePaths(region), map[string]any{})
		if errEnterprise != "" {
			return failedProbe(request, observedAt, errEnterprise)
		}
		balance = enterprise
	}
	windows := windowsFromBalance(balance, observedAt)
	if len(windows) == 0 {
		return failedProbe(request, observedAt, "credit quota unavailable")
	}
	result := ProbeResult{
		AuthID:      request.AuthID,
		AuthIndex:   request.AuthIndex,
		ObservedAt:  observedAt,
		Remain:      balance.Remain,
		Total:       balance.Total,
		UsedPercent: windows[0].usedPercent,
		ResetsAt:    windows[0].resetsAt,
		Windows:     windows,
		Status:      statusReady,
	}
	return result
}

func failedProbe(request ProbeRequest, observedAt time.Time, message string) ProbeResult {
	return ProbeResult{
		AuthID:     request.AuthID,
		AuthIndex:  request.AuthIndex,
		ObservedAt: observedAt,
		Status:     statusFailed,
		Error:      safeError(message),
	}
}

func (p Prober) fetchMeter(ctx context.Context, request ProbeRequest, region region, paths []string, body any) (balance, string) {
	encoded, errMarshal := json.Marshal(body)
	if errMarshal != nil {
		return balance{}, "encode billing body failed"
	}
	headers := probeHeaders(request, region)
	var lastErr string
	for _, path := range paths {
		resp, errDo := p.host.HTTPDoRaw(ctx, host.HTTPRequest{
			AuthIndex: request.AuthIndex,
			Method:    http.MethodPost,
			URL:       region.billingBase + path,
			Headers:   headers,
			Body:      encoded,
		})
		if errDo != nil {
			return balance{}, "host http do failed"
		}
		if resp.StatusCode == http.StatusNotFound {
			lastErr = fmt.Sprintf("billing status %d", resp.StatusCode)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return balance{}, fmt.Sprintf("billing status %d", resp.StatusCode)
		}
		parsed, errParse := parseMeterBody(resp.Body)
		if errParse != nil {
			return balance{}, errParse.Error()
		}
		return parsed, ""
	}
	if lastErr == "" {
		lastErr = "billing endpoint is unavailable"
	}
	return balance{}, lastErr
}

func probeHeaders(request ProbeRequest, region region) host.Header {
	token := strings.TrimSpace(request.AccessToken)
	uid := strings.TrimSpace(request.UID)
	headers := host.Header{
		"Accept":              []string{"application/json"},
		"Content-Type":        []string{"application/json"},
		"X-Requested-With":    []string{"XMLHttpRequest"},
		"Origin":              []string{region.origin},
		"Referer":             []string{region.origin + "/"},
		"User-Agent":          []string{"WorkBuddy/" + region.clientVersion},
		"Accept-Language":     []string{region.acceptLanguage},
		"X-CodeBuddy-Request": []string{"1"},
		"X-Product":           []string{"SaaS"},
		"X-Product-Code":      []string{region.productCode},
	}
	if token != "" {
		headers["Authorization"] = []string{"Bearer " + token}
	} else {
		headers["X-No-Authorization"] = []string{"1"}
	}
	if uid != "" {
		headers["X-User-Id"] = []string{uid}
		headers["X-Machine-ID"] = []string{deriveID(uid, "machine")}
		headers["X-Session-ID"] = []string{deriveID(uid, "session")}
	} else {
		headers["X-No-User-Id"] = []string{"1"}
	}
	if enterpriseID := strings.TrimSpace(request.EnterpriseID); enterpriseID != "" {
		headers["X-Enterprise-Id"] = []string{enterpriseID}
		headers["X-Tenant-Id"] = []string{enterpriseID}
	} else {
		headers["X-No-Enterprise-Id"] = []string{"1"}
	}
	domain := strings.TrimSpace(request.Domain)
	if domain == "" {
		domain = strings.TrimPrefix(strings.TrimPrefix(region.origin, "https://"), "http://")
	}
	headers["X-Domain"] = []string{domain}
	if device := strings.TrimSpace(request.DeviceToken); device != "" {
		headers["X-Device-Token"] = []string{device}
	}
	return headers
}

func deriveID(uid, purpose string) string {
	sum := sha256.Sum256([]byte("wb2a:" + purpose + ":" + uid))
	return hex.EncodeToString(sum[:18])
}

func safeError(message string) string {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		return "probe failed"
	}
	return trimmed
}

func personalMeterBody(now time.Time) map[string]any {
	stamp := now.In(cstZone).Format(packageEndLayout)
	end := now.In(cstZone).Add(365 * 101 * 24 * time.Hour).Format(packageEndLayout)
	return map[string]any{
		"PageNumber":               1,
		"PageSize":                 100,
		"ProductCode":              "p_tcaca",
		"Status":                   []int{0, 3},
		"PackageEndTimeRangeBegin": stamp,
		"PackageEndTimeRangeEnd":   end,
	}
}

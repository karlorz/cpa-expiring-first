package freetier

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/karlorz/cpa-expiring-first/internal/probe/host"
)

type stubHTTP struct {
	calls []host.HTTPRequest
	serve func(req host.HTTPRequest) (host.HTTPResponse, error)
}

func (s *stubHTTP) HTTPDoRaw(ctx context.Context, req host.HTTPRequest) (host.HTTPResponse, error) {
	s.calls = append(s.calls, req)
	return s.serve(req)
}

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now.UTC() }

func TestProbeUsesPersonalMeterThenMapsWindows(t *testing.T) {
	now := time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC)
	stub := &stubHTTP{serve: func(req host.HTTPRequest) (host.HTTPResponse, error) {
		if req.Method != http.MethodPost || !strings.Contains(req.URL, "/billing/meter/get-user-resource") {
			t.Fatalf("unexpected request %s %s", req.Method, req.URL)
		}
		if got := req.Headers["User-Agent"][0]; got != "WorkBuddy/5.8.4" {
			t.Fatalf("User-Agent = %q", got)
		}
		if !strings.HasPrefix(req.Headers["Authorization"][0], "Bearer ") {
			t.Fatal("missing bearer")
		}
		body := `{"code":0,"data":{"Response":{"Data":{"Accounts":[{"PackageName":"基础套餐","CycleCapacitySize":440,"CycleCapacityRemain":0,"CycleCapacityUsed":440,"CycleEndTime":"2026-11-20 12:00:00"}]}}}}`
		return host.HTTPResponse{StatusCode: http.StatusOK, Body: []byte(body)}, nil
	}}
	result := NewProber(stub, fixedClock{now: now}).Probe(context.Background(), ProbeRequest{
		AuthID:      "workbuddyglobal-a.json",
		AuthIndex:   "idx",
		Provider:    "workbuddyglobal",
		AccessToken: "tok",
		UID:         "uid-1",
	})
	if result.Error != "" {
		t.Fatal(result.Error)
	}
	windows := Windows(result)
	if len(windows) != 1 || windows[0].UsedPercent != 100 {
		t.Fatalf("windows = %+v", windows)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("calls = %d, want one personal meter request", len(stub.calls))
	}
}

func TestProbeFallsBackToV2On404Only(t *testing.T) {
	stub := &stubHTTP{serve: func(req host.HTTPRequest) (host.HTTPResponse, error) {
		if strings.Contains(req.URL, "/v2/") {
			return host.HTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"code":0,"data":{"Response":{"Data":{"Accounts":[{"CapacitySize":590,"CapacityRemain":590}]}}}}`)}, nil
		}
		return host.HTTPResponse{StatusCode: http.StatusNotFound, Body: []byte(`not found`)}, nil
	}}
	result := NewProber(stub, nil).Probe(context.Background(), ProbeRequest{
		Provider:    "workbuddyglobal",
		AccessToken: "tok",
		UID:         "uid",
	})
	if result.Error != "" || result.UsedPercent != 0 {
		t.Fatalf("result = %+v", result)
	}
	if len(stub.calls) != 2 {
		t.Fatalf("calls = %d, want 404 then v2", len(stub.calls))
	}
}

func TestProbeEnterpriseWhenPersonalEmpty(t *testing.T) {
	stub := &stubHTTP{serve: func(req host.HTTPRequest) (host.HTTPResponse, error) {
		if strings.Contains(req.URL, "enterprise") {
			return host.HTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"code":0,"data":{"credit":10,"limitNum":100,"cycleResetTime":"2026-12-01 00:00:00"}}`)}, nil
		}
		return host.HTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"code":0,"data":{"Response":{"Data":{"Accounts":[]}}}}`)}, nil
	}}
	result := NewProber(stub, nil).Probe(context.Background(), ProbeRequest{
		Provider:     "workbuddyglobal",
		AccessToken:  "tok",
		UID:          "uid",
		EnterpriseID: "ent-1",
	})
	if result.Error != "" || result.Total != 100 || result.Remain != 90 {
		t.Fatalf("result = %+v", result)
	}
}

func TestProbeDoesNotRetryNon404(t *testing.T) {
	stub := &stubHTTP{serve: func(req host.HTTPRequest) (host.HTTPResponse, error) {
		return host.HTTPResponse{StatusCode: http.StatusForbidden, Body: []byte(`{"code":11140}`)}, nil
	}}
	result := NewProber(stub, nil).Probe(context.Background(), ProbeRequest{
		Provider:    "workbuddyglobal",
		AccessToken: "tok",
		UID:         "uid",
	})
	if result.Error == "" || len(stub.calls) != 1 {
		t.Fatalf("error=%q calls=%d", result.Error, len(stub.calls))
	}
}

func TestPersonalMeterBodyHasRequiredFields(t *testing.T) {
	body := personalMeterBody(time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC))
	raw, _ := json.Marshal(body)
	if !strings.Contains(string(raw), `"ProductCode":"p_tcaca"`) || !strings.Contains(string(raw), `"PageSize":100`) {
		t.Fatalf("body = %s", raw)
	}
}

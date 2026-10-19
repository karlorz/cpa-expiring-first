package antigravity

import (
	"context"
	"encoding/json"
	"github.com/karlorz/cpa-expiring-first/internal/probe/host"
	"github.com/karlorz/cpa-expiring-first/internal/probe/reading"
	"net/http"
	"strings"
	"testing"
)

type rawHost struct {
	calls  []host.HTTPRequest
	status int
}

func (h *rawHost) HTTPDoRaw(_ context.Context, req host.HTTPRequest) (host.HTTPResponse, error) {
	h.calls = append(h.calls, req)
	if len(h.calls) == 1 {
		return host.HTTPResponse{StatusCode: h.status}, nil
	}
	return host.HTTPResponse{StatusCode: 200, Body: []byte(`{"groups":[{"displayName":"Gemini Models","buckets":[{"window":"weekly","remainingFraction":0.86,"resetTime":"2026-10-24T04:25:56Z"}]}]}`)}, nil
}
func TestProbeIdentityAndEndpointFallback(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound} {
		h := &rawHost{status: status}
		result := NewProber(h, nil).Probe(context.Background(), ProbeRequest{AuthID: "test", AccessToken: "private-token", ProjectID: "project", ModelGroup: ModelGroupGemini})
		if result.Status != reading.StatusReady || len(h.calls) != 2 {
			t.Fatalf("status %d: result=%+v calls=%d", status, result, len(h.calls))
		}
		for _, req := range h.calls {
			if req.Headers["User-Agent"][0] != "antigravity/hub/2.23.0 darwin/arm64" || req.Headers["Authorization"][0] != "Bearer private-token" {
				t.Fatal("unexpected request identity")
			}
			var body map[string]string
			if json.Unmarshal(req.Body, &body) != nil || body["project"] != "project" {
				t.Fatal("missing project body")
			}
		}
	}
}
func TestProbeRejectsMissingCredentialContext(t *testing.T) {
	for _, req := range []ProbeRequest{{AccessToken: "token"}, {ProjectID: "project"}} {
		h := &rawHost{}
		result := NewProber(h, nil).Probe(context.Background(), req)
		if result.Status == reading.StatusReady || len(h.calls) != 0 || !strings.Contains(result.Error, "missing") {
			t.Fatalf("result=%+v calls=%d", result, len(h.calls))
		}
	}
}
func TestUnknownWindowCannotBecomeWeeklyEvidence(t *testing.T) {
	r := ParseAvailableModels([]byte(`{"models":{"gemini-test":{"quotaInfo":{"remainingFraction":0.5,"resetTime":"2026-10-24T04:25:56Z"}}}}`), realClock{}.Now(), ModelGroupGemini)
	if r.Status == reading.StatusReady || len(r.Windows) > 0 {
		t.Fatalf("untyped evidence accepted: %+v", r)
	}
}
func TestDuplicateWeeklyWindowsChooseWorstDeterministically(t *testing.T) {
	for _, buckets := range []string{
		`{"window":"weekly","remainingFraction":0.8,"resetTime":"2026-10-24T04:25:56Z"},{"window":"weekly","remainingFraction":0.1,"resetTime":"2026-10-25T04:25:56Z"}`,
		`{"window":"weekly","remainingFraction":0.1,"resetTime":"2026-10-25T04:25:56Z"},{"window":"weekly","remainingFraction":0.8,"resetTime":"2026-10-24T04:25:56Z"}`,
	} {
		r := ParseAvailableModels([]byte(`{"groups":[{"displayName":"Gemini Models","buckets":[`+buckets+`]}]}`), realClock{}.Now(), ModelGroupGemini)
		if len(r.Windows) != 1 || r.Windows[0].Remaining != 10 {
			t.Fatalf("result = %+v", r)
		}
	}
}

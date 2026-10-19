package antigravity

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/karlorz/cpa-expiring-first/internal/probe/host"
	"github.com/karlorz/cpa-expiring-first/internal/probe/reading"
)

type httpDoer interface {
	HTTPDoRaw(ctx context.Context, req host.HTTPRequest) (host.HTTPResponse, error)
}

// Prober runs Antigravity quota summary probes through the host HTTP transport.
type Prober struct {
	host  httpDoer
	clock clock
}

// NewProber returns a prober over hostAPI. A nil clock uses the wall clock.
func NewProber(hostAPI httpDoer, clockSource clock) Prober {
	if clockSource == nil {
		clockSource = realClock{}
	}
	return Prober{host: hostAPI, clock: clockSource}
}

// Probe requests the quota summary and returns the target model group's reading.
func (p Prober) Probe(ctx context.Context, request ProbeRequest) ProbeResult {
	observedAt := p.clock.Now().UTC()
	if strings.TrimSpace(request.ProjectID) == "" {
		return failedProbe(request, observedAt, "missing quota project ID")
	}
	if strings.TrimSpace(request.AccessToken) == "" {
		return failedProbe(request, observedAt, "missing access token")
	}
	lastStatus := 0
	for _, url := range retrieveUserQuotaSummaryURLs {
		response, errDo := p.host.HTTPDoRaw(ctx, host.HTTPRequest{
			AuthIndex: request.AuthIndex,
			Method:    http.MethodPost,
			URL:       url,
			Headers:   probeHeaders(request),
			Body:      probeBody(request),
		})
		if errDo != nil {
			lastStatus = 0
			continue
		}
		lastStatus = response.StatusCode
		if response.StatusCode != http.StatusOK {
			continue
		}
		result := ParseAvailableModels(response.Body, observedAt, request.ModelGroup)
		if result.Status == reading.StatusReady {
			result.AuthID = request.AuthID
			result.AuthIndex = request.AuthIndex
			return result
		}
	}
	if lastStatus == 0 {
		return failedProbe(request, observedAt, "quota transport failed")
	}
	return failedProbe(request, observedAt, fmt.Sprintf("retrieve quota summary status %d", lastStatus))
}

func probeBody(request ProbeRequest) []byte {
	projectID := strings.TrimSpace(request.ProjectID)
	if projectID == "" {
		return nil
	}
	body, errMarshal := json.Marshal(struct {
		Project string `json:"project"`
	}{Project: projectID})
	if errMarshal != nil {
		return nil
	}
	return body
}

func probeHeaders(request ProbeRequest) host.Header {
	headers := host.Header{
		"Accept":        []string{"application/json"},
		"Authorization": []string{"Bearer " + strings.TrimSpace(request.AccessToken)},
		"Content-Type":  []string{"application/json"},
		"User-Agent":    []string{"antigravity/hub/2.23.0 darwin/arm64"},
	}
	return headers
}

func failedProbe(request ProbeRequest, observedAt time.Time, message string) ProbeResult {
	result := failedResult(observedAt, request.ModelGroup, safeError(message))
	result.AuthID = request.AuthID
	result.AuthIndex = request.AuthIndex
	return result
}

func safeError(message string) string {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		return "probe failed"
	}
	return trimmed
}

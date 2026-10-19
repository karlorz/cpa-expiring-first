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
	HTTPDo(ctx context.Context, req host.HTTPRequest) (host.HTTPResponse, error)
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
	lastStatus := 0
	for _, url := range retrieveUserQuotaSummaryURLs {
		response, errDo := p.host.HTTPDo(ctx, host.HTTPRequest{
			AuthIndex: request.AuthIndex,
			Method:    http.MethodPost,
			URL:       url,
			Headers:   probeHeaders(request),
			Body:      probeBody(request),
		})
		if errDo != nil {
			return failedProbe(request, observedAt, "host http do failed")
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
		"Authorization": []string{"Bearer $TOKEN$"},
		"Content-Type":  []string{"application/json"},
		"User-Agent":    []string{"antigravity/cli/1.0.8 darwin/arm64"},
	}
	if token := strings.TrimSpace(request.AccessToken); token != "" {
		headers["Authorization"] = []string{"Bearer " + token}
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

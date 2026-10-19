package codex

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/karlorz/cpa-expiring-first/internal/probe/host"
)

type httpDoer interface {
	HTTPDo(ctx context.Context, req host.HTTPRequest) (host.HTTPResponse, error)
}

// Prober runs Codex wham usage probes through the host HTTP transport.
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

// Probe requests wham usage and returns a result holding only safe fields.
func (p Prober) Probe(ctx context.Context, request ProbeRequest) ProbeResult {
	observedAt := p.clock.Now().UTC()
	response, errDo := p.host.HTTPDo(ctx, host.HTTPRequest{
		AuthIndex: request.AuthIndex,
		Method:    http.MethodGet,
		URL:       WhamUsageURL,
		Headers:   probeHeaders(request),
	})
	if errDo != nil {
		return failedProbe(request, observedAt, "host http do failed")
	}
	if response.StatusCode != http.StatusOK {
		return failedProbe(request, observedAt, fmt.Sprintf("wham usage status %d", response.StatusCode))
	}
	result := ParseWhamUsage(response.Body, observedAt)
	result.AuthID = request.AuthID
	result.AuthIndex = request.AuthIndex
	return result
}

func failedProbe(request ProbeRequest, observedAt time.Time, message string) ProbeResult {
	result := failedResult(observedAt, safeError(message))
	result.AuthID = request.AuthID
	result.AuthIndex = request.AuthIndex
	return result
}

func probeHeaders(request ProbeRequest) host.Header {
	token := "$TOKEN$"
	if accessToken := strings.TrimSpace(request.AccessToken); accessToken != "" {
		token = accessToken
	}
	headers := host.Header{
		"Accept":        []string{"application/json"},
		"Authorization": []string{"Bearer " + token},
		"Content-Type":  []string{"application/json"},
		"User-Agent":    []string{"codex_cli_rs/0.76.0 (Debian 13.0.0; x86_64) WindowsTerminal"},
	}
	if accountID := strings.TrimSpace(request.AccountID); accountID != "" {
		headers["Chatgpt-Account-Id"] = []string{accountID}
	}
	return headers
}

func safeError(message string) string {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		return "probe failed"
	}
	return trimmed
}

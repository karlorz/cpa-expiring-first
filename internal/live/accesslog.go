package live

import (
	"os"
	"regexp"
	"time"
)

// AccessLogLimitWindow is how long a Live POST 429 without a parsed reset is remembered.
const AccessLogLimitWindow = 7 * 24 * time.Hour

var realtimeCallLine = regexp.MustCompile(
	`\[(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})\].*(201|429)\s+\|.*POST\s+\x22/v1/realtime/calls\x22`,
)

var accessLogZone = time.FixedZone("CST", 8*3600)

// RealtimeCall is one gin access-log POST /v1/realtime/calls result.
type RealtimeCall struct {
	Status int
	At     time.Time
}

// LastRealtimeCallAfterFile reads path and returns the latest realtime POST after since.
func LastRealtimeCallAfterFile(path string, since time.Time) (RealtimeCall, bool) {
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return RealtimeCall{}, false
	}
	return LastRealtimeCallAfter(raw, since)
}

// LastRealtimeCallAfter returns the latest realtime POST after since. Hangup paths are ignored.
func LastRealtimeCallAfter(raw []byte, since time.Time) (RealtimeCall, bool) {
	matches := realtimeCallLine.FindAllSubmatch(raw, -1)
	var latest RealtimeCall
	found := false
	for _, match := range matches {
		if len(match) < 3 {
			continue
		}
		at, errParse := time.ParseInLocation("2006-01-02 15:04:05", string(match[1]), accessLogZone)
		if errParse != nil {
			continue
		}
		at = at.UTC()
		if !since.IsZero() && !at.After(since) {
			continue
		}
		status := 0
		switch string(match[2]) {
		case "201":
			status = 201
		case "429":
			status = 429
		default:
			continue
		}
		if !found || at.After(latest.At) {
			latest = RealtimeCall{Status: status, At: at}
			found = true
		}
	}
	return latest, found
}

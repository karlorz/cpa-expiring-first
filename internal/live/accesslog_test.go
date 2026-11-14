package live

import (
	"testing"
	"time"
)

func TestLastRealtimeCallAfterIgnoresHangupAndKeepsLatest(t *testing.T) {
	raw := []byte(`[2026-11-05 23:29:54] [info ] 201 | 801ms | POST    "/v1/realtime/calls"
[2026-11-05 23:30:05] [warn ] 404 | 0s | POST    "/v1/realtime/calls/rtc_x/hangup"
[2026-11-05 23:37:18] [warn ] 429 | 743ms | POST    "/v1/realtime/calls"
`)
	since := time.Date(2026, 11, 5, 15, 29, 54, 0, time.UTC) // 23:29:54 CST
	call, ok := LastRealtimeCallAfter(raw, since)
	if !ok || call.Status != 429 || call.At.Unix() != time.Date(2026, 11, 5, 15, 37, 18, 0, time.UTC).Unix() {
		t.Fatalf("LastRealtimeCallAfter() = %+v ok=%v, want 429 at 23:37:18 CST", call, ok)
	}
}

func TestLastRealtimeCallAfterIgnoresHangup429(t *testing.T) {
	raw := []byte(`[2026-11-05 23:37:18] [warn ] 429 | 0s | POST    "/v1/realtime/calls/rtc_x/hangup"
`)
	if _, ok := LastRealtimeCallAfter(raw, time.Time{}); ok {
		t.Fatal("LastRealtimeCallAfter() matched a hangup path")
	}
}

func TestAccessLogLimitWindowMatchesStatusMessageFallback(t *testing.T) {
	if AccessLogLimitWindow != liveLimitFallback {
		t.Fatalf("AccessLogLimitWindow = %s, want liveLimitFallback %s", AccessLogLimitWindow, liveLimitFallback)
	}
	if AccessLogLimitWindow != time.Hour {
		t.Fatalf("AccessLogLimitWindow = %s, want 1h", AccessLogLimitWindow)
	}
}

func TestLastRealtimeCallAfterIgnoresOlderLines(t *testing.T) {
	raw := []byte(`[2026-11-05 23:37:18] [warn ] 429 | 743ms | POST    "/v1/realtime/calls"
`)
	since := time.Date(2026, 11, 5, 15, 37, 18, 0, time.UTC)
	if _, ok := LastRealtimeCallAfter(raw, since); ok {
		t.Fatal("LastRealtimeCallAfter() matched a 429 at the previous pick time")
	}
}

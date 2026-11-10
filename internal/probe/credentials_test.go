package probe

import (
	"encoding/json"
	"testing"
)

func TestParseAuthMaterialWorkBuddyNested(t *testing.T) {
	raw := json.RawMessage(`{"auth":{"accessToken":"at","domain":"www.workbuddy.ai","realm":"global"},"account":{"uid":"u-1","enterpriseId":"ent"},"device_token":"dev"}`)
	got := parseAuthMaterial(raw)
	if got.AccessToken != "at" || got.UID != "u-1" || got.Domain != "www.workbuddy.ai" || got.EnterpriseID != "ent" || got.DeviceToken != "dev" {
		t.Fatalf("material = %+v", got)
	}
}

func TestNormalizeProviderWorkBuddy(t *testing.T) {
	if got := normalizeProvider("workbuddyglobal"); got != "workbuddyglobal" || !supportedProvider(got) {
		t.Fatalf("normalizeProvider = %q", got)
	}
	if normalizeProvider("gemini") != "" {
		t.Fatal("unknown provider must stay empty")
	}
}

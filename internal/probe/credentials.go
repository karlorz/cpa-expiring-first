package probe

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/karlorz/cpa-expiring-first/internal/probe/host"
)

// authMaterial is the non-secret slice of a credential's JSON that a probe needs.
type authMaterial struct {
	AccessToken      string
	AccountID        string
	ProjectID        string
	BaseURL          string
	AuthKind         string
	UserID           string
	OrganizationUUID string
}

// parseAuthMaterial reads the probe fields from a credential JSON document. Only the fields a
// probe request needs are kept; tokens are never logged or returned to callers beyond the
// probe call itself.
func parseAuthMaterial(raw json.RawMessage) authMaterial {
	var document struct {
		AccessToken      string `json:"access_token"`
		SessionKey       string `json:"session_key"`
		Token            string `json:"token"`
		AccountID        string `json:"account_id"`
		ProjectID        string `json:"project_id"`
		QuotaProjectID   string `json:"quota_project_id"`
		Project          string `json:"project"`
		BaseURL          string `json:"base_url"`
		AuthKind         string `json:"auth_kind"`
		Sub              string `json:"sub"`
		Subject          string `json:"subject"`
		UserID           string `json:"user_id"`
		OrganizationUUID string `json:"organization_uuid"`
		OrgUUID          string `json:"org_uuid"`
		OrganizationID   string `json:"organization_id"`
		OrgID            string `json:"org_id"`
		Organization     string `json:"organization"`
	}
	if errUnmarshal := json.Unmarshal(raw, &document); errUnmarshal != nil {
		return authMaterial{}
	}
	return authMaterial{
		AccessToken:      firstNonEmpty(document.AccessToken, document.SessionKey, document.Token),
		AccountID:        strings.TrimSpace(document.AccountID),
		ProjectID:        firstNonEmpty(document.ProjectID, document.QuotaProjectID, document.Project),
		BaseURL:          strings.TrimSpace(document.BaseURL),
		AuthKind:         strings.TrimSpace(document.AuthKind),
		UserID:           firstNonEmpty(document.Sub, document.Subject, document.UserID),
		OrganizationUUID: firstNonEmpty(document.OrganizationUUID, document.OrgUUID, document.OrganizationID, document.OrgID, document.Organization),
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// credential is one host credential reduced to what a probe needs.
type credential struct {
	id        string
	authIndex string
	provider  string
	material  authMaterial
}

// listCredentials reads the host's credentials and the material each probe needs.
func listCredentials(ctx context.Context, client *host.Client) ([]credential, error) {
	files, errList := client.ListAuthFiles(ctx)
	if errList != nil {
		return nil, errList
	}
	credentials := make([]credential, 0, len(files))
	for _, file := range files {
		provider := normalizeProvider(file.Provider)
		if provider == "" {
			provider = normalizeProvider(file.Type)
		}
		if !supportedProvider(provider) {
			continue
		}
		item := credential{
			id:        strings.TrimSpace(file.ID),
			authIndex: strings.TrimSpace(file.AuthIndex),
			provider:  provider,
		}
		if item.authIndex != "" {
			if raw, errRead := client.GetAuthJSON(ctx, item.authIndex); errRead == nil {
				item.material = parseAuthMaterial(raw)
			}
		}
		credentials = append(credentials, item)
	}
	return credentials, nil
}

// normalizeProvider maps a host provider or type key to one of the probed providers, or "".
func normalizeProvider(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "antigravity":
		return "antigravity"
	case "codex", "chatgpt", "chat-gpt":
		return "codex"
	case "claude", "claude-oauth", "claude_oauth", "anthropic":
		return "claude"
	case "xai", "x-ai":
		return "xai"
	default:
		return ""
	}
}

func supportedProvider(provider string) bool {
	switch provider {
	case "antigravity", "codex", "claude", "xai":
		return true
	default:
		return false
	}
}

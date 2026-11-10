package probe

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/karlorz/cpa-expiring-first/internal/probe/freetier"
	"github.com/karlorz/cpa-expiring-first/internal/probe/host"
)

// authMaterial contains the credential fields needed only during a probe.
type authMaterial struct {
	AccessToken      string
	AccountID        string
	ProjectID        string
	BaseURL          string
	AuthKind         string
	UserID           string
	OrganizationUUID string
	UID              string
	Domain           string
	EnterpriseID     string
	DeviceToken      string
}

// parseAuthMaterial reads the probe fields from a credential JSON document. Only the fields a
// probe request needs are kept; tokens are never logged or returned to callers beyond the
// probe call itself.
func parseAuthMaterial(raw json.RawMessage) authMaterial {
	var document struct {
		AccessToken       string          `json:"access_token"`
		AccessTokenCamel  string          `json:"accessToken"`
		SessionKey        string          `json:"session_key"`
		Token             json.RawMessage `json:"token"`
		AccountID         string          `json:"account_id"`
		ProjectID         string          `json:"project_id"`
		QuotaProjectID    string          `json:"quota_project_id"`
		Project           string          `json:"project"`
		BaseURL           string          `json:"base_url"`
		AuthKind          string          `json:"auth_kind"`
		Sub               string          `json:"sub"`
		Subject           string          `json:"subject"`
		UserID            string          `json:"user_id"`
		UID               string          `json:"uid"`
		Domain            string          `json:"domain"`
		EnterpriseID      string          `json:"enterpriseId"`
		EnterpriseIDSnake string          `json:"enterprise_id"`
		DeviceToken       string          `json:"device_token"`
		DeviceTokenCamel  string          `json:"deviceToken"`
		OrganizationUUID  string          `json:"organization_uuid"`
		OrgUUID           string          `json:"org_uuid"`
		OrganizationID    string          `json:"organization_id"`
		OrgID             string          `json:"org_id"`
		Organization      string          `json:"organization"`
		Auth              json.RawMessage `json:"auth"`
		Account           json.RawMessage `json:"account"`
	}
	if errUnmarshal := json.Unmarshal(raw, &document); errUnmarshal != nil {
		return authMaterial{}
	}
	nestedAuth := objectStrings(document.Auth)
	nestedAccount := objectStrings(document.Account)
	var nested map[string]json.RawMessage
	_ = json.Unmarshal(raw, &nested)
	for _, key := range []string{"metadata", "attributes", "token"} {
		value := nested[key]
		if len(value) == 0 || value[0] != '{' {
			continue
		}
		fields := objectStrings(value)
		document.AccessToken = firstNonEmpty(document.AccessToken, fields["access_token"], fields["accessToken"])
		document.ProjectID = firstNonEmpty(document.ProjectID, fields["project_id"], fields["projectId"], fields["gemini_virtual_project"])
	}
	return authMaterial{
		AccessToken: firstNonEmpty(
			document.AccessToken,
			document.AccessTokenCamel,
			nestedAuth["accessToken"],
			nestedAuth["access_token"],
			document.SessionKey,
			tokenString(document.Token),
		),
		AccountID:        strings.TrimSpace(document.AccountID),
		ProjectID:        firstNonEmpty(document.ProjectID, document.QuotaProjectID, document.Project),
		BaseURL:          strings.TrimSpace(document.BaseURL),
		AuthKind:         strings.TrimSpace(document.AuthKind),
		UserID:           firstNonEmpty(document.Sub, document.Subject, document.UserID, document.UID, nestedAccount["uid"]),
		OrganizationUUID: firstNonEmpty(document.OrganizationUUID, document.OrgUUID, document.OrganizationID, document.OrgID, document.Organization),
		UID:              firstNonEmpty(document.UID, nestedAccount["uid"], nestedAccount["UID"]),
		Domain:           firstNonEmpty(document.Domain, nestedAuth["domain"]),
		EnterpriseID:     firstNonEmpty(document.EnterpriseID, document.EnterpriseIDSnake, nestedAccount["enterpriseId"], nestedAccount["enterprise_id"]),
		DeviceToken:      firstNonEmpty(document.DeviceToken, document.DeviceTokenCamel),
	}
}

func objectStrings(raw json.RawMessage) map[string]string {
	if len(raw) == 0 || raw[0] != '{' {
		return nil
	}
	var fields map[string]any
	if json.Unmarshal(raw, &fields) != nil {
		return nil
	}
	out := make(map[string]string, len(fields))
	for key, value := range fields {
		text, _ := value.(string)
		out[key] = text
	}
	return out
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
		if provider == "" {
			provider = normalizeProvider(file.ID)
		}
		if !supportedProvider(provider) || file.Disabled || strings.TrimSpace(file.ID) == "" {
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
		item.material.ProjectID = firstNonEmpty(file.ProjectID, item.material.ProjectID)
		item.material.BaseURL = firstNonEmpty(file.BaseURL, item.material.BaseURL)
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
		return freetier.Normalize(value)
	}
}

func supportedProvider(provider string) bool {
	switch provider {
	case "antigravity", "codex", "claude", "xai":
		return true
	default:
		return freetier.IsProvider(provider)
	}
}

func tokenString(raw json.RawMessage) string {
	var token string
	_ = json.Unmarshal(raw, &token)
	return token
}

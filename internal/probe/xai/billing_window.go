package xai

import (
	"encoding/json"
	"strings"
	"time"
)

// oauthBillingDocument models only the weekly-window fields of the Management Center billing
// contract, accepting the camelCase and snake_case spellings the endpoint uses.
type oauthBillingDocument struct {
	Config *oauthBillingConfig `json:"config"`
}

type oauthBillingConfig struct {
	CurrentPeriod         *oauthBillingPeriod `json:"currentPeriod"`
	CurrentPeriodSnake    *oauthBillingPeriod `json:"current_period"`
	BillingPeriodEnd      string              `json:"billingPeriodEnd"`
	BillingPeriodEndSnake string              `json:"billing_period_end"`
}

type oauthBillingPeriod struct {
	Type string `json:"type"`
	End  string `json:"end"`
}

// ParseWeeklyLongWindowReset extracts the weekly window reset from an OAuth billing response.
// Only config.currentPeriod(type=weekly).end counts: a monthly period and billingPeriodEnd are
// billing metadata, not a rate-limit capacity reset. Malformed or unsupported input yields
// (nil, nil) rather than an error.
func ParseWeeklyLongWindowReset(body []byte) (*time.Time, error) {
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil, nil
	}
	var document oauthBillingDocument
	if errUnmarshal := json.Unmarshal(body, &document); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	if document.Config == nil {
		return nil, nil
	}
	period := document.Config.CurrentPeriod
	if period == nil {
		period = document.Config.CurrentPeriodSnake
	}
	if period == nil {
		return nil, nil
	}
	if !strings.EqualFold(strings.TrimSpace(period.Type), "weekly") {
		return nil, nil
	}
	endRaw := strings.TrimSpace(period.End)
	if endRaw == "" {
		return nil, nil
	}
	end, errParse := parseRFC3339UTC(endRaw)
	if errParse != nil {
		return nil, nil
	}
	return &end, nil
}

func parseRFC3339UTC(raw string) (time.Time, error) {
	if parsed, errParse := time.Parse(time.RFC3339Nano, raw); errParse == nil {
		return parsed.UTC(), nil
	}
	parsed, errParse := time.Parse(time.RFC3339, raw)
	if errParse != nil {
		return time.Time{}, errParse
	}
	return parsed.UTC(), nil
}

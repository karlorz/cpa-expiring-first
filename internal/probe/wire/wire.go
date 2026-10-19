// Package wire holds the tolerant JSON and time helpers the provider parsers share. Provider
// quota responses mix snake_case, camelCase and numeric-or-string encodings, so every value is
// decoded into any and coerced here.
package wire

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"
)

// ToString coerces a decoded JSON value to a non-empty trimmed string.
func ToString(raw any) (string, bool) {
	switch value := raw.(type) {
	case string:
		trimmed := strings.TrimSpace(value)
		return trimmed, trimmed != ""
	case json.Number:
		trimmed := strings.TrimSpace(value.String())
		return trimmed, trimmed != ""
	default:
		return "", false
	}
}

// ToInt64 coerces a decoded JSON value to an int64.
func ToInt64(raw any) (int64, bool) {
	switch value := raw.(type) {
	case float64:
		return int64(value), true
	case json.Number:
		integer, errParse := value.Int64()
		return integer, errParse == nil
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return 0, false
		}
		integer, errParse := strconv.ParseInt(trimmed, 10, 64)
		return integer, errParse == nil
	default:
		return 0, false
	}
}

// ToFloat64 coerces a decoded JSON value to a float64.
func ToFloat64(raw any) (float64, bool) {
	switch value := raw.(type) {
	case float64:
		return value, true
	case json.Number:
		floatValue, errParse := value.Float64()
		return floatValue, errParse == nil
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return 0, false
		}
		floatValue, errParse := strconv.ParseFloat(trimmed, 64)
		return floatValue, errParse == nil
	default:
		return 0, false
	}
}

// ToBool coerces a decoded JSON value to a bool.
func ToBool(raw any) (bool, bool) {
	switch value := raw.(type) {
	case bool:
		return value, true
	case string:
		parsed, errParse := strconv.ParseBool(strings.TrimSpace(value))
		return parsed, errParse == nil
	default:
		return false, false
	}
}

// ParseAnyTime decodes a reset timestamp given as RFC3339, a Unix second or millisecond number,
// or a numeric string.
func ParseAnyTime(raw any) (*time.Time, bool) {
	switch value := raw.(type) {
	case nil:
		return nil, false
	case string:
		return ParseTimeString(value)
	case float64:
		return parseUnix(int64(value))
	case json.Number:
		integer, errParse := value.Int64()
		if errParse != nil {
			return nil, false
		}
		return parseUnix(integer)
	default:
		return nil, false
	}
}

// ParseTimeString decodes one timestamp string.
func ParseTimeString(value string) (*time.Time, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, false
	}
	if integer, errParse := strconv.ParseInt(trimmed, 10, 64); errParse == nil {
		return parseUnix(integer)
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		parsed, errParse := time.Parse(layout, trimmed)
		if errParse == nil {
			utc := parsed.UTC()
			return &utc, true
		}
	}
	return nil, false
}

func parseUnix(value int64) (*time.Time, bool) {
	if value <= 0 {
		return nil, false
	}
	if value > 1_000_000_000_000 {
		parsed := time.UnixMilli(value).UTC()
		return &parsed, true
	}
	parsed := time.Unix(value, 0).UTC()
	return &parsed, true
}

// ClampPercent rounds a remaining percentage up into 0..100.
func ClampPercent(value float64) int64 {
	if value <= 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return int64(math.Ceil(value))
}

// HeaderValue returns the first non-empty value of an HTTP header, ignoring case.
func HeaderValue(headers map[string][]string, target string) string {
	for key, values := range headers {
		if !strings.EqualFold(strings.TrimSpace(key), target) {
			continue
		}
		for _, value := range values {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

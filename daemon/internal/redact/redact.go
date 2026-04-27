package redact

import (
	"encoding/json"
	"net/http"
	"strings"
)

const Placeholder = "[redacted]"

var sensitiveHeaders = map[string]bool{
	"authorization":       true,
	"cookie":              true,
	"proxy-authorization": true,
	"set-cookie":          true,
	"x-csrf-token":        true,
	"x-xsrf-token":        true,
}

var sensitiveFields = []string{
	"access_token",
	"api_key",
	"auth",
	"authorization",
	"csrf",
	"id_token",
	"key",
	"mfa",
	"otp",
	"passcode",
	"password",
	"refresh_token",
	"secret",
	"session",
	"token",
}

func Header(headers http.Header) http.Header {
	redacted := make(http.Header, len(headers))
	for key, values := range headers {
		if sensitiveHeaders[strings.ToLower(key)] {
			redacted[key] = []string{Placeholder}
			continue
		}
		redacted[key] = append([]string(nil), values...)
	}
	return redacted
}

func HeaderHasSensitive(headers http.Header) bool {
	for key := range headers {
		if sensitiveHeaders[strings.ToLower(key)] {
			return true
		}
	}
	return false
}

func JSON(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			if SensitiveName(key) {
				out[key] = Placeholder
				continue
			}
			out[key] = JSON(child)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			out[i] = JSON(child)
		}
		return out
	default:
		return value
	}
}

func Body(body []byte) []byte {
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return body
	}
	redacted := JSON(parsed)
	data, err := json.Marshal(redacted)
	if err != nil {
		return body
	}
	return data
}

func SensitiveName(name string) bool {
	lower := strings.ToLower(name)
	for _, field := range sensitiveFields {
		if lower == field || strings.Contains(lower, field) {
			return true
		}
	}
	return false
}

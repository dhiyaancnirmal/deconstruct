package exporter

import (
	"sort"
	"strings"

	"github.com/dhiyaan/deconstruct/daemon/internal/store"
)

func Curl(flow store.Flow, requestBody []byte) (string, error) {
	headers, requestBody, err := redactedExportInputs(flow, requestBody)
	if err != nil {
		return "", err
	}

	parts := []string{"curl", "-X", shellQuote(flow.Method)}
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := headers[key]
		parts = append(parts, "-H", shellQuote(key+": "+value))
	}
	if len(requestBody) > 0 {
		parts = append(parts, "--data-binary", shellQuote(string(requestBody)))
	}
	parts = append(parts, shellQuote(flow.URL))
	return strings.Join(parts, " "), nil
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

package exporter

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/dhiyaan/deconstruct/daemon/internal/redact"
	"github.com/dhiyaan/deconstruct/daemon/internal/store"
)

func TypeScript(flow store.Flow, requestBody []byte) (string, error) {
	headers, body, err := redactedExportInputs(flow, requestBody)
	if err != nil {
		return "", err
	}
	headerJSON, _ := json.MarshalIndent(headers, "", "  ")
	bodyJSON, _ := json.Marshal(string(body))
	return fmt.Sprintf(`export async function run() {
  const response = await fetch(%q, {
    method: %q,
    headers: %s,
    body: %s,
  });

  if (!response.ok) {
    throw new Error("Request failed: " + response.status);
  }

  return response.text();
}
`, flow.URL, flow.Method, string(headerJSON), bodyJSON), nil
}

func Python(flow store.Flow, requestBody []byte) (string, error) {
	headers, body, err := redactedExportInputs(flow, requestBody)
	if err != nil {
		return "", err
	}
	headerJSON, _ := json.MarshalIndent(headers, "", "    ")
	bodyJSON, _ := json.Marshal(string(body))
	return fmt.Sprintf(`import requests


def run():
    response = requests.request(
        %q,
        %q,
        headers=%s,
        data=%s,
        timeout=60,
    )
    response.raise_for_status()
    return response.text
`, flow.Method, flow.URL, string(headerJSON), bodyJSON), nil
}

func redactedExportInputs(flow store.Flow, requestBody []byte) (map[string]string, []byte, error) {
	headers := http.Header{}
	if len(flow.RequestHeaders) > 0 {
		if err := json.Unmarshal(flow.RequestHeaders, &headers); err != nil {
			return nil, nil, fmt.Errorf("decode request headers: %w", err)
		}
	}
	headers = redact.Header(headers)
	requestBody = redact.Body(requestBody)
	output := map[string]string{}
	keys := make([]string, 0, len(headers))
	for key := range headers {
		if strings.EqualFold(key, "Host") || strings.EqualFold(key, "Proxy-Connection") {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		output[key] = strings.Join(headers.Values(key), ", ")
	}
	return output, requestBody, nil
}

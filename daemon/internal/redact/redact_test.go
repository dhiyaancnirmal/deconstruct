package redact

import (
	"net/http"
	"testing"
)

func TestHeaderRedaction(t *testing.T) {
	headers := http.Header{
		"Authorization": []string{"Bearer secret"},
		"Accept":        []string{"application/json"},
	}
	redacted := Header(headers)
	if redacted.Get("Authorization") != Placeholder {
		t.Fatalf("expected authorization redaction, got %q", redacted.Get("Authorization"))
	}
	if redacted.Get("Accept") != "application/json" {
		t.Fatalf("unexpected accept header")
	}
	if !HeaderHasSensitive(headers) {
		t.Fatal("expected sensitive header detection")
	}
}

func TestBodyRedaction(t *testing.T) {
	body := Body([]byte(`{"token":"abc","nested":{"password":"pw"},"safe":"ok"}`))
	got := string(body)
	if got != `{"nested":{"password":"[redacted]"},"safe":"ok","token":"[redacted]"}` {
		t.Fatalf("unexpected body: %s", got)
	}
}

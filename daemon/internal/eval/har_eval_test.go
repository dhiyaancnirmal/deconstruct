package eval

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dhiyaan/deconstruct/daemon/internal/blobstore"
	"github.com/dhiyaan/deconstruct/daemon/internal/store"
)

func TestRunHARCompilesExportsAndReplaysWorkflow(t *testing.T) {
	var actionHeader string
	var actionBody string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/assets/app.js":
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = w.Write([]byte("console.log('noise')"))
		case "/csrf":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"csrf":"csrf123"}`))
		case "/items":
			actionHeader = r.Header.Get("X-CSRF")
			body := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(body)
			actionBody = string(body)
			if actionHeader != "csrf123" {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"ok":false}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"ok":true,"id":"item_1"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer target.Close()

	harPath := filepath.Join(t.TempDir(), "scenario.har")
	if err := os.WriteFile(harPath, []byte(scenarioHAR(target.URL)), 0o644); err != nil {
		t.Fatal(err)
	}
	db, blobs := evalDeps(t)
	outDir := filepath.Join(t.TempDir(), "out")
	report, err := RunHAR(context.Background(), db, blobs, HAREvalParams{
		Name:      "csrf-create-item",
		HARPath:   harPath,
		OutputDir: outDir,
		Replay:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if Failed(report) {
		t.Fatalf("expected passing eval, got %#v", report)
	}
	if report.ImportedFlows != 3 || report.IncludedSteps != 2 || report.ExcludedSteps != 1 {
		t.Fatalf("unexpected report counts: %#v", report)
	}
	if report.ExtractedKeys == 0 || report.BoundTemplates == 0 {
		t.Fatalf("expected compiler bindings in report: %#v", report)
	}
	if actionHeader != "csrf123" || !strings.Contains(actionBody, `"name":"old"`) {
		t.Fatalf("workflow did not replay captured action: header=%q body=%q", actionHeader, actionBody)
	}
	generated, err := os.ReadFile(filepath.Join(outDir, "typescript", "src", "workflow.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(generated), "readPath") || !strings.Contains(string(generated), "${steps.step_02.") {
		t.Fatalf("generated TypeScript did not preserve workflow graph:\n%s", generated)
	}
}

func evalDeps(t *testing.T) (*store.Store, *blobstore.Store) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	blobs, err := blobstore.Open(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	return db, blobs
}

func scenarioHAR(baseURL string) string {
	return fmt.Sprintf(`{
  "log": {
    "entries": [
      {
        "startedDateTime": "2026-04-27T10:00:00Z",
        "time": 5,
        "request": {"method": "GET", "url": "%[1]s/assets/app.js", "headers": []},
        "response": {"status": 200, "headers": [{"name":"Content-Type","value":"application/javascript"}], "content": {"mimeType":"application/javascript", "text": "console.log('noise')"}}
      },
      {
        "startedDateTime": "2026-04-27T10:00:01Z",
        "time": 10,
        "request": {"method": "GET", "url": "%[1]s/csrf", "headers": [{"name":"Accept","value":"application/json"}]},
        "response": {"status": 200, "headers": [{"name":"Content-Type","value":"application/json"}], "content": {"mimeType":"application/json", "text": "{\"csrf\":\"csrf123\"}"}}
      },
      {
        "startedDateTime": "2026-04-27T10:00:02Z",
        "time": 20,
        "request": {
          "method": "POST",
          "url": "%[1]s/items",
          "headers": [{"name":"Content-Type","value":"application/json"},{"name":"X-CSRF","value":"csrf123"}],
          "postData": {"mimeType":"application/json", "text": "{\"name\":\"old\",\"csrf\":\"csrf123\"}"}
        },
        "response": {"status": 201, "headers": [{"name":"Content-Type","value":"application/json"}], "content": {"mimeType":"application/json", "text": "{\"ok\":true,\"id\":\"item_1\"}"}}
      }
    ]
  }
}`, baseURL)
}

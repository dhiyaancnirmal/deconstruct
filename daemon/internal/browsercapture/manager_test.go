package browsercapture

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/dhiyaan/deconstruct/daemon/internal/blobstore"
	"github.com/dhiyaan/deconstruct/daemon/internal/store"
	"github.com/gorilla/websocket"
)

func TestManagerCapturesNetworkEvents(t *testing.T) {
	upgrader := websocket.Upgrader{}
	events := make(chan map[string]any, 8)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/json/version":
			wsURL := "ws" + server.URL[len("http"):] + "/devtools/browser/test"
			_ = json.NewEncoder(w).Encode(map[string]string{"webSocketDebuggerUrl": wsURL})
		case "/devtools/browser/test":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			for {
				var message map[string]any
				if err := conn.ReadJSON(&message); err != nil {
					return
				}
				id := message["id"]
				method := message["method"]
				switch method {
				case "Network.enable":
					if err := conn.WriteJSON(map[string]any{"id": id, "result": map[string]any{}}); err != nil {
						t.Fatal(err)
					}
					for _, event := range []map[string]any{
						{
							"method": "Network.requestWillBeSent",
							"params": map[string]any{
								"requestId": "req_1",
								"request": map[string]any{
									"method":   "POST",
									"url":      "https://example.test/api",
									"headers":  map[string]any{"Content-Type": "application/json", "Authorization": "Bearer secret"},
									"postData": `{"name":"one"}`,
								},
							},
						},
						{
							"method": "Network.responseReceived",
							"params": map[string]any{
								"requestId": "req_1",
								"response": map[string]any{
									"status":  float64(http.StatusCreated),
									"headers": map[string]any{"Content-Type": "application/json"},
								},
							},
						},
						{
							"method": "Network.loadingFinished",
							"params": map[string]any{"requestId": "req_1"},
						},
					} {
						events <- event
						if err := conn.WriteJSON(event); err != nil {
							t.Fatal(err)
						}
					}
				case "Network.getResponseBody":
					if err := conn.WriteJSON(map[string]any{"id": id, "result": map[string]any{"body": `{"ok":true}`, "base64Encoded": false}}); err != nil {
						t.Fatal(err)
					}
					return
				}
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	_ = events
	db, blobs := browserTestStore(t)
	manager := NewManager(db, blobs)
	ctx := context.Background()
	status, err := manager.Start(ctx, StartParams{Endpoint: server.URL, SessionName: "CDP test"})
	if err != nil {
		t.Fatal(err)
	}
	if !status.Running || status.SessionID == "" {
		t.Fatalf("unexpected status: %#v", status)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status = manager.Status()
		if status.Flows == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	manager.Stop()
	result, err := db.QueryFlows(ctx, store.FlowQuery{SessionID: status.SessionID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || result.Flows[0].Source != "browser_cdp" {
		t.Fatalf("unexpected captured flows: %#v", result)
	}
	flow, err := db.GetFlow(ctx, result.Flows[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	var headers http.Header
	if err := json.Unmarshal(flow.RequestHeaders, &headers); err != nil {
		t.Fatal(err)
	}
	if headers.Get("Authorization") != "[redacted]" {
		t.Fatalf("expected redacted auth header, got %#v", headers)
	}
	tags, err := db.FlowTags(ctx, flow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasTag(tags, "browser_fidelity") {
		t.Fatalf("expected browser_fidelity tag, got %#v", tags)
	}
}

func browserTestStore(t *testing.T) (*store.Store, *blobstore.Store) {
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

func hasTag(tags []string, target string) bool {
	for _, tag := range tags {
		if tag == target {
			return true
		}
	}
	return false
}

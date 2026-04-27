package harimport

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/dhiyaan/deconstruct/daemon/internal/blobstore"
	"github.com/dhiyaan/deconstruct/daemon/internal/store"
)

func TestImportHAR(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	blobs, err := blobstore.Open(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	har := []byte(`{
	  "log": {
	    "entries": [
	      {
	        "startedDateTime": "2026-04-27T10:00:00Z",
	        "time": 25,
	        "request": {
	          "method": "POST",
	          "url": "https://api.example.test/graphql",
	          "headers": [{"name":"Content-Type","value":"application/json"},{"name":"Authorization","value":"Bearer secret"}],
	          "postData": {"mimeType":"application/json","text":"{\"query\":\"query Me { me { id } }\",\"variables\":{\"token\":\"secret\"}}"}
	        },
	        "response": {
	          "status": 200,
	          "headers": [{"name":"Content-Type","value":"application/json"}],
	          "content": {"mimeType":"application/json","text":"{\"data\":{\"me\":{\"id\":\"1\"}}}"}
	        }
	      }
	    ]
	  }
	}`)
	result, err := New(db, blobs).Import(context.Background(), har, Options{Name: "Test HAR"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Flows != 1 || result.SessionID == "" {
		t.Fatalf("unexpected result: %#v", result)
	}
	query, err := db.QueryFlows(context.Background(), store.FlowQuery{Tag: "graphql", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if query.Total != 1 || len(query.Flows) != 1 || query.Flows[0].AppName != "HAR" {
		t.Fatalf("unexpected imported flow: %#v", query)
	}
	flow, err := db.GetFlow(context.Background(), query.Flows[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(flow.RequestHeaders) == "" {
		t.Fatal("expected request headers")
	}
}

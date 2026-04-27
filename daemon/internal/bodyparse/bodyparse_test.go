package bodyparse

import (
	"bytes"
	"compress/gzip"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
)

func TestGraphQLJSONParse(t *testing.T) {
	headers := http.Header{"Content-Type": []string{"application/json"}}
	parsed := Parse(headers, []byte(`{"operationName":"ListThings","query":"query ListThings { things { id } }","variables":{"limit":2}}`))
	if parsed.GraphQL == nil || parsed.GraphQL.OperationName != "ListThings" {
		t.Fatalf("unexpected graphql parse: %#v", parsed.GraphQL)
	}
}

func TestFormParse(t *testing.T) {
	headers := http.Header{"Content-Type": []string{"application/x-www-form-urlencoded"}}
	parsed := Parse(headers, []byte(`name=dhiyaan&query=query+Thing`))
	if parsed.Form.Get("name") != "dhiyaan" || parsed.GraphQL == nil {
		t.Fatalf("unexpected form parse: %#v", parsed)
	}
}

func TestMultipartParse(t *testing.T) {
	var body bytes.Buffer
	writer := multipartWriter(t, &body)
	if err := writer.WriteField("name", "demo"); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("upload", "demo.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("hello"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	headers := http.Header{"Content-Type": []string{writer.FormDataContentType()}}
	parsed := Parse(headers, body.Bytes())
	if len(parsed.Multipart) != 2 {
		t.Fatalf("unexpected multipart parse: %#v", parsed.Multipart)
	}
}

func TestGzipDecode(t *testing.T) {
	var body bytes.Buffer
	writer := gzip.NewWriter(&body)
	_, _ = writer.Write([]byte(`{"ok":true}`))
	_ = writer.Close()
	headers := http.Header{
		"Content-Type":     []string{"application/json"},
		"Content-Encoding": []string{"gzip"},
	}
	parsed := Parse(headers, body.Bytes())
	if parsed.Encoding != "gzip" || !strings.Contains(parsed.Text, `"ok":true`) {
		t.Fatalf("unexpected gzip parse: %#v", parsed)
	}
}

func TestAdvancedProtocolParsers(t *testing.T) {
	sse := Parse(http.Header{"Content-Type": []string{"text/event-stream"}}, []byte("event: update\nid: 1\ndata: hello\n\n"))
	if len(sse.SSE) != 1 || sse.SSE[0].Data != "hello" {
		t.Fatalf("unexpected sse parse: %#v", sse.SSE)
	}
	protobufBody := []byte{0x0a, 0x05, 'h', 'e', 'l', 'l', 'o'}
	protobuf := Parse(http.Header{"Content-Type": []string{"application/x-protobuf"}}, protobufBody)
	if protobuf.Protobuf == nil || len(protobuf.Protobuf.Fields) != 1 || protobuf.Protobuf.Fields[0].Number != 1 {
		t.Fatalf("unexpected protobuf parse: %#v", protobuf.Protobuf)
	}
	grpc := Parse(http.Header{"Content-Type": []string{"application/grpc"}, ":path": []string{"/demo.Service/Create"}}, append([]byte{0, 0, 0, 0, byte(len(protobufBody))}, protobufBody...))
	if grpc.GRPC == nil || grpc.GRPC.Service != "demo.Service" || grpc.Protobuf == nil {
		t.Fatalf("unexpected grpc parse: %#v", grpc)
	}
	tags := Tags(http.Header{"Content-Type": []string{"application/grpc"}, ":path": []string{"/demo.Service/Create"}}, append([]byte{0, 0, 0, 0, byte(len(protobufBody))}, protobufBody...))
	if !hasTag(tags, "grpc") || !hasTag(tags, "protobuf") {
		t.Fatalf("unexpected tags: %#v", tags)
	}
}

func hasTag(tags []string, target string) bool {
	for _, tag := range tags {
		if tag == target {
			return true
		}
	}
	return false
}

func multipartWriter(t *testing.T, body *bytes.Buffer) *multipart.Writer {
	t.Helper()
	return multipart.NewWriter(body)
}

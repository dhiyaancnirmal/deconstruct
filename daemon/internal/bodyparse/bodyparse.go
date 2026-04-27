package bodyparse

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

type Parsed struct {
	ContentType string          `json:"content_type,omitempty"`
	Encoding    string          `json:"encoding,omitempty"`
	Text        string          `json:"text,omitempty"`
	JSON        any             `json:"json,omitempty"`
	Form        url.Values      `json:"form,omitempty"`
	Multipart   []MultipartPart `json:"multipart,omitempty"`
	GraphQL     *GraphQL        `json:"graphql,omitempty"`
	SSE         []SSEEvent      `json:"sse,omitempty"`
	GRPC        *GRPC           `json:"grpc,omitempty"`
	Protobuf    *Protobuf       `json:"protobuf,omitempty"`
}

type MultipartPart struct {
	Name        string `json:"name,omitempty"`
	FileName    string `json:"file_name,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Size        int64  `json:"size"`
}

type GraphQL struct {
	OperationName string         `json:"operation_name,omitempty"`
	Query         string         `json:"query,omitempty"`
	Variables     map[string]any `json:"variables,omitempty"`
}

type SSEEvent struct {
	Event string `json:"event,omitempty"`
	ID    string `json:"id,omitempty"`
	Data  string `json:"data,omitempty"`
}

type GRPC struct {
	Service string `json:"service,omitempty"`
	Method  string `json:"method,omitempty"`
	Web     bool   `json:"web,omitempty"`
}

type Protobuf struct {
	Fields []ProtobufField `json:"fields,omitempty"`
}

type ProtobufField struct {
	Number   uint64 `json:"number"`
	WireType uint64 `json:"wire_type"`
	Value    string `json:"value,omitempty"`
}

func Parse(headers http.Header, body []byte) Parsed {
	decoded, encoding := Decode(headers, body)
	mediaType, params, _ := mime.ParseMediaType(headers.Get("Content-Type"))
	result := Parsed{
		ContentType: mediaType,
		Encoding:    encoding,
		Text:        string(decoded),
	}
	switch mediaType {
	case "application/json", "application/graphql-response+json":
		parseJSON(decoded, &result)
	case "application/x-www-form-urlencoded":
		if values, err := url.ParseQuery(string(decoded)); err == nil {
			result.Form = values
			result.GraphQL = graphqlFromForm(values)
		}
	case "multipart/form-data":
		result.Multipart = parseMultipart(decoded, params["boundary"])
	case "text/event-stream":
		result.SSE = parseSSE(string(decoded))
	}
	if strings.HasPrefix(mediaType, "application/grpc") || strings.Contains(mediaType, "grpc-web") {
		result.GRPC = grpcFromHeaders(headers)
	}
	if strings.Contains(mediaType, "protobuf") || strings.HasPrefix(mediaType, "application/grpc") || strings.Contains(mediaType, "grpc-web") {
		result.Protobuf = parseProtobuf(decoded)
	}
	if result.GraphQL == nil && strings.Contains(strings.ToLower(result.Text), "query") {
		var payload map[string]any
		if err := json.Unmarshal(decoded, &payload); err == nil {
			result.GraphQL = graphqlFromJSON(payload)
		}
	}
	if result.JSON == nil {
		parseJSON(decoded, &result)
	}
	return result
}

func Decode(headers http.Header, body []byte) ([]byte, string) {
	encoding := strings.ToLower(strings.TrimSpace(headers.Get("Content-Encoding")))
	switch encoding {
	case "gzip":
		reader, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return body, encoding
		}
		defer reader.Close()
		decoded, err := io.ReadAll(reader)
		if err != nil {
			return body, encoding
		}
		return decoded, encoding
	case "br":
		decoded, err := io.ReadAll(brotli.NewReader(bytes.NewReader(body)))
		if err != nil {
			return body, encoding
		}
		return decoded, encoding
	case "zstd":
		reader, err := zstd.NewReader(bytes.NewReader(body))
		if err != nil {
			return body, encoding
		}
		defer reader.Close()
		decoded, err := io.ReadAll(reader)
		if err != nil {
			return body, encoding
		}
		return decoded, encoding
	default:
		return body, encoding
	}
}

func parseJSON(body []byte, result *Parsed) {
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return
	}
	result.JSON = parsed
	if object, ok := parsed.(map[string]any); ok {
		result.GraphQL = graphqlFromJSON(object)
	}
}

func graphqlFromJSON(payload map[string]any) *GraphQL {
	query, _ := payload["query"].(string)
	if query == "" {
		return nil
	}
	operationName, _ := payload["operationName"].(string)
	var variables map[string]any
	if rawVariables, ok := payload["variables"].(map[string]any); ok {
		variables = rawVariables
	}
	return &GraphQL{OperationName: operationName, Query: query, Variables: variables}
}

func graphqlFromForm(values url.Values) *GraphQL {
	query := values.Get("query")
	if query == "" {
		return nil
	}
	var variables map[string]any
	if raw := values.Get("variables"); raw != "" {
		_ = json.Unmarshal([]byte(raw), &variables)
	}
	return &GraphQL{
		OperationName: values.Get("operationName"),
		Query:         query,
		Variables:     variables,
	}
}

func parseMultipart(body []byte, boundary string) []MultipartPart {
	if boundary == "" {
		return nil
	}
	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	var parts []MultipartPart
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		size, _ := io.Copy(io.Discard, part)
		parts = append(parts, MultipartPart{
			Name:        part.FormName(),
			FileName:    part.FileName(),
			ContentType: part.Header.Get("Content-Type"),
			Size:        size,
		})
	}
	return parts
}

func parseSSE(text string) []SSEEvent {
	var events []SSEEvent
	current := SSEEvent{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			if current.Event != "" || current.ID != "" || current.Data != "" {
				events = append(events, current)
			}
			current = SSEEvent{}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if ok {
			value = strings.TrimPrefix(value, " ")
		}
		switch key {
		case "event":
			current.Event = value
		case "id":
			current.ID = value
		case "data":
			if current.Data != "" {
				current.Data += "\n"
			}
			current.Data += value
		}
	}
	if current.Event != "" || current.ID != "" || current.Data != "" {
		events = append(events, current)
	}
	return events
}

func grpcFromHeaders(headers http.Header) *GRPC {
	path := headers.Get(":path")
	if path == "" {
		path = headers.Get("X-Original-Path")
	}
	result := &GRPC{Web: strings.Contains(strings.ToLower(headers.Get("Content-Type")), "grpc-web")}
	if strings.HasPrefix(path, "/") {
		parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
		if len(parts) >= 2 {
			result.Service = parts[0]
			result.Method = parts[1]
		}
	}
	return result
}

func parseProtobuf(body []byte) *Protobuf {
	if len(body) == 0 {
		return nil
	}
	if len(body) > 5 && body[0] <= 1 {
		messageLength := int(binary.BigEndian.Uint32(body[1:5]))
		if messageLength >= 0 && messageLength <= len(body)-5 {
			body = body[5 : 5+messageLength]
		}
	}
	fields := []ProtobufField{}
	for offset := 0; offset < len(body) && len(fields) < 64; {
		key, n := readVarint(body[offset:])
		if n <= 0 {
			break
		}
		offset += n
		field := ProtobufField{Number: key >> 3, WireType: key & 0x7}
		switch field.WireType {
		case 0:
			value, read := readVarint(body[offset:])
			if read <= 0 {
				return &Protobuf{Fields: fields}
			}
			field.Value = fmt.Sprint(value)
			offset += read
		case 1:
			if offset+8 > len(body) {
				return &Protobuf{Fields: fields}
			}
			field.Value = fmt.Sprintf("fixed64:%x", body[offset:offset+8])
			offset += 8
		case 2:
			length, read := readVarint(body[offset:])
			if read <= 0 || int(length) < 0 || offset+read+int(length) > len(body) {
				return &Protobuf{Fields: fields}
			}
			offset += read
			value := body[offset : offset+int(length)]
			if isPrintable(value) {
				field.Value = string(value)
			} else {
				field.Value = fmt.Sprintf("bytes:%d", len(value))
			}
			offset += int(length)
		case 5:
			if offset+4 > len(body) {
				return &Protobuf{Fields: fields}
			}
			field.Value = fmt.Sprintf("fixed32:%x", body[offset:offset+4])
			offset += 4
		default:
			return &Protobuf{Fields: fields}
		}
		if field.Number == 0 {
			break
		}
		fields = append(fields, field)
	}
	if len(fields) == 0 {
		return nil
	}
	return &Protobuf{Fields: fields}
}

func readVarint(body []byte) (uint64, int) {
	var value uint64
	for i := 0; i < len(body) && i < 10; i++ {
		value |= uint64(body[i]&0x7f) << (7 * i)
		if body[i] < 0x80 {
			return value, i + 1
		}
	}
	return 0, -1
}

func isPrintable(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	for _, b := range data {
		if b < 0x20 || b > 0x7e {
			return false
		}
	}
	return true
}

func Tags(headers http.Header, body []byte) []string {
	parsed := Parse(headers, body)
	tags := map[string]bool{}
	if parsed.GraphQL != nil {
		tags["graphql"] = true
	}
	if len(parsed.Multipart) > 0 {
		tags["multipart"] = true
	}
	if parsed.Form != nil {
		tags["form"] = true
	}
	if len(parsed.SSE) > 0 || strings.EqualFold(parsed.ContentType, "text/event-stream") {
		tags["sse"] = true
	}
	if parsed.GRPC != nil {
		tags["grpc"] = true
		if parsed.GRPC.Web {
			tags["grpc_web"] = true
		}
	}
	if parsed.Protobuf != nil && len(parsed.Protobuf.Fields) > 0 {
		tags["protobuf"] = true
	}
	if parsed.Encoding != "" {
		tags["compressed"] = true
	}
	if strings.EqualFold(headers.Get("Upgrade"), "websocket") {
		tags["websocket"] = true
	}
	var out []string
	for tag := range tags {
		out = append(out, tag)
	}
	return out
}

func MustJSON(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("marshal json: %v", err))
	}
	return data
}

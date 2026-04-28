package protoschema

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/dhiyaan/deconstruct/daemon/internal/id"
	"github.com/dhiyaan/deconstruct/daemon/internal/store"
)

type Store interface {
	SaveProtocolSchema(context.Context, store.ProtocolSchema) error
}

type ImportParams struct {
	Name   string `json:"name,omitempty"`
	Source string `json:"source"`
}

type Summary struct {
	Package  string    `json:"package,omitempty"`
	Messages []Message `json:"messages,omitempty"`
	Services []Service `json:"services,omitempty"`
	Enums    []string  `json:"enums,omitempty"`
}

type Message struct {
	Name   string  `json:"name"`
	Fields []Field `json:"fields,omitempty"`
}

type Field struct {
	Number   int    `json:"number"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Repeated bool   `json:"repeated,omitempty"`
}

type Service struct {
	Name string `json:"name"`
	RPCs []RPC  `json:"rpcs,omitempty"`
}

type RPC struct {
	Name            string `json:"name"`
	RequestType     string `json:"request_type"`
	ResponseType    string `json:"response_type"`
	ClientStreaming bool   `json:"client_streaming,omitempty"`
	ServerStreaming bool   `json:"server_streaming,omitempty"`
}

func Import(ctx context.Context, db Store, params ImportParams) (store.ProtocolSchema, error) {
	if strings.TrimSpace(params.Source) == "" {
		return store.ProtocolSchema{}, fmt.Errorf("proto source is required")
	}
	summary := Parse(params.Source)
	name := params.Name
	if name == "" {
		name = summary.Package
	}
	if name == "" && len(summary.Services) > 0 {
		name = summary.Services[0].Name
	}
	if name == "" && len(summary.Messages) > 0 {
		name = summary.Messages[0].Name
	}
	if name == "" {
		name = "protobuf schema"
	}
	schemaID, err := id.New("schema")
	if err != nil {
		return store.ProtocolSchema{}, err
	}
	summaryJSON, err := json.Marshal(summary)
	if err != nil {
		return store.ProtocolSchema{}, err
	}
	schema := store.ProtocolSchema{
		ID:        schemaID,
		Name:      name,
		Kind:      "protobuf",
		Source:    params.Source,
		Summary:   summaryJSON,
		CreatedAt: time.Now().UTC(),
	}
	if err := db.SaveProtocolSchema(ctx, schema); err != nil {
		return store.ProtocolSchema{}, err
	}
	return schema, nil
}

func Parse(source string) Summary {
	cleaned := stripComments(source)
	summary := Summary{}
	if match := regexp.MustCompile(`(?m)\bpackage\s+([A-Za-z_][A-Za-z0-9_.]*)\s*;`).FindStringSubmatch(cleaned); len(match) == 2 {
		summary.Package = match[1]
	}
	for _, match := range regexp.MustCompile(`(?s)\bmessage\s+([A-Za-z_][A-Za-z0-9_]*)\s*\{(.*?)\}`).FindAllStringSubmatch(cleaned, -1) {
		message := Message{Name: match[1], Fields: parseFields(match[2])}
		summary.Messages = append(summary.Messages, message)
	}
	for _, match := range regexp.MustCompile(`(?s)\bservice\s+([A-Za-z_][A-Za-z0-9_]*)\s*\{(.*?)\}`).FindAllStringSubmatch(cleaned, -1) {
		service := Service{Name: match[1], RPCs: parseRPCs(match[2])}
		summary.Services = append(summary.Services, service)
	}
	for _, match := range regexp.MustCompile(`(?m)\benum\s+([A-Za-z_][A-Za-z0-9_]*)\s*\{`).FindAllStringSubmatch(cleaned, -1) {
		summary.Enums = append(summary.Enums, match[1])
	}
	sort.Slice(summary.Messages, func(i, j int) bool { return summary.Messages[i].Name < summary.Messages[j].Name })
	sort.Slice(summary.Services, func(i, j int) bool { return summary.Services[i].Name < summary.Services[j].Name })
	sort.Strings(summary.Enums)
	return summary
}

func parseFields(body string) []Field {
	var fields []Field
	fieldRe := regexp.MustCompile(`(?m)^\s*(repeated\s+)?([A-Za-z_][A-Za-z0-9_.<>]*)\s+([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(\d+)`)
	for _, match := range fieldRe.FindAllStringSubmatch(body, -1) {
		var number int
		_, _ = fmt.Sscanf(match[4], "%d", &number)
		fields = append(fields, Field{Number: number, Name: match[3], Type: match[2], Repeated: strings.TrimSpace(match[1]) != ""})
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].Number < fields[j].Number })
	return fields
}

func parseRPCs(body string) []RPC {
	var rpcs []RPC
	rpcRe := regexp.MustCompile(`(?m)\brpc\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(\s*(stream\s+)?([A-Za-z_][A-Za-z0-9_.]*)\s*\)\s*returns\s*\(\s*(stream\s+)?([A-Za-z_][A-Za-z0-9_.]*)\s*\)`)
	for _, match := range rpcRe.FindAllStringSubmatch(body, -1) {
		rpcs = append(rpcs, RPC{
			Name:            match[1],
			RequestType:     match[3],
			ResponseType:    match[5],
			ClientStreaming: strings.TrimSpace(match[2]) != "",
			ServerStreaming: strings.TrimSpace(match[4]) != "",
		})
	}
	sort.Slice(rpcs, func(i, j int) bool { return rpcs[i].Name < rpcs[j].Name })
	return rpcs
}

func stripComments(source string) string {
	block := regexp.MustCompile(`(?s)/\*.*?\*/`)
	line := regexp.MustCompile(`(?m)//.*$`)
	return line.ReplaceAllString(block.ReplaceAllString(source, ""), "")
}

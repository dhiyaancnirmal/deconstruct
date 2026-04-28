package protoschema

import "testing"

func TestParseProtoSummary(t *testing.T) {
	summary := Parse(`
		syntax = "proto3";
		package demo.chat;

		message SendRequest {
			string channel_id = 1;
			repeated string body = 2;
		}

		message SendResponse {
			string id = 1;
		}

		service ChatService {
			rpc Send(SendRequest) returns (SendResponse);
			rpc Stream(stream SendRequest) returns (stream SendResponse);
		}
	`)
	if summary.Package != "demo.chat" || len(summary.Messages) != 2 || len(summary.Services) != 1 {
		t.Fatalf("unexpected summary: %#v", summary)
	}
	if len(summary.Services[0].RPCs) != 2 {
		t.Fatalf("expected rpcs: %#v", summary.Services[0])
	}
}

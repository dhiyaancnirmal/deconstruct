package truststore

import (
	"context"
	"strings"
	"testing"
)

type fakeRunner struct {
	calls []string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	return nil, nil
}

func TestTrustRoot(t *testing.T) {
	runner := &fakeRunner{}
	manager := New(runner)
	if err := manager.TrustRoot(context.Background(), "/tmp/deconstruct-root-ca.pem"); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(runner.calls, "\n")
	if !strings.Contains(got, "security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain /tmp/deconstruct-root-ca.pem") {
		t.Fatalf("unexpected calls:\n%s", got)
	}
}

func TestRevokeRoot(t *testing.T) {
	runner := &fakeRunner{}
	manager := New(runner)
	if err := manager.RevokeRoot(context.Background(), "/tmp/deconstruct-root-ca.pem"); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(runner.calls, "\n")
	if !strings.Contains(got, "security remove-trusted-cert -d /tmp/deconstruct-root-ca.pem") {
		t.Fatalf("unexpected calls:\n%s", got)
	}
}

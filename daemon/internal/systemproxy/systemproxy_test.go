package systemproxy

import (
	"context"
	"strings"
	"testing"
)

type fakeRunner struct {
	calls []string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	call := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, call)
	if len(args) > 0 && (args[0] == "-getwebproxy" || args[0] == "-getsecurewebproxy") {
		return []byte("Enabled: No\nServer: old.proxy\nPort: 8080\nAuthenticated Proxy Enabled: 0\n"), nil
	}
	return nil, nil
}

func TestEnableAndDisableRestoresPriorState(t *testing.T) {
	runner := &fakeRunner{}
	manager := New(t.TempDir(), runner)
	ctx := context.Background()
	if err := manager.Enable(ctx, "Wi-Fi", "127.0.0.1", 18080); err != nil {
		t.Fatal(err)
	}
	if err := manager.Disable(ctx, "Wi-Fi"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(runner.calls, "\n")
	for _, want := range []string{
		"networksetup -getwebproxy Wi-Fi",
		"networksetup -getsecurewebproxy Wi-Fi",
		"networksetup -setwebproxy Wi-Fi 127.0.0.1 18080",
		"networksetup -setsecurewebproxy Wi-Fi 127.0.0.1 18080",
		"networksetup -setwebproxystate Wi-Fi on",
		"networksetup -setsecurewebproxystate Wi-Fi on",
		"networksetup -setwebproxy Wi-Fi old.proxy 8080",
		"networksetup -setsecurewebproxy Wi-Fi old.proxy 8080",
		"networksetup -setwebproxystate Wi-Fi off",
		"networksetup -setsecurewebproxystate Wi-Fi off",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing call %q in:\n%s", want, joined)
		}
	}
}

func TestParseNetworkSetupProxy(t *testing.T) {
	config, err := parseNetworkSetupProxy([]byte("Enabled: Yes\nServer: proxy.local\nPort: 8888\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !config.Enabled || config.Server != "proxy.local" || config.Port != 8888 {
		t.Fatalf("unexpected config: %#v", config)
	}
}

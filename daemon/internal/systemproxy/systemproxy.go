package systemproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

type Manager struct {
	stateDir string
	runner   Runner
}

type ProxyState struct {
	Service string      `json:"service"`
	HTTP    ProxyConfig `json:"http"`
	HTTPS   ProxyConfig `json:"https"`
}

type ProxyConfig struct {
	Enabled bool   `json:"enabled"`
	Server  string `json:"server"`
	Port    int    `json:"port"`
}

func New(stateDir string, runner Runner) *Manager {
	if runner == nil {
		runner = ExecRunner{}
	}
	return &Manager{stateDir: stateDir, runner: runner}
}

func (m *Manager) Enable(ctx context.Context, service, host string, port int) error {
	if service == "" {
		return fmt.Errorf("service is required")
	}
	if host == "" {
		return fmt.Errorf("host is required")
	}
	if port <= 0 {
		return fmt.Errorf("port must be positive")
	}
	if err := os.MkdirAll(m.stateDir, 0o700); err != nil {
		return fmt.Errorf("create system proxy state dir: %w", err)
	}
	statePath := m.statePath(service)
	if _, err := os.Stat(statePath); os.IsNotExist(err) {
		state, err := m.Read(ctx, service)
		if err != nil {
			return err
		}
		data, err := json.MarshalIndent(state, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal system proxy state: %w", err)
		}
		if err := os.WriteFile(statePath, data, 0o600); err != nil {
			return fmt.Errorf("write system proxy state: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("stat system proxy state: %w", err)
	}

	portString := strconv.Itoa(port)
	if _, err := m.runner.Run(ctx, "networksetup", "-setwebproxy", service, host, portString); err != nil {
		return fmt.Errorf("set web proxy: %w", err)
	}
	if _, err := m.runner.Run(ctx, "networksetup", "-setsecurewebproxy", service, host, portString); err != nil {
		return fmt.Errorf("set secure web proxy: %w", err)
	}
	if _, err := m.runner.Run(ctx, "networksetup", "-setwebproxystate", service, "on"); err != nil {
		return fmt.Errorf("enable web proxy: %w", err)
	}
	if _, err := m.runner.Run(ctx, "networksetup", "-setsecurewebproxystate", service, "on"); err != nil {
		return fmt.Errorf("enable secure web proxy: %w", err)
	}
	return nil
}

func (m *Manager) Disable(ctx context.Context, service string) error {
	if service == "" {
		return fmt.Errorf("service is required")
	}
	data, err := os.ReadFile(m.statePath(service))
	if err != nil {
		return fmt.Errorf("read system proxy state: %w", err)
	}
	var state ProxyState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("decode system proxy state: %w", err)
	}
	if err := m.apply(ctx, state); err != nil {
		return err
	}
	if err := os.Remove(m.statePath(service)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove system proxy state: %w", err)
	}
	return nil
}

func (m *Manager) Read(ctx context.Context, service string) (ProxyState, error) {
	httpConfig, err := m.readOne(ctx, service, "-getwebproxy")
	if err != nil {
		return ProxyState{}, err
	}
	httpsConfig, err := m.readOne(ctx, service, "-getsecurewebproxy")
	if err != nil {
		return ProxyState{}, err
	}
	return ProxyState{Service: service, HTTP: httpConfig, HTTPS: httpsConfig}, nil
}

func (m *Manager) apply(ctx context.Context, state ProxyState) error {
	if state.HTTP.Server != "" && state.HTTP.Port > 0 {
		if _, err := m.runner.Run(ctx, "networksetup", "-setwebproxy", state.Service, state.HTTP.Server, strconv.Itoa(state.HTTP.Port)); err != nil {
			return fmt.Errorf("restore web proxy: %w", err)
		}
	}
	if _, err := m.runner.Run(ctx, "networksetup", "-setwebproxystate", state.Service, onOff(state.HTTP.Enabled)); err != nil {
		return fmt.Errorf("restore web proxy state: %w", err)
	}
	if state.HTTPS.Server != "" && state.HTTPS.Port > 0 {
		if _, err := m.runner.Run(ctx, "networksetup", "-setsecurewebproxy", state.Service, state.HTTPS.Server, strconv.Itoa(state.HTTPS.Port)); err != nil {
			return fmt.Errorf("restore secure web proxy: %w", err)
		}
	}
	if _, err := m.runner.Run(ctx, "networksetup", "-setsecurewebproxystate", state.Service, onOff(state.HTTPS.Enabled)); err != nil {
		return fmt.Errorf("restore secure web proxy state: %w", err)
	}
	return nil
}

func (m *Manager) readOne(ctx context.Context, service, flag string) (ProxyConfig, error) {
	output, err := m.runner.Run(ctx, "networksetup", flag, service)
	if err != nil {
		return ProxyConfig{}, fmt.Errorf("read proxy state: %w", err)
	}
	return parseNetworkSetupProxy(output)
}

func (m *Manager) statePath(service string) string {
	name := strings.NewReplacer("/", "_", " ", "_").Replace(service)
	return filepath.Join(m.stateDir, name+".json")
}

func parseNetworkSetupProxy(output []byte) (ProxyConfig, error) {
	config := ProxyConfig{}
	for _, line := range strings.Split(string(output), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "Enabled":
			config.Enabled = strings.EqualFold(value, "Yes")
		case "Server":
			config.Server = value
		case "Port":
			port, err := strconv.Atoi(value)
			if err == nil {
				config.Port = port
			}
		}
	}
	return config, nil
}

func onOff(enabled bool) string {
	if enabled {
		return "on"
	}
	return "off"
}

package truststore

import (
	"context"
	"fmt"
	"os/exec"
)

type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

type Manager struct {
	runner Runner
}

func New(runner Runner) *Manager {
	if runner == nil {
		runner = ExecRunner{}
	}
	return &Manager{runner: runner}
}

func (m *Manager) TrustRoot(ctx context.Context, certPath string) error {
	if certPath == "" {
		return fmt.Errorf("certificate path is required")
	}
	if _, err := m.runner.Run(ctx, "security", "add-trusted-cert", "-d", "-r", "trustRoot", "-k", "/Library/Keychains/System.keychain", certPath); err != nil {
		return fmt.Errorf("trust root certificate: %w", err)
	}
	return nil
}

func (m *Manager) RevokeRoot(ctx context.Context, certPath string) error {
	if certPath == "" {
		return fmt.Errorf("certificate path is required")
	}
	if _, err := m.runner.Run(ctx, "security", "remove-trusted-cert", "-d", certPath); err != nil {
		return fmt.Errorf("revoke root certificate trust: %w", err)
	}
	return nil
}

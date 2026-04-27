package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/dhiyaan/deconstruct/daemon/internal/auth"
	"github.com/dhiyaan/deconstruct/daemon/internal/blobstore"
	"github.com/dhiyaan/deconstruct/daemon/internal/certs"
	"github.com/dhiyaan/deconstruct/daemon/internal/config"
	"github.com/dhiyaan/deconstruct/daemon/internal/jsonrpc"
	"github.com/dhiyaan/deconstruct/daemon/internal/mcp"
	"github.com/dhiyaan/deconstruct/daemon/internal/proxy"
	"github.com/dhiyaan/deconstruct/daemon/internal/store"
	"github.com/dhiyaan/deconstruct/daemon/internal/systemproxy"
	"github.com/dhiyaan/deconstruct/daemon/internal/truststore"
)

const version = "0.1.0-dev"

func main() {
	if err := run(); err != nil {
		slog.Error("daemon stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	defaultPaths, err := config.DefaultPaths()
	if err != nil {
		return err
	}

	dataDir := flag.String("data-dir", defaultPaths.DataDir, "directory for daemon state")
	socketPath := flag.String("socket", defaultPaths.SocketPath, "Unix domain socket path for JSON-RPC")
	proxyAddr := flag.String("proxy", "127.0.0.1:18080", "explicit HTTP proxy listen address")
	mcpAddr := flag.String("mcp", "127.0.0.1:18081", "local MCP HTTP listen address")
	warmingInterval := flag.Duration("cookie-warming-check", time.Minute, "interval for auth profile cookie-warming checks")
	caDir := flag.String("ca-dir", "", "directory for local proxy CA material")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return nil
	}

	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(*socketPath), 0o700); err != nil {
		return fmt.Errorf("create runtime dir: %w", err)
	}

	db, err := store.Open(filepath.Join(*dataDir, "deconstruct.sqlite3"))
	if err != nil {
		return err
	}
	defer db.Close()

	blobs, err := blobstore.Open(filepath.Join(*dataDir, "blobs"))
	if err != nil {
		return err
	}
	if *caDir == "" {
		*caDir = filepath.Join(*dataDir, "ca")
	}
	authority, err := certs.LoadOrCreate(*caDir)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	proxyServer := proxy.NewServer(db, blobs, authority)
	httpServer := &http.Server{Addr: *proxyAddr, Handler: proxyServer}
	go func() {
		slog.Info("proxy listening", "addr", *proxyAddr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("proxy failed", "error", err)
			stop()
		}
	}()

	rpcServer := jsonrpc.NewServer(jsonrpc.Dependencies{
		Store:       db,
		Blobs:       blobs,
		CA:          authority,
		Proxy:       proxyServer,
		SystemProxy: systemproxy.New(filepath.Join(*dataDir, "system-proxy"), nil),
		TrustStore:  truststore.New(nil),
		Version:     version,
		DataDir:     *dataDir,
		ProxyAddr:   *proxyAddr,
		SocketPath:  *socketPath,
		CAPath:      authority.CertificatePath(),
	})
	listener, err := listenUnix(*socketPath)
	if err != nil {
		return err
	}
	go func() {
		slog.Info("json-rpc listening", "socket", *socketPath)
		if err := rpcServer.Serve(ctx, listener); err != nil {
			slog.Error("json-rpc failed", "error", err)
			stop()
		}
	}()

	mcpServer := &http.Server{Addr: *mcpAddr, Handler: mcp.NewServer(db, blobs)}
	go func() {
		slog.Info("mcp listening", "addr", *mcpAddr)
		if err := mcpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("mcp failed", "error", err)
			stop()
		}
	}()

	vault, err := auth.NewVault(db, *dataDir)
	if err != nil {
		return err
	}
	warmingRunner := auth.NewWarmingRunner(db, blobs, vault)
	go runCookieWarming(ctx, warmingRunner, *warmingInterval)

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), config.ShutdownTimeout)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown proxy: %w", err)
	}
	if err := mcpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown mcp: %w", err)
	}
	return nil
}

func runCookieWarming(ctx context.Context, runner *auth.WarmingRunner, interval time.Duration) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			results, err := runner.RunDue(ctx, now.UTC())
			if err != nil {
				slog.Error("cookie warming check failed", "error", err)
				continue
			}
			for _, result := range results {
				if result.Error != "" {
					slog.Warn("cookie warming profile failed", "profile_id", result.ProfileID, "workflow_id", result.WorkflowID, "error", result.Error)
				} else if result.Ran {
					slog.Info("cookie warming profile ran", "profile_id", result.ProfileID, "workflow_id", result.WorkflowID)
				}
			}
		}
	}
}

func listenUnix(path string) (net.Listener, error) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("remove stale socket: %w", err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen unix socket: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("chmod socket: %w", err)
	}
	return listener, nil
}

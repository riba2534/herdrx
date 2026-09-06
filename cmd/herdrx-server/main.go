package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/riba2534/herdrx/internal/config"
	"github.com/riba2534/herdrx/internal/httpapi"
	"github.com/riba2534/herdrx/internal/secure"
	"github.com/riba2534/herdrx/internal/store"
	"github.com/riba2534/herdrx/internal/webassets"
)

var version = "dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if err := healthcheck(cfg.Addr); err != nil {
			logger.Error("healthcheck failed", "error", err)
			os.Exit(1)
		}
		return
	}
	dataStore, err := store.Open(cfg.DataDir)
	if err != nil {
		logger.Error("open store", "error", err)
		os.Exit(1)
	}
	defer dataStore.Close()
	vault, err := secure.OpenVault(cfg.DataDir, cfg.MasterKey)
	if err != nil {
		logger.Error("open secrets vault", "error", err)
		os.Exit(1)
	}
	httpapi.Version = version
	api, err := httpapi.New(cfg, dataStore, vault, webassets.FS(), logger)
	if err != nil {
		logger.Error("create server", "error", err)
		os.Exit(1)
	}
	defer api.Close()
	server := &http.Server{Addr: cfg.Addr, Handler: api.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 75 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	logger.Info("herdrx listening", "version", version, "addr", cfg.Addr, "public_url", cfg.PublicURL)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func healthcheck(address string) error {
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	client := http.Client{Timeout: 3 * time.Second}
	response, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected health status %d", response.StatusCode)
	}
	return nil
}

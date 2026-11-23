// Command gateway serves a GitHub-compatible API backed by Gitea.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"micro1-interview/internal/gitea"
	"micro1-interview/internal/rest"
)

func main() {
	giteaURL := os.Getenv("GITEA_URL")
	if giteaURL == "" {
		slog.Error("GITEA_URL is required")
		os.Exit(1)
	}
	publicURL := os.Getenv("PUBLIC_URL")
	if publicURL == "" {
		slog.Error("PUBLIC_URL is required")
		os.Exit(1)
	}
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	server := &http.Server{
		Addr:              addr,
		Handler:           rest.NewHandler(gitea.New(giteaURL), publicURL),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Error("shutdown", "error", err)
		}
	}()

	slog.Info("gateway listening", "addr", addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("serve", "error", err)
		os.Exit(1)
	}
}

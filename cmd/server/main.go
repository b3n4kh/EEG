package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ben/eeg-sumsum/internal/auth"
	"github.com/ben/eeg-sumsum/internal/config"
	"github.com/ben/eeg-sumsum/internal/db"
	"github.com/ben/eeg-sumsum/internal/web"
)

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	database, err := db.Open(cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer database.Close()

	authService := auth.Service{DB: database}
	if err := authService.BootstrapAdmin(context.Background(), cfg.AdminUsername, cfg.AdminPassword); err != nil {
		return err
	}
	if err := authService.BootstrapAPIToken(context.Background(), cfg.AdminAPIToken); err != nil {
		return err
	}

	server := web.New(database, cfg.DevMode)
	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           server.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errs := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", cfg.Addr, "database", cfg.DatabasePath)
		errs <- httpServer.ListenAndServe()
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-errs:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	case <-stop:
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpServer.Shutdown(ctx)
	}
}

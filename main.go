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

	"github.com/matthiaslubbertsen/mcp-full-of-mcps/internal/config"
	"github.com/matthiaslubbertsen/mcp-full-of-mcps/internal/crypto"
	"github.com/matthiaslubbertsen/mcp-full-of-mcps/internal/server"
	"github.com/matthiaslubbertsen/mcp-full-of-mcps/internal/store"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	box, err := crypto.NewBox(cfg.SecretKey)
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.DataDir, box)
	if err != nil {
		return err
	}
	defer st.Close()

	srv, err := server.New(cfg, st, box, log)
	if err != nil {
		return err
	}
	if len(cfg.Providers) == 0 {
		log.Warn("no login providers configured: set OIDC_PROVIDERS (see README)")
	}
	if u := srv.SetupURL(); u != "" {
		log.Info("FIRST-TIME SETUP: open this link to create the admin account", "url", u)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			st.Prune(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()

	hs := &http.Server{Addr: cfg.Addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- hs.ListenAndServe() }()
	log.Info("listening", "addr", cfg.Addr, "base_url", cfg.BaseURL)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := hs.Shutdown(sctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

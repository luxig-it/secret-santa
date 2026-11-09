// Command secret-santa serves the Secret Santa web app.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/luxig-it/secret-santa/internal/config"
	"github.com/luxig-it/secret-santa/internal/mail"
	"github.com/luxig-it/secret-santa/internal/store"
	"github.com/luxig-it/secret-santa/internal/web"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if len(os.Args) > 1 && os.Args[1] == "backup" {
		if err := backup(); err != nil {
			log.Error("backup", "err", err)
			os.Exit(1)
		}
		return
	}
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
	if cfg.GeneratedToken != "" {
		// Printed on stdout rather than logged so it does not end up in a log
		// aggregator by accident, and only on the very first glance.
		fmt.Printf("\nNo SECRET_SANTA_ORGANIZER_TOKENS configured. Organizer key for this run:\n\n    %s\n\n", cfg.GeneratedToken)
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           web.New(cfg, st, log, mailer(cfg)),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Addr, "base_url", cfg.BaseURL, "db", cfg.DBPath)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
	}
	return nil
}

// mailer returns the SMTP sender when links may be sent by mail, else nil.
func mailer(cfg config.Config) mail.Sender {
	if !cfg.AllowEmail {
		return nil
	}
	s := cfg.SMTP
	return mail.NewSMTP(s.Host, s.Port, s.User, s.Pass, s.From, cfg.SMTPInsecure)
}

// backup writes a consistent copy of the database to stdout, for
// `docker compose exec -T app /secret-santa backup > file.db` while the
// server keeps running.
func backup() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	return st.Backup(context.Background(), os.Stdout)
}

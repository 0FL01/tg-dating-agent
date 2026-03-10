package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/0FL01/tg-dating-agent/internal/forwarder"
)

type httpServer interface {
	ListenAndServe() error
	Shutdown(ctx context.Context) error
}

type senderFactory func(cfg *forwarder.Config) (forwarder.MessageSender, error)
type serverFactory func(cfg *forwarder.Config, sender forwarder.MessageSender) (httpServer, error)

type appDeps struct {
	loadConfig func() (*forwarder.Config, error)
	newSender  senderFactory
	newServer  serverFactory
}

func defaultDeps() appDeps {
	return appDeps{
		loadConfig: forwarder.Load,
		newSender: func(cfg *forwarder.Config) (forwarder.MessageSender, error) {
			return forwarder.NewTelegramSender(cfg)
		},
		newServer: func(cfg *forwarder.Config, sender forwarder.MessageSender) (httpServer, error) {
			return forwarder.NewHTTPServer(cfg, sender)
		},
	}
}

func run(ctx context.Context, sigCh <-chan os.Signal, deps appDeps) error {
	cfg, err := deps.loadConfig()
	if err != nil {
		return fmt.Errorf("load forwarder config: %w", err)
	}

	sender, err := deps.newSender(cfg)
	if err != nil {
		return fmt.Errorf("build telegram sender: %w", err)
	}

	server, err := deps.newServer(cfg, sender)
	if err != nil {
		return fmt.Errorf("build HTTP server: %w", err)
	}

	serveErrCh := make(chan error, 1)
	go func() {
		err := server.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErrCh <- err
		}
		close(serveErrCh)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.HTTPTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown HTTP server after context cancellation: %w", err)
		}
		if serveErr := <-serveErrCh; serveErr != nil {
			return fmt.Errorf("HTTP server failed while shutting down after context cancellation: %w", serveErr)
		}
		return nil
	case <-sigCh:
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.HTTPTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown HTTP server after signal: %w", err)
		}
		if serveErr := <-serveErrCh; serveErr != nil {
			return fmt.Errorf("HTTP server failed while shutting down after signal: %w", serveErr)
		}
		return nil
	case serveErr := <-serveErrCh:
		if serveErr != nil {
			return fmt.Errorf("serve HTTP: %w", serveErr)
		}
		return nil
	}
}

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	log.Println("Starting match forwarder...")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	ctx := context.Background()
	start := time.Now()
	if err := run(ctx, sigCh, defaultDeps()); err != nil {
		log.Fatalf("Match forwarder failed: %v", err)
	}

	log.Printf("Match forwarder stopped gracefully in %s", time.Since(start).Round(time.Millisecond))
}

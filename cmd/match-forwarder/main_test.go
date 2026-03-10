package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0FL01/tg-dating-agent/internal/forwarder"
)

type fakeServer struct {
	listenStarted chan struct{}
	listenDone    chan struct{}

	mu            sync.Mutex
	shutdownCalls int
	shutdownErr   error
}

func newFakeServer() *fakeServer {
	return &fakeServer{
		listenStarted: make(chan struct{}),
		listenDone:    make(chan struct{}),
	}
}

func (s *fakeServer) ListenAndServe() error {
	close(s.listenStarted)
	<-s.listenDone
	return http.ErrServerClosed
}

func (s *fakeServer) Shutdown(context.Context) error {
	s.mu.Lock()
	s.shutdownCalls++
	err := s.shutdownErr
	s.mu.Unlock()
	close(s.listenDone)
	return err
}

func (s *fakeServer) shutdownCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shutdownCalls
}

type noopSender struct{}

func (noopSender) SendMessage(context.Context, string) error {
	return nil
}

func minimalConfig() *forwarder.Config {
	return &forwarder.Config{
		BotToken:           "123456:token",
		TargetChatID:       1,
		TelegramAPIBaseURL: forwarder.DefaultTelegramAPIBaseURL,
		HTTPTimeout:        250 * time.Millisecond,
		BindAddress:        "127.0.0.1:8081",
		WebhookPath:        forwarder.DefaultWebhookPath,
		WebhookAuthToken:   "token",
	}
}

func TestRun_GracefulShutdownOnSignal(t *testing.T) {
	t.Parallel()

	fakeSrv := newFakeServer()
	sigCh := make(chan os.Signal, 1)
	runDone := make(chan error, 1)

	go func() {
		runDone <- run(context.Background(), sigCh, appDeps{
			loadConfig: func() (*forwarder.Config, error) {
				return minimalConfig(), nil
			},
			newSender: func(*forwarder.Config) (forwarder.MessageSender, error) {
				return noopSender{}, nil
			},
			newServer: func(*forwarder.Config, forwarder.MessageSender) (httpServer, error) {
				return fakeSrv, nil
			},
		})
	}()

	select {
	case <-fakeSrv.listenStarted:
	case <-time.After(time.Second):
		t.Fatal("server did not start")
	}

	sigCh <- os.Interrupt

	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("run() did not return after signal")
	}

	if got := fakeSrv.shutdownCallCount(); got != 1 {
		t.Fatalf("shutdown call count = %d, want 1", got)
	}
}

func TestRun_LoadConfigError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("bad env")
	err := run(context.Background(), make(chan os.Signal), appDeps{
		loadConfig: func() (*forwarder.Config, error) {
			return nil, wantErr
		},
		newSender: func(*forwarder.Config) (forwarder.MessageSender, error) {
			t.Fatal("newSender should not be called")
			return nil, nil
		},
		newServer: func(*forwarder.Config, forwarder.MessageSender) (httpServer, error) {
			t.Fatal("newServer should not be called")
			return nil, nil
		},
	})

	if err == nil {
		t.Fatal("run() error = nil, want error")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("run() error = %v, want wrapped %v", err, wantErr)
	}
	if !strings.Contains(err.Error(), "load forwarder config") {
		t.Fatalf("run() error = %v, want context message", err)
	}
}

func TestRun_ListenAndServeBindError(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	defer func() {
		_ = ln.Close()
	}()

	occupiedAddr := ln.Addr().String()

	err = run(context.Background(), make(chan os.Signal), appDeps{
		loadConfig: func() (*forwarder.Config, error) {
			cfg := minimalConfig()
			cfg.BindAddress = occupiedAddr
			return cfg, nil
		},
		newSender: func(*forwarder.Config) (forwarder.MessageSender, error) {
			return noopSender{}, nil
		},
		newServer: func(cfg *forwarder.Config, _ forwarder.MessageSender) (httpServer, error) {
			return &http.Server{Addr: cfg.BindAddress, Handler: http.NewServeMux()}, nil
		},
	})

	if err == nil {
		t.Fatal("run() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "serve HTTP") {
		t.Fatalf("run() error = %v, want serve context", err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "address already in use") {
		t.Fatalf("run() error = %v, want bind failure", err)
	}
}

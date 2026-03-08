package main

import (
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/0FL01/tg-dating-agent/internal/dating"
	"github.com/0FL01/tg-dating-agent/internal/standalone"
	"github.com/amarnathcjd/gogram/telegram"
)

type shutdownHandler interface {
	Shutdown()
}

type stoppableClient interface {
	Stop() error
}

func runMainLoop(idle func(), sigCh <-chan os.Signal, shutdown func()) {
	idleDone := make(chan struct{})
	go func() {
		defer close(idleDone)
		idle()
	}()

	var shutdownDone <-chan struct{}

	for {
		select {
		case <-sigCh:
			if shutdownDone != nil {
				continue
			}

			done := make(chan struct{})
			shutdownDone = done
			go func(done chan struct{}) {
				defer close(done)
				shutdown()
			}(done)
		case <-idleDone:
			if shutdownDone != nil {
				<-shutdownDone
			}
			return
		}
	}
}

func orchestrateShutdown(handler shutdownHandler, client stoppableClient) {
	shutdownDone := make(chan struct{})

	go func() {
		defer close(shutdownDone)
		handler.Shutdown()
	}()

	if err := client.Stop(); err != nil {
		log.Printf("Failed to stop Telegram client: %v", err)
	}

	<-shutdownDone
}

func shouldHandleOutgoingStop(chatID, datingBotChatID int64, text string, stopped bool) bool {
	if chatID != datingBotChatID {
		return false
	}

	switch strings.TrimSpace(text) {
	case "*stop":
		return true
	case dating.ButtonSleep:
		return !stopped
	default:
		return false
	}
}

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	log.Println("Starting Dating Agent...")

	cfg, err := standalone.Load()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}
	log.Printf("Configuration loaded (session source: %s)", cfg.SessionSource())

	result, err := standalone.EnsureAuthorized(cfg)
	if err != nil {
		log.Fatalf("Failed to bootstrap: %v", err)
	}

	if result.Me != nil {
		log.Printf("Authorized as: %s %s (@%s)", result.Me.FirstName, result.Me.LastName, result.Me.Username)
	} else {
		log.Println("Authorized (user info unavailable)")
	}

	handler := dating.NewStandaloneHandler(cfg, result.Client)
	handler.Start()
	handler.StartWorker()

	result.Client.On("message", func(m *telegram.NewMessage) error {
		if !handler.Filter()(m) {
			return nil
		}
		return handler.Handle(m)
	})

	result.Client.On("message", func(m *telegram.NewMessage) error {
		if shouldHandleOutgoingStop(m.ChatID(), cfg.DatingBotChatID, m.Text(), handler.IsStopped()) {
			handler.Stop()
			_, _ = m.Reply(dating.StatusStopped)
		}
		return nil
	}, telegram.FilterOutgoing)

	result.Client.On("album", func(a *telegram.Album) error {
		if len(a.Messages) == 0 || a.Messages[0].ChatID() != cfg.DatingBotChatID {
			return nil
		}
		return handler.HandleAlbum(a)
	})

	if err := handler.Bootstrap(); err != nil {
		log.Printf("Dating startup bootstrap failed (non-fatal): %v", err)
	}

	log.Printf("Dating Agent ready! Listening for profiles from chat ID: %d", cfg.DatingBotChatID)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	runMainLoop(result.Client.Idle, sigCh, func() {
		log.Println("Received shutdown signal, stopping worker...")
		orchestrateShutdown(handler, result.Client)
		log.Println("Dating Agent stopped gracefully")
	})

	log.Println("Dating Agent stopped")
}

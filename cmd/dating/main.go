package main

import (
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/0FL01/tg-dating-agent/internal/dating"
	"github.com/0FL01/tg-dating-agent/internal/standalone"
	"github.com/amarnathcjd/gogram/telegram"
)

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
		text := strings.TrimSpace(m.Text())
		if text == "*stop" || text == "💤" {
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

	go func() {
		<-sigCh
		log.Println("Received shutdown signal, entering sleep mode...")

		// 1. Отправляем 💤 и переводим в StateStopped
		handler.Stop()

		// 2. Останавливаем worker goroutine
		handler.StopWorker()

		// 3. Даём время на отправку сообщения
		time.Sleep(5 * time.Second)

		log.Println("Dating Agent stopped gracefully")
	}()

	result.Client.Idle()
	log.Println("Dating Agent stopped")
}

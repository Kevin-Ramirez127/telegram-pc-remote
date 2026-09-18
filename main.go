// telegram-pc-remote is a Telegram bot that exposes configurable action
// buttons for remote-controlling the host PC.
//
// Usage:
//
//	TELEGRAM_BOT_TOKEN=... go run .
//
// Security posture (see README.md):
//   - allowlist for users AND chats (fail closed),
//   - at most one message processed per chat per second (extras ignored),
//   - pre-approved commands only, with timeouts and locked-down env,
//   - no secret is ever logged.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"telegram-pc-remote/internal/config"
	"telegram-pc-remote/internal/ratelimit"
	"telegram-pc-remote/internal/store"
	"telegram-pc-remote/internal/whitelist"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	bot, err := tgbotapi.NewBotAPI(cfg.Token)
	if err != nil {
		log.Fatalf("telegram authentication failed: %v", err)
	}
	log.Printf("authenticated as @%s", bot.Self.UserName)

	st, err := store.New(cfg.CommandsFile, cfg.CommandsDir)
	if err != nil {
		log.Fatalf("commands store: %v", err)
	}
	wl, err := whitelist.New(cfg.WhitelistFile)
	if err != nil {
		log.Fatalf("whitelist: %v", err)
	}

	s := &service{
		bot:       bot,
		cfg:       cfg,
		store:     st,
		whitelist: wl,
		limit:     ratelimit.New(),
		sem:       make(chan struct{}, cfg.MaxConcurrent),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)

	go s.refreshLoop(ctx, hup)
	go s.pruneLoop(ctx)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	u.AllowedUpdates = []string{"message", "callback_query"}
	updates := bot.GetUpdatesChan(u)

	log.Printf("started: %d commands, %d whitelisted users, %d whitelisted chats",
		st.Count(), wl.LenUsers(), wl.LenChats())

	for {
		select {
		case <-ctx.Done():
			log.Print("shutting down")
			return
		case upd, ok := <-updates:
			if !ok {
				log.Print("update channel closed")
				return
			}
			s.handle(upd)
		}
	}
}

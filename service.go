package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"telegram-pc-remote/internal/actions"
	"telegram-pc-remote/internal/config"
	"telegram-pc-remote/internal/ratelimit"
	"telegram-pc-remote/internal/store"
	"telegram-pc-remote/internal/whitelist"
)

type service struct {
	bot       *tgbotapi.BotAPI
	cfg       *config.Config
	store     *store.Store
	whitelist *whitelist.Whitelist
	limit     *ratelimit.Limiter
	sem       chan struct{}

	// Stat cache for change detection; used only by refreshLoop.
	whitelistStat os.FileInfo
	commandsStat  os.FileInfo
}

func isBuiltin(text string) bool {
	switch text {
	case "/start", "/menu", "/commands", "/help":
		return true
	}
	return false
}

// handle accepts an update, bounding concurrency so slow shell commands can
// not exhaust memory, and re-arming panics so one bad update cannot crash the
// process (a security-relevant property: the bot stays available).
func (s *service) handle(upd tgbotapi.Update) {
	select {
	case s.sem <- struct{}{}:
	default:
		log.Printf("dropping update: concurrency limit (%d) reached", s.cfg.MaxConcurrent)
		return
	}
	go func() {
		defer func() { <-s.sem }()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("panic while handling update: %v", r)
			}
		}()
		s.process(upd)
	}()
}

func (s *service) process(upd tgbotapi.Update) {
	var userID, chatID int64
	var text string
	var isCallback bool

	switch {
	case upd.Message != nil && upd.Message.From != nil:
		userID, chatID = upd.Message.From.ID, upd.Message.Chat.ID
		text = upd.Message.Text
	case upd.CallbackQuery != nil:
		q := upd.CallbackQuery
		userID = q.From.ID
		if q.Message == nil {
			return // callback from an inline message; nothing to reply to
		}
		chatID = q.Message.Chat.ID
		text = q.Data
		isCallback = true
	default:
		return
	}

	// Gate 1: allowlist. Both the user and the chat must be whitelisted.
	// Unauthorized updates are dropped silently (no reply, no callback ack)
	// so the bot never leaks its existence or its command set.
	if !s.whitelist.IsAllowed(userID, chatID) {
		log.Printf("blocked update from user %d in chat %d (not whitelisted)", userID, chatID)
		return
	}

	// Gate 2: rate limit — at most one update per chat per second, the rest
	// are ignored. Callbacks are still acknowledged so the button spinner
	// clears, but the command itself is treated as dropped.
	if isCallback {
		_, _ = s.bot.Request(tgbotapi.NewCallback(upd.CallbackQuery.ID, ""))
	}
	if !s.limit.Allow(chatID) {
		log.Printf("rate limited: ignored update from chat %d", chatID)
		return
	}

	text = strings.TrimSpace(text)
	if text == "" {
		return
	}

	if isCallback {
		// A button press: the button text must match a stored command.
		s.runCommand(text, chatID)
		return
	}
	if isBuiltin(text) {
		s.showMenu(chatID)
		return
	}
	s.runCommand(text, chatID)
}

// runCommand looks up exact text among the stored commands. If there is no
// match, it sends an error message (the caller never has other command names).
// If there is a match, it executes the registered script and reports the
// result (which may be a photo for --img commands).
func (s *service) runCommand(text string, chatID int64) {
	cmd, ok := s.store.Lookup(text)
	if !ok {
		s.send(chatID, fmt.Sprintf("⚠️ Unknown command: %q. Send /menu to see the available buttons.", text))
		return
	}

	scriptPath, err := s.store.ResolveScript(cmd)
	if err != nil {
		s.send(chatID, fmt.Sprintf("❌ Command %q is misconfigured: %v", cmd.Text, err))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.MaxCommandDuration)
	defer cancel()

	res, err := actions.Execute(ctx, cmd, actions.ExecuteOpts{
		ScriptPath: scriptPath,
		WorkDir:    s.cfg.CommandsDir,
	})
	s.sendResult(chatID, cmd, res, err)
}

func (s *service) sendResult(chatID int64, cmd store.Command, res actions.Result, err error) {
	if err != nil {
		text := fmt.Sprintf("❌ Command %q failed: %v", cmd.Text, err)
		if res.Text != "" {
			text += "\n\n" + res.Text
		}
		s.send(chatID, text)
		return
	}

	if res.Photo != nil {
		photo := tgbotapi.NewPhoto(chatID, tgbotapi.FileBytes{Name: "result", Bytes: res.Photo})
		if res.Text != "" {
			photo.Caption = res.Text
		}
		if _, err := s.bot.Send(photo); err != nil {
			log.Printf("failed to send photo to chat %d: %v", chatID, err)
		}
		return
	}

	if res.Text == "" {
		res.Text = "✅ Done."
	}
	s.send(chatID, res.Text)
}

func (s *service) send(chatID int64, text string) {
	if _, err := s.bot.Send(tgbotapi.NewMessage(chatID, text)); err != nil {
		log.Printf("failed to send to chat %d: %v", chatID, err)
	}
}

// showMenu builds the inline keyboard from the configured commands: each
// button label IS the command text, and its callback data is the same text.
func (s *service) showMenu(chatID int64) {
	cmds := s.store.List()
	if len(cmds) == 0 {
		s.send(chatID, "No commands configured. Add some with scripts/manage_commands.sh")
		return
	}

	rows := make([][]tgbotapi.InlineKeyboardButton, 0, (len(cmds)+1)/2)
	for i := 0; i < len(cmds); i += 2 {
		row := []tgbotapi.InlineKeyboardButton{
			tgbotapi.NewInlineKeyboardButtonData(cmds[i].Text, cmds[i].Text),
		}
		if i+1 < len(cmds) {
			row = append(row, tgbotapi.NewInlineKeyboardButtonData(cmds[i+1].Text, cmds[i+1].Text))
		}
		rows = append(rows, row)
	}

	msg := tgbotapi.NewMessage(chatID, "Pick a command:")
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(rows...)
	if _, err := s.bot.Send(msg); err != nil {
		log.Printf("failed to send menu to chat %d: %v", chatID, err)
	}
}

// refreshLoop reloads commands.json / whitelist.json when they change on disk
// (e.g. edited by scripts/manage_commands.sh) or on SIGHUP.
func (s *service) refreshLoop(ctx context.Context, hup <-chan os.Signal) {
	ticker := time.NewTicker(s.cfg.ReloadEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-hup:
			log.Print("SIGHUP: reloading commands and whitelist")
			s.reloadAll()
		case <-ticker.C:
			s.reloadIfChanged()
		}
	}
}

func (s *service) reloadAll() {
	if err := s.store.Load(); err != nil {
		log.Printf("commands reload failed (keeping previous): %v", err)
	} else {
		log.Printf("commands reloaded: %d commands", s.store.Count())
	}
	if err := s.whitelist.Load(); err != nil {
		log.Printf("whitelist reload failed (keeping previous): %v", err)
	} else {
		log.Printf("whitelist reloaded: %d users, %d chats", s.whitelist.LenUsers(), s.whitelist.LenChats())
	}
}

func (s *service) reloadIfChanged() {
	if changed := fileChanged(s.cfg.WhitelistFile, &s.whitelistStat); changed {
		if err := s.whitelist.Load(); err != nil {
			log.Printf("whitelist reload failed (keeping previous): %v", err)
		} else {
			log.Printf("whitelist reloaded: %d users, %d chats", s.whitelist.LenUsers(), s.whitelist.LenChats())
		}
	}
	if changed := fileChanged(s.cfg.CommandsFile, &s.commandsStat); changed {
		if err := s.store.Load(); err != nil {
			log.Printf("commands reload failed (keeping previous): %v", err)
		} else {
			log.Printf("commands reloaded: %d commands", s.store.Count())
		}
	}
}

func fileChanged(path string, cached *os.FileInfo) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false // file temporarily unavailable: keep previous state
	}
	if *cached != nil && fi.Size() == (*cached).Size() && fi.ModTime().Equal((*cached).ModTime()) {
		return false
	}
	*cached = fi
	return true
}

// pruneLoop periodically drops idle rate-limit buckets.
func (s *service) pruneLoop(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.limit.Prune(30 * time.Minute)
		}
	}
}

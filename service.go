package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"telegram-pc-remote/internal/actions"
	"telegram-pc-remote/internal/config"
	"telegram-pc-remote/internal/ratelimit"
	"telegram-pc-remote/internal/store"
	"telegram-pc-remote/internal/whitelist"
)

// optionData is the callback payload of a menu option button. It is built as
// "m\x1f<menu-id>\x1f<index>" using the unit separator (0x1f) as the field
// delimiter — command texts / menu ids / labels cannot contain control
// characters, so parsing is unambiguous, and menu ids keep it well under
// Telegram's 64-byte callback_data limit.
const (
	optionDataPrefix = "m\x1f"
	optionDataSep    = "\x1f"
)

func encodeOptionData(menuID string, idx int) string {
	return optionDataPrefix + menuID + optionDataSep + strconv.Itoa(idx)
}

func parseOptionData(data string) (menuID string, idx int, ok bool) {
	parts := strings.SplitN(data, optionDataSep, 3)
	if len(parts) != 3 || parts[0] != "m" {
		return "", 0, false
	}
	idx, err := strconv.Atoi(parts[2])
	if err != nil || idx < 0 {
		return "", 0, false
	}
	return parts[1], idx, true
}

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
		if strings.HasPrefix(text, optionDataPrefix) {
			// A menu option button was pressed.
			s.runMenuOption(chatID, text)
			return
		}
		// A main menu button: the button text must match a stored command.
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
// Script commands are executed and their result reported (which may be a
// photo for --img commands); menu commands show their option buttons.
// After a script command finishes, the general menu is re-sent so the user
// is back at the top-level picker.
func (s *service) runCommand(text string, chatID int64) {
	cmd, ok := s.store.Lookup(text)
	if !ok {
		s.send(chatID, fmt.Sprintf("⚠️ Unknown command: %q. Send /menu to see the available buttons.", text))
		return
	}

	if cmd.Menu != nil {
		s.showMenuOptions(chatID, cmd)
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

	// The script finished: re-send the general menu.
	s.showMenu(chatID)
}

// showMenuOptions answers a menu command with its prompt and one button per
// option. Each button's callback encodes the menu id + option index.
func (s *service) showMenuOptions(chatID int64, cmd store.Command) {
	if len(cmd.Menu.Options) == 0 {
		s.send(chatID, fmt.Sprintf("⚠️ Menu %q has no options yet (add them with scripts/manage_commands.sh addopt).", cmd.Text))
		return
	}
	prompt := cmd.Menu.Prompt
	if prompt == "" {
		prompt = "Select an option:"
	}
	prompt = actions.ExpandEscapes(prompt)

	msg := tgbotapi.NewMessage(chatID, prompt)
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(optionRows(cmd.Menu, cmd.Text)...)
	if _, err := s.bot.Send(msg); err != nil {
		log.Printf("failed to send menu options to chat %d: %v", chatID, err)
	}
}

// optionRows lays out a menu's options as up to store.MaxButtonsPerRow
// buttons per keyboard row.
func optionRows(m *store.Menu, text string) [][]tgbotapi.InlineKeyboardButton {
	rows := make([][]tgbotapi.InlineKeyboardButton, 0, (len(m.Options)+store.MaxButtonsPerRow-1)/store.MaxButtonsPerRow)
	for i := 0; i < len(m.Options); i += store.MaxButtonsPerRow {
		end := i + store.MaxButtonsPerRow
		if end > len(m.Options) {
			end = len(m.Options)
		}
		row := make([]tgbotapi.InlineKeyboardButton, 0, end-i)
		for j := i; j < end; j++ {
			row = append(row, tgbotapi.NewInlineKeyboardButtonData(m.Options[j].Label, encodeOptionData(m.ID, j)))
		}
		rows = append(rows, row)
	}
	return rows
}

// runMenuOption handles a pressed option button.
//
//   - A navigation option (menu_id) opens the referenced menu: its options
//     are shown and nothing is re-sent — the flow descends until a final
//     (leaf) script runs.
//   - A script option runs its own script (or the menu's shared handler),
//     passing the option's value as $1 / TPR_OPTION. Once its reply is out,
//     the GENERAL menu (/menu: every non-hidden command) is re-sent so the
//     user is back at the top-level picker.
func (s *service) runMenuOption(chatID int64, data string) {
	menuID, idx, ok := parseOptionData(data)
	if !ok {
		s.staleButton(chatID)
		return
	}
	cmd, ok := s.store.LookupMenu(menuID)
	if !ok || cmd.Menu == nil || idx >= len(cmd.Menu.Options) {
		s.staleButton(chatID)
		return
	}

	opt := cmd.Menu.Options[idx]

	// Navigation: this option opens another (custom) menu.
	if opt.MenuID != "" {
		target, ok := s.store.LookupMenu(opt.MenuID)
		if !ok {
			s.send(chatID, fmt.Sprintf("❌ Option %q is misconfigured: menu %q not found.", opt.Label, opt.MenuID))
			return
		}
		s.showMenuOptions(chatID, target)
		return
	}

	scriptRel, value := resolveOption(cmd.Menu, opt)
	scriptPath, err := s.store.ResolveScriptPath(scriptRel)
	if err != nil {
		s.send(chatID, fmt.Sprintf("❌ Option %q is misconfigured: %v", opt.Label, err))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.MaxCommandDuration)
	defer cancel()

	res, err := actions.Execute(ctx, cmd, actions.ExecuteOpts{
		ScriptPath:  scriptPath,
		WorkDir:     s.cfg.CommandsDir,
		Option:      value,
		OptionLabel: opt.Label,
	})
	msg := cmd
	msg.Text = opt.Label // report failures against the pressed option
	s.sendResult(chatID, msg, res, err)

	// The final script ended: re-send the general menu.
	s.showMenu(chatID)
}

// resolveOption determines which script an option runs (its own, or the
// menu's shared handler — non-empty by store validation) and the value
// passed to it as $1 / TPR_OPTION (the option's value, or its label).
func resolveOption(menu *store.Menu, opt store.MenuOption) (scriptRel, value string) {
	scriptRel = opt.Script
	if scriptRel == "" {
		scriptRel = menu.Script
	}
	value = opt.Value
	if value == "" {
		value = opt.Label
	}
	return scriptRel, value
}

func (s *service) staleButton(chatID int64) {
	s.send(chatID, "⚠️ This button is outdated. Send /menu to refresh.")
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
// Hidden commands (submenu targets) are excluded.
func (s *service) showMenu(chatID int64) {
	visible := make([]store.Command, 0)
	for _, c := range s.store.List() {
		if !c.Hidden {
			visible = append(visible, c)
		}
	}
	if len(visible) == 0 {
		s.send(chatID, "No commands configured. Add some with scripts/manage_commands.sh")
		return
	}

	rows := make([][]tgbotapi.InlineKeyboardButton, 0, (len(visible)+1)/2)
	for i := 0; i < len(visible); i += 2 {
		row := []tgbotapi.InlineKeyboardButton{
			tgbotapi.NewInlineKeyboardButtonData(visible[i].Text, visible[i].Text),
		}
		if i+1 < len(visible) {
			row = append(row, tgbotapi.NewInlineKeyboardButtonData(visible[i+1].Text, visible[i+1].Text))
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

package relay

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"rwgin/internal/i18n"
	"rwgin/internal/protocol"
)

// CommandContext provides information about the command being executed.
type CommandContext struct {
	Room    *Room
	Sender  Identity
	Command string
	Args    []string
}

// Reply sends a system message back to the sender.
func (c *CommandContext) Reply(message string) {
	if s, ok := c.Sender.(*Session); ok {
		_ = s.systemMessage(message)
	}
}

// T returns a translated message for the sender's locale.
func (c *CommandContext) T(key string, args ...any) string {
	if s, ok := c.Sender.(*Session); ok {
		return s.T(key, args...)
	}
	return i18n.T(i18n.LangEN, key, args...)
}

// CommandHandler is a function that handles a specific command.
type CommandHandler func(ctx *CommandContext)

type commandEntry struct {
	handler     CommandHandler
	description string // stores the i18n key
	adminOnly   bool
}

// CommandRegistry manages the available commands.
type CommandRegistry struct {
	commands map[string]*commandEntry
	prefix   string
}

// NewCommandRegistry creates a new registry with the given prefix.
func NewCommandRegistry(prefix string) *CommandRegistry {
	return &CommandRegistry{
		commands: make(map[string]*commandEntry),
		prefix:   prefix,
	}
}

// Register adds a new command to the registry.
func (r *CommandRegistry) Register(name, description string, adminOnly bool, handler CommandHandler) {
	r.commands[strings.ToLower(name)] = &commandEntry{
		handler:     handler,
		description: description,
		adminOnly:   adminOnly,
	}
}

// Handle processes a message and executes the command if it matches the prefix.
func (r *CommandRegistry) Handle(room *Room, sender Identity, message string) bool {
	if !strings.HasPrefix(message, r.prefix) {
		return false
	}

	fullCmd := strings.TrimPrefix(message, r.prefix)
	parts := strings.Fields(fullCmd)
	if len(parts) == 0 {
		return false
	}

	cmdName := strings.ToLower(parts[0])
	args := parts[1:]

	entry, ok := r.commands[cmdName]
	if !ok {
		return false
	}

	// Check admin permission
	if entry.adminOnly {
		if s, ok := sender.(*Session); ok {
			if !room.IsAdmin(s) {
				if err := s.systemMessage(s.T("msg.admin_required")); err != nil {
					return true
				}
				return true
			}
		}
	}

	ctx := &CommandContext{
		Room:    room,
		Sender:  sender,
		Command: cmdName,
		Args:    args,
	}

	entry.handler(ctx)
	return true
}

// PlayerHelpText returns a help string listing only commands available to non-admin players.
func (r *CommandRegistry) PlayerHelpText(locale string) string {
	var lines []string
	for name, entry := range r.commands {
		if entry.adminOnly {
			continue
		}
		lines = append(lines, fmt.Sprintf("/%s - %s", name, i18n.T(locale, entry.description)))
	}
	return i18n.T(locale, "available_commands") + "\n" + strings.Join(lines, "\n")
}

// DefaultRegistry returns a registry with all built-in commands.
func DefaultRegistry() *CommandRegistry {
	reg := NewCommandRegistry("/")

	// --- General commands ---

	reg.Register("help", "cmd.help.desc", false, func(ctx *CommandContext) {
		var lines []string
		locale := i18n.LangEN
		if s, ok := ctx.Sender.(*Session); ok {
			locale = s.Locale()
		}
		for name, entry := range reg.commands {
			adminTag := ""
			if entry.adminOnly {
				adminTag = " " + i18n.T(locale, "admin_tag")
			}
			lines = append(lines, fmt.Sprintf("/%s - %s%s", name, i18n.T(locale, entry.description), adminTag))
		}
		// Sort for consistent output
		joined := strings.Join(lines, "\n")
		if s, ok := ctx.Sender.(*Session); ok {
			_ = s.systemMessage(joined)
		}
	})

	reg.Register("ping", "cmd.ping.desc", false, func(ctx *CommandContext) {
		ctx.Reply("Pong!")
	})

	reg.Register("list", "cmd.list.desc", false, func(ctx *CommandContext) {
		locale := i18n.LangEN
		if s, ok := ctx.Sender.(*Session); ok {
			locale = s.Locale()
		}
		ctx.Reply(ctx.Room.MembersText(locale))
	})

	reg.Register("info", "cmd.info.desc", false, func(ctx *CommandContext) {
		r := ctx.Room
		r.mu.RLock()
		detail := r.Summary()
		credits := r.Credits
		income := r.Income
		maxUnits := r.MaxUnits
		fog := r.FogOfWar
		nuke := r.DisableNuke
		shared := r.SharedControl
		paused := r.paused
		r.mu.RUnlock()

		locale := i18n.LangEN
		if s, ok := ctx.Sender.(*Session); ok {
			locale = s.Locale()
		}

		status := i18n.T(locale, "status.waiting")
		if detail.InGame {
			if paused {
				status = i18n.T(locale, "status.ingame_paused")
			} else {
				status = i18n.T(locale, "status.ingame")
			}
		}
		fogText := fogTypeText(locale, fog)
		info := fmt.Sprintf(i18n.T(locale, "info.title"),
			detail.Title, detail.MapName, detail.Players, detail.MaxPlayers,
			status, credits, income, maxUnits, fogText,
			boolText(locale, !nuke), boolText(locale, shared))
		ctx.Reply(info)
	})

	reg.Register("maps", "cmd.maps.desc", false, func(ctx *CommandContext) {
		maps := ctx.Room.commands // access through room to get manager
		_ = maps                  // we need the manager
		// Get maps from the room's manager via the session
		if s, ok := ctx.Sender.(*Session); ok {
			mapsList := s.server.rooms.ListMaps()
			if len(mapsList) == 0 {
				ctx.Reply(ctx.T("msg.no_maps"))
				return
			}
			var sb strings.Builder
			sb.WriteString(ctx.T("msg.available_maps") + "\n")
			for i, m := range mapsList {
				sb.WriteString(fmt.Sprintf("[%d] %s\n", i, m))
			}
			ctx.Reply(sb.String())
		}
	})

	// --- Admin game config commands ---

	reg.Register("map", "cmd.map.desc", true, func(ctx *CommandContext) {
		if len(ctx.Args) == 0 {
			ctx.Reply(ctx.T("msg.usage.map"))
			return
		}
		if s, ok := ctx.Sender.(*Session); ok {
			mapArg := strings.Join(ctx.Args, " ")
			mapsList := s.server.rooms.ListMaps()

			var mapName string
			// Try as index first
			if idx, err := strconv.Atoi(mapArg); err == nil && idx >= 0 && idx < len(mapsList) {
				mapName = mapsList[idx]
			} else {
				// Try as name (with or without .tmx suffix)
				mapName = mapArg
				if !strings.HasSuffix(mapName, ".tmx") {
					mapName = mapName + ".tmx"
				}
				found := false
				for _, m := range mapsList {
					if strings.EqualFold(m, mapName) {
						mapName = m
						found = true
						break
					}
				}
				if !found {
					ctx.Reply(ctx.T("msg.map_not_found", mapArg))
					return
				}
			}

			ctx.Room.mu.Lock()
			ctx.Room.MapName = mapName
			ctx.Room.mu.Unlock()

			ctx.Room.broadcastSystemKey("msg.map_changed", mapName)
			ctx.Room.broadcastServerInfo(s.server)
		}
	})

	reg.Register("credits", "cmd.credits.desc", true, func(ctx *CommandContext) {
		if len(ctx.Args) == 0 {
			ctx.Reply(ctx.T("msg.usage.credits"))
			return
		}
		val, err := strconv.Atoi(ctx.Args[0])
		if err != nil || val < 0 {
			ctx.Reply(ctx.T("msg.invalid_nonneg_int"))
			return
		}
		ctx.Room.mu.Lock()
		ctx.Room.Credits = int32(val)
		ctx.Room.mu.Unlock()

		ctx.Room.broadcastSystemKey("msg.credits_set", val)
		if s, ok := ctx.Sender.(*Session); ok {
			ctx.Room.broadcastServerInfo(s.server)
		}
	})

	reg.Register("income", "cmd.income.desc", true, func(ctx *CommandContext) {
		if len(ctx.Args) == 0 {
			ctx.Reply(ctx.T("msg.usage.income"))
			return
		}
		val, err := strconv.ParseFloat(ctx.Args[0], 32)
		if err != nil || val < 0 || val > 100 {
			ctx.Reply(ctx.T("msg.invalid_0_100"))
			return
		}
		ctx.Room.mu.Lock()
		ctx.Room.Income = float32(val)
		ctx.Room.mu.Unlock()

		ctx.Room.broadcastSystemKey("msg.income_set", val)
		if s, ok := ctx.Sender.(*Session); ok {
			ctx.Room.broadcastServerInfo(s.server)
		}
	})

	reg.Register("fog", "cmd.fog.desc", true, func(ctx *CommandContext) {
		if len(ctx.Args) == 0 {
			ctx.Reply(ctx.T("msg.usage.fog"))
			return
		}
		var fogType int32
		switch strings.ToLower(ctx.Args[0]) {
		case "off", "0":
			fogType = 0
		case "basic", "1":
			fogType = 1
		case "los", "sight", "2":
			fogType = 2
		default:
			ctx.Reply(ctx.T("msg.invalid_fog"))
			return
		}
		ctx.Room.mu.Lock()
		ctx.Room.FogOfWar = fogType
		ctx.Room.mu.Unlock()

		locale := i18n.LangEN
		if s, ok := ctx.Sender.(*Session); ok {
			locale = s.Locale()
		}
		ctx.Room.broadcastSystemKey("msg.fog_set", fogTypeText(locale, fogType))
		if s, ok := ctx.Sender.(*Session); ok {
			ctx.Room.broadcastServerInfo(s.server)
		}
	})

	reg.Register("nukes", "cmd.nukes.desc", true, func(ctx *CommandContext) {
		if len(ctx.Args) == 0 {
			ctx.Reply(ctx.T("msg.usage.nukes"))
			return
		}
		val, err := parseBool(ctx.Args[0])
		if err != nil {
			ctx.Reply(ctx.T("msg.invalid_bool"))
			return
		}
		ctx.Room.mu.Lock()
		ctx.Room.DisableNuke = !val
		ctx.Room.mu.Unlock()

		locale := i18n.LangEN
		if s, ok := ctx.Sender.(*Session); ok {
			locale = s.Locale()
		}
		ctx.Room.broadcastSystemKey("msg.nukes_set", boolText(locale, val))
		if s, ok := ctx.Sender.(*Session); ok {
			ctx.Room.broadcastServerInfo(s.server)
		}
	})

	reg.Register("startingunits", "cmd.startingunits.desc", true, func(ctx *CommandContext) {
		if len(ctx.Args) == 0 {
			ctx.Reply(ctx.T("msg.usage.startingunits"))
			return
		}
		val, err := strconv.Atoi(ctx.Args[0])
		if err != nil || val < 0 {
			ctx.Reply(ctx.T("msg.invalid_nonneg_int"))
			return
		}
		ctx.Room.mu.Lock()
		ctx.Room.StartingUnits = int32(val)
		ctx.Room.mu.Unlock()

		ctx.Room.broadcastSystemKey("msg.startingunits_set", val)
		if s, ok := ctx.Sender.(*Session); ok {
			ctx.Room.broadcastServerInfo(s.server)
		}
	})

	reg.Register("maxunits", "cmd.maxunits.desc", true, func(ctx *CommandContext) {
		if len(ctx.Args) == 0 {
			ctx.Reply(ctx.T("msg.usage.maxunits"))
			return
		}
		val, err := strconv.Atoi(ctx.Args[0])
		if err != nil || val < 1 {
			ctx.Reply(ctx.T("msg.invalid_pos_int"))
			return
		}
		ctx.Room.mu.Lock()
		ctx.Room.MaxUnits = val
		ctx.Room.mu.Unlock()

		ctx.Room.broadcastSystemKey("msg.maxunits_set", val)
		ctx.Room.broadcastTeamList()
		if s, ok := ctx.Sender.(*Session); ok {
			ctx.Room.broadcastServerInfo(s.server)
		}
	})

	reg.Register("share", "cmd.share.desc", true, func(ctx *CommandContext) {
		if len(ctx.Args) == 0 {
			ctx.Reply(ctx.T("msg.usage.share"))
			return
		}
		val, err := parseBool(ctx.Args[0])
		if err != nil {
			ctx.Reply(ctx.T("msg.invalid_bool"))
			return
		}
		ctx.Room.mu.Lock()
		ctx.Room.SharedControl = val
		ctx.Room.mu.Unlock()

		locale := i18n.LangEN
		if s, ok := ctx.Sender.(*Session); ok {
			locale = s.Locale()
		}
		ctx.Room.broadcastSystemKey("msg.share_set", boolText(locale, val))
		if s, ok := ctx.Sender.(*Session); ok {
			ctx.Room.broadcastServerInfo(s.server)
		}
	})

	// --- Team/slot management ---

	reg.Register("team", "cmd.team.desc", true, func(ctx *CommandContext) {
		if len(ctx.Args) < 2 {
			ctx.Reply(ctx.T("msg.usage.team"))
			return
		}
		target, err := ctx.Room.FindTarget(ctx.Args[0])
		if err != nil {
			ctx.Reply(ctx.T("msg.player_not_found"))
			return
		}
		teamNum, err := strconv.Atoi(ctx.Args[1])
		if err != nil {
			ctx.Reply(ctx.T("msg.invalid_team_num"))
			return
		}
		ctx.Room.mu.Lock()
		target.team = int32(teamNum)
		ctx.Room.mu.Unlock()

		ctx.Room.broadcastSystemKey("msg.team_set", target.Name(), teamNum)
		_ = ctx.Room.broadcastTeamList()
	})

	reg.Register("self_team", "cmd.self_team.desc", false, func(ctx *CommandContext) {
		if len(ctx.Args) == 0 {
			ctx.Reply(ctx.T("msg.usage.self_team"))
			return
		}
		teamNum, err := strconv.Atoi(ctx.Args[0])
		if err != nil {
			ctx.Reply(ctx.T("msg.invalid_team_num"))
			return
		}
		if s, ok := ctx.Sender.(*Session); ok {
			ctx.Room.mu.Lock()
			s.team = int32(teamNum)
			ctx.Room.mu.Unlock()

			ctx.Room.broadcastSystemKey("msg.team_set", s.Name(), teamNum)
			_ = ctx.Room.broadcastTeamList()
		}
	})

	reg.Register("move", "cmd.move.desc", true, func(ctx *CommandContext) {
		if len(ctx.Args) < 2 {
			ctx.Reply(ctx.T("msg.usage.move"))
			return
		}
		target, err := ctx.Room.FindTarget(ctx.Args[0])
		if err != nil {
			ctx.Reply(ctx.T("msg.player_not_found"))
			return
		}
		newSlot, err := strconv.Atoi(ctx.Args[1])
		if err != nil || newSlot < 0 || newSlot >= ctx.Room.MaxPlayers {
			ctx.Reply(ctx.T("msg.invalid_slot", ctx.Room.MaxPlayers-1))
			return
		}

		ctx.Room.mu.Lock()
		defer ctx.Room.mu.Unlock()

		// Find current slot of target
		oldSlot := -1
		for i, p := range ctx.Room.players {
			if p == target {
				oldSlot = i
				break
			}
		}
		if oldSlot == -1 {
			ctx.Reply(ctx.T("msg.player_not_found"))
			return
		}
		if oldSlot == newSlot {
			ctx.Reply(ctx.T("msg.already_in_slot"))
			return
		}

		// Swap with occupant if any
		occupant := ctx.Room.players[newSlot]
		ctx.Room.players[oldSlot] = occupant
		ctx.Room.players[newSlot] = target
		if occupant != nil {
			occupant.setRoom(ctx.Room, oldSlot)
		}
		target.setRoom(ctx.Room, newSlot)

		_ = ctx.Room.broadcastTeamList()
		ctx.Room.broadcastSystemKey("msg.player_moved", target.Name(), newSlot)
	})

	reg.Register("self_move", "cmd.self_move.desc", false, func(ctx *CommandContext) {
		if len(ctx.Args) == 0 {
			ctx.Reply(ctx.T("msg.usage.self_move"))
			return
		}
		newSlot, err := strconv.Atoi(ctx.Args[0])
		if err != nil || newSlot < 0 || newSlot >= ctx.Room.MaxPlayers {
			ctx.Reply(ctx.T("msg.invalid_slot", ctx.Room.MaxPlayers-1))
			return
		}

		if s, ok := ctx.Sender.(*Session); ok {
			ctx.Room.mu.Lock()
			defer ctx.Room.mu.Unlock()

			oldSlot := -1
			for i, p := range ctx.Room.players {
				if p == s {
					oldSlot = i
					break
				}
			}
			if oldSlot == -1 {
				ctx.Reply(ctx.T("msg.self_not_found"))
				return
			}
			if oldSlot == newSlot {
				ctx.Reply(ctx.T("msg.already_in_slot_you"))
				return
			}

			occupant := ctx.Room.players[newSlot]
			ctx.Room.players[oldSlot] = occupant
			ctx.Room.players[newSlot] = s
			if occupant != nil {
				occupant.setRoom(ctx.Room, oldSlot)
			}
			s.setRoom(ctx.Room, newSlot)

			_ = ctx.Room.broadcastTeamList()
			ctx.Room.broadcastSystemKey("msg.player_moved", s.Name(), newSlot)
		}
	})

	// --- Game control ---

	reg.Register("kick", "cmd.kick.desc", true, func(ctx *CommandContext) {
		if len(ctx.Args) == 0 {
			ctx.Reply(ctx.T("msg.usage.kick"))
			return
		}
		target, err := ctx.Room.FindTarget(ctx.Args[0])
		if err != nil {
			ctx.Reply(ctx.T("msg.player_not_found"))
			return
		}
		if s, ok := ctx.Sender.(*Session); ok {
			err = ctx.Room.Kick(s, target)
			if err != nil {
				ctx.Reply(ctx.T("msg.kick_failed", err.Error()))
			}
		}
	})

	reg.Register("transfer", "cmd.transfer.desc", false, func(ctx *CommandContext) {
		if len(ctx.Args) == 0 {
			ctx.Reply(ctx.T("msg.usage.transfer"))
			return
		}
		target, err := ctx.Room.FindTarget(ctx.Args[0])
		if err != nil {
			ctx.Reply(ctx.T("msg.player_not_found"))
			return
		}
		if s, ok := ctx.Sender.(*Session); ok {
			err = ctx.Room.TransferOwnership(s, target)
			if err != nil {
				ctx.Reply(ctx.T("msg.transfer_failed", err.Error()))
			} else {
				ctx.Room.broadcastSystemKey("msg.transfer_success", target.Name())
			}
		}
	})

	reg.Register("start", "cmd.start.desc", false, func(ctx *CommandContext) {
		if ctx.Room.IsInGame() {
			ctx.Reply(ctx.T("msg.game_already_running"))
			return
		}

		s, ok := ctx.Sender.(*Session)
		if !ok {
			return
		}

		// Admin can force start
		if ctx.Room.IsAdmin(s) {
			ctx.Room.broadcastSystemKey("msg.admin_started", s.Name())
			_ = s.handleStartGame(protocol.Packet{})
			return
		}

		// Non-admin: vote
		votes := ctx.Room.AddStartVote(s.playerID)
		totalHumans := 0
		for _, p := range ctx.Room.SnapshotPlayers() {
			if p != nil {
				totalHumans++
			}
		}

		required := (totalHumans + 1) / 2
		if votes >= required {
			ctx.Room.broadcastSystemKey("msg.start_vote_passed", votes, required)
			_ = s.handleStartGame(protocol.Packet{})
		} else {
			ctx.Room.broadcastSystemKey("msg.start_vote_cast", s.Name(), votes, required)
		}
	})

	reg.Register("return", "cmd.return.desc", true, func(ctx *CommandContext) {
		if !ctx.Room.IsInGame() {
			ctx.Reply(ctx.T("msg.not_in_game"))
			return
		}
		ctx.Room.SetInGame(false)
		ctx.Room.broadcast(protocol.EncodeReturnToBattleroom())
		ctx.Room.broadcastSystemKey("msg.return_by_admin")
		_ = ctx.Room.broadcastTeamList()
	})

	reg.Register("admin", "cmd.admin.desc", false, func(ctx *CommandContext) {
		if len(ctx.Args) < 2 {
			ctx.Reply(ctx.T("msg.usage.admin"))
			return
		}
		action := strings.ToLower(ctx.Args[0])
		target, err := ctx.Room.FindTarget(ctx.Args[1])
		if err != nil {
			ctx.Reply(ctx.T("msg.player_not_found"))
			return
		}
		if s, ok := ctx.Sender.(*Session); ok {
			switch action {
			case "add":
				err = ctx.Room.SetAdmin(s, target, true)
				if err != nil {
					ctx.Reply(ctx.T("msg.action_failed", err.Error()))
				} else {
					ctx.Room.broadcastSystemKey("msg.admin_added", target.Name())
				}
			case "remove":
				err = ctx.Room.SetAdmin(s, target, false)
				if err != nil {
					ctx.Reply(ctx.T("msg.action_failed", err.Error()))
				} else {
					ctx.Room.broadcastSystemKey("msg.admin_removed", target.Name())
				}
			default:
				ctx.Reply(ctx.T("msg.usage.admin"))
			}
		}
	})

	reg.Register("mute", "cmd.mute.desc", true, func(ctx *CommandContext) {
		if len(ctx.Args) == 0 {
			ctx.Reply(ctx.T("msg.usage.mute"))
			return
		}
		target, err := ctx.Room.FindTarget(ctx.Args[0])
		if err != nil {
			ctx.Reply(ctx.T("msg.player_not_found"))
			return
		}
		if s, ok := ctx.Sender.(*Session); ok {
			err = ctx.Room.Mute(s, target)
			if err != nil {
				ctx.Reply(ctx.T("msg.mute_failed", err.Error()))
			} else {
				ctx.Room.broadcastSystemKey("msg.muted", target.Name())
			}
		}
	})

	reg.Register("unmute", "cmd.unmute.desc", true, func(ctx *CommandContext) {
		if len(ctx.Args) == 0 {
			ctx.Reply(ctx.T("msg.usage.unmute"))
			return
		}
		target, err := ctx.Room.FindTarget(ctx.Args[0])
		if err != nil {
			ctx.Reply(ctx.T("msg.player_not_found"))
			return
		}
		if s, ok := ctx.Sender.(*Session); ok {
			err = ctx.Room.Unmute(s, target)
			if err != nil {
				ctx.Reply(ctx.T("msg.unmute_failed", err.Error()))
			} else {
				ctx.Room.broadcastSystemKey("msg.unmuted", target.Name())
			}
		}
	})

	reg.Register("pause", "cmd.pause.desc", true, func(ctx *CommandContext) {
		if !ctx.Room.IsInGame() {
			ctx.Reply(ctx.T("msg.not_in_game"))
			return
		}
		if ctx.Room.IsPaused() {
			ctx.Reply(ctx.T("msg.already_paused"))
			return
		}
		ctx.Room.SetPaused(true)
		ctx.Room.broadcastSystemKey("msg.game_paused", ctx.Sender.Name())
	})

	reg.Register("unpause", "cmd.unpause.desc", true, func(ctx *CommandContext) {
		if !ctx.Room.IsInGame() {
			ctx.Reply(ctx.T("msg.not_in_game"))
			return
		}
		if !ctx.Room.IsPaused() {
			ctx.Reply(ctx.T("msg.not_paused"))
			return
		}
		ctx.Room.SetPaused(false)
		ctx.Room.broadcastSystemKey("msg.game_resumed", ctx.Sender.Name())
	})

	// --- Vote commands ---

	reg.Register("sync", "cmd.sync.desc", false, func(ctx *CommandContext) {
		if !ctx.Room.IsInGame() {
			ctx.Reply(ctx.T("msg.not_in_game"))
			return
		}

		s, ok := ctx.Sender.(*Session)
		if !ok {
			return
		}

		// Admin can force sync
		if ctx.Room.IsAdmin(s) {
			ctx.Room.broadcastSystemKey("msg.admin_sync", s.Name())
			go ctx.Room.TriggerSync(s.server)
			return
		}

		// Check if there's already an active sync vote
		if vote := ctx.Room.GetActiveVote(); vote != nil && vote.Type == "sync" {
			// Cast vote
			votes, ok := ctx.Room.CastVote("sync", s.playerID)
			if !ok {
				ctx.Reply(ctx.T("msg.vote_expired"))
				return
			}
			totalHumans := ctx.Room.HumanCountLocked()
			required := (totalHumans + 1) / 2
			if votes >= required {
				ctx.Room.ClearVote()
				ctx.Room.broadcastSystemKey("msg.sync_vote_passed", votes, required)
				go ctx.Room.TriggerSync(s.server)
			} else {
				ctx.Room.broadcastSystemKey("msg.sync_vote_cast", s.Name(), votes, required)
			}
			return
		}

		// Start new vote
		ctx.Room.StartVote("sync", s.playerID)
		totalHumans := ctx.Room.HumanCountLocked()
		required := (totalHumans + 1) / 2
		if 1 >= required {
			ctx.Room.ClearVote()
			ctx.Room.broadcastSystemKey("msg.sync_vote_single")
			go ctx.Room.TriggerSync(s.server)
		} else {
			ctx.Room.broadcastSystemKey("msg.sync_vote_started", s.Name(), required)
		}
	})

	reg.Register("gameover", "cmd.gameover.desc", false, func(ctx *CommandContext) {
		if !ctx.Room.IsInGame() {
			ctx.Reply(ctx.T("msg.not_in_game"))
			return
		}

		s, ok := ctx.Sender.(*Session)
		if !ok {
			return
		}

		// Admin can force gameover
		if ctx.Room.IsAdmin(s) {
			ctx.Room.SetInGame(false)
			ctx.Room.broadcast(protocol.EncodeReturnToBattleroom())
			ctx.Room.broadcastSystemKey("msg.admin_gameover", s.Name())
			_ = ctx.Room.broadcastTeamList()
			return
		}

		// Check if there's already an active gameover vote
		if vote := ctx.Room.GetActiveVote(); vote != nil && vote.Type == "gameover" {
			// Cast vote
			votes, ok := ctx.Room.CastVote("gameover", s.playerID)
			if !ok {
				ctx.Reply(ctx.T("msg.vote_expired"))
				return
			}
			totalHumans := ctx.Room.HumanCountLocked()
			required := (totalHumans + 1) / 2
			if votes >= required {
				ctx.Room.ClearVote()
				ctx.Room.SetInGame(false)
				ctx.Room.broadcast(protocol.EncodeReturnToBattleroom())
				ctx.Room.broadcastSystemKey("msg.gameover_vote_passed", votes, required)
				_ = ctx.Room.broadcastTeamList()
			} else {
				ctx.Room.broadcastSystemKey("msg.gameover_vote_cast", s.Name(), votes, required)
			}
			return
		}

		// Start new vote
		ctx.Room.StartVote("gameover", s.playerID)
		totalHumans := ctx.Room.HumanCountLocked()
		required := (totalHumans + 1) / 2
		if 1 >= required {
			ctx.Room.ClearVote()
			ctx.Room.SetInGame(false)
			ctx.Room.broadcast(protocol.EncodeReturnToBattleroom())
			ctx.Room.broadcastSystemKey("msg.gameover_vote_single")
			_ = ctx.Room.broadcastTeamList()
		} else {
			ctx.Room.broadcastSystemKey("msg.gameover_vote_started", s.Name(), required)
		}
	})

	// --- Ban commands ---

	reg.Register("ban", "cmd.ban.desc", true, func(ctx *CommandContext) {
		if len(ctx.Args) == 0 {
			ctx.Reply(ctx.T("msg.usage.ban"))
			return
		}
		target, err := ctx.Room.FindTarget(ctx.Args[0])
		if err != nil {
			ctx.Reply(ctx.T("msg.player_not_found"))
			return
		}
		if s, ok := ctx.Sender.(*Session); ok {
			if !ctx.Room.IsAdmin(s) {
				ctx.Reply(ctx.T("msg.admin_required"))
				return
			}
			ctx.Room.BanPlayer(target.playerID)
			ctx.Room.broadcastSystemKey("msg.banned", target.Name())
			// Kick the banned player
			_ = target.Send(protocol.EncodeKick(target.T("msg.banned", target.Name())))
			target.close()
		}
	})

	reg.Register("unban", "cmd.unban.desc", true, func(ctx *CommandContext) {
		if len(ctx.Args) == 0 {
			ctx.Reply(ctx.T("msg.usage.unban"))
			return
		}
		playerID := ctx.Args[0]
		if !ctx.Room.IsBanned(playerID) {
			ctx.Reply(ctx.T("msg.not_banned"))
			return
		}
		ctx.Room.UnbanPlayer(playerID)
		ctx.Room.broadcastSystemKey("msg.unbanned", playerID)
	})

	// --- HA Authentication commands ---

	// /login - Verify HA credentials and link proxy account (CBH stays 0)
	reg.Register("login", "cmd.login.desc", false, func(ctx *CommandContext) {
		if len(ctx.Args) < 2 {
			ctx.Reply(ctx.T("msg.login_usage"))
			return
		}

		s, ok := ctx.Sender.(*Session)
		if !ok {
			return
		}

		if s.server.auth == nil {
			ctx.Reply(ctx.T("msg.ha_not_configured"))
			return
		}

		if s.authenticated {
			ctx.Reply(ctx.T("msg.already_authenticated"))
			return
		}

		identifier := ctx.Args[0]
		password := ctx.Args[1]

		// Verify credentials against HA core API
		user, err := s.server.auth.VerifyCredentials(identifier, password)
		if err != nil {
			ctx.Reply(ctx.T("msg.auth_failed_service"))
			s.server.logger.Printf("HA login error for %s: %v", s.name, err)
			return
		}
		if user == nil {
			ctx.Reply(ctx.T("msg.auth_failed_creds"))
			return
		}

		// Claim binding: link HA user UUID and flip CBH 0→1
		if err := s.server.db.ClaimBinding(s.playerID, user.UUID); err != nil {
			ctx.Reply(ctx.T("msg.claim_bind_failed", err.Error()))
			return
		}

		// Store authentication state in Redis (transient, with TTL)
		if s.server.rdb != nil {
			if err := s.server.rdb.SetAuthenticated(s.playerID, user.UUID, 24*time.Hour); err != nil {
				s.server.logger.Printf("HA: failed to set redis auth for %s: %v", s.playerID, err)
			}
		}

		s.haUserUUID = user.UUID
		s.authenticated = true

		ctx.Reply(ctx.T("msg.auth_success", user.Username))
		ctx.Room.broadcastSystemKey("msg.auth_broadcast", s.name)
		s.server.logger.Printf("HA: %s authenticated as %s (uuid=%s)", s.name, user.Username, user.UUID)
	})

	// /claim - Claim proxy account by registering in HA (CBH 0→1)
	reg.Register("claim", "cmd.claim.desc", false, func(ctx *CommandContext) {
		if len(ctx.Args) < 2 {
			ctx.Reply(ctx.T("msg.claim_usage"))
			return
		}

		s, ok := ctx.Sender.(*Session)
		if !ok {
			return
		}

		if s.server.auth == nil {
			ctx.Reply(ctx.T("msg.ha_not_configured"))
			return
		}

		// Check if already claimed
		binding, err := s.server.db.GetBindingByGameUUID(s.playerID)
		if err != nil || binding == nil {
			ctx.Reply(ctx.T("msg.claim_not_found"))
			return
		}
		if binding.CBH == 1 {
			ctx.Reply(ctx.T("msg.claim_already"))
			return
		}

		password := ctx.Args[0]
		email := ctx.Args[1]

		if len(password) < 6 {
			ctx.Reply(ctx.T("msg.claim_pwd_too_short"))
			return
		}

		// Register in HA core
		result, err := s.server.auth.Register(email, s.name, password)
		if err != nil {
			ctx.Reply(ctx.T("msg.claim_failed", err.Error()))
			s.server.logger.Printf("HA claim register error for %s: %v", s.name, err)
			return
		}

		// Claim the binding (CBH 0→1)
		if err := s.server.db.ClaimBinding(s.playerID, result.UUID); err != nil {
			ctx.Reply(ctx.T("msg.claim_bind_failed", err.Error()))
			return
		}

		// Update authentication state
		s.haUserUUID = result.UUID
		s.authenticated = true

		// Store in Redis
		if s.server.rdb != nil {
			_ = s.server.rdb.SetAuthenticated(s.playerID, result.UUID, 24*time.Hour)
		}

		ctx.Reply(ctx.T("msg.claim_success"))
		ctx.Reply(ctx.T("msg.claim_email", email))
		ctx.Reply(ctx.T("msg.claim_remember"))
		ctx.Room.broadcastSystemKey("msg.claim_broadcast", s.name)
		s.server.logger.Printf("HA: %s claimed as %s (uuid=%s)", s.name, email, result.UUID)
	})

	reg.Register("whoami", "cmd.whoami.desc", false, func(ctx *CommandContext) {
		s, ok := ctx.Sender.(*Session)
		if !ok {
			return
		}

		if s.server.auth == nil {
			ctx.Reply(ctx.T("msg.ha_not_configured"))
			return
		}

		if s.authenticated {
			ctx.Reply(ctx.T("msg.whoami_authenticated"))
			ctx.Reply(ctx.T("msg.whoami_uuid", s.haUserUUID))
		} else {
			ctx.Reply(ctx.T("msg.whoami_guest"))
			ctx.Reply(ctx.T("msg.whoami_hint"))
		}
	})

	reg.Register("version", "cmd.version.desc", false, func(ctx *CommandContext) {
		ctx.Reply(ctx.T("msg.version"))
	})

	reg.Register("lang", "cmd.lang.desc", false, func(ctx *CommandContext) {
		if len(ctx.Args) == 0 {
			ctx.Reply(ctx.T("msg.usage.lang"))
			return
		}
		s, ok := ctx.Sender.(*Session)
		if !ok {
			return
		}
		newLang := i18n.NormalizeLang(ctx.Args[0])
		if newLang != i18n.LangEN && newLang != i18n.LangZH {
			ctx.Reply(ctx.T("msg.usage.lang"))
			return
		}
		s.locale = newLang
		// Persist to DB
		if s.server.db != nil {
			if err := s.server.db.SetLang(s.playerID, newLang); err != nil {
				s.server.logger.Printf("failed to set lang for %s: %v", s.playerID, err)
			}
		}
		ctx.Reply(s.T("msg.lang_changed"))
	})

	return reg
}

// broadcastServerInfo sends a ServerInfo packet to all players in the room.
func (r *Room) broadcastServerInfo(server *Server) {
	for _, player := range r.SnapshotPlayers() {
		if player == nil {
			continue
		}
		_ = r.sendServerInfoTo(player, server)
	}
}

func (r *Room) sendServerInfoTo(session *Session, server *Server) error {
	clientVersion := int32(session.clientVersion)
	if clientVersion <= 0 {
		clientVersion = int32(server.cfg.DefaultClientVersion)
	}

	var customUnits []protocol.ModUnitDef
	if mods := server.rooms.Mods(); mods != nil {
		for _, u := range mods.AllUnits() {
			customUnits = append(customUnits, protocol.ModUnitDef{
				ModName:  u.ModName,
				UnitName: u.UnitName,
				UnitID:   u.UnitID,
			})
		}
	}

	r.mu.RLock()
	cfg := protocol.ServerInfoConfig{
		MapType:           0,
		MapName:           r.MapName,
		Credits:           r.Credits,
		FogType:           r.FogOfWar,
		IsAdmin:           r.adminIDs[session.playerID],
		MaxUnitsPerPlayer: int32(r.MaxUnits),
		StartingUnits:     r.StartingUnits,
		Income:            r.Income,
		DisableNuke:       r.DisableNuke,
		SharedControl:     r.SharedControl,
		CustomUnits:       customUnits,
	}
	r.mu.RUnlock()

	packet := protocol.EncodeServerInfo(cfg, clientVersion)
	return session.Send(packet)
}

func fogTypeText(locale string, fog int32) string {
	switch fog {
	case 0:
		return i18n.T(locale, "fog.off")
	case 1:
		return i18n.T(locale, "fog.basic")
	case 2:
		return i18n.T(locale, "fog.los")
	default:
		return fmt.Sprintf(i18n.T(locale, "fog.unknown"), fog)
	}
}

func boolText(locale string, val bool) string {
	if val {
		return i18n.T(locale, "bool.on")
	}
	return i18n.T(locale, "bool.off")
}

func parseBool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "yes", "on":
		return true, nil
	case "false", "0", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("invalid boolean: %s", s)
	}
}

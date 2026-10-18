package relay

import (
	"fmt"
	"rwgin/internal/protocol"
	"strconv"
	"strings"
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

// CommandHandler is a function that handles a specific command.
type CommandHandler func(ctx *CommandContext)

type commandEntry struct {
	handler     CommandHandler
	description string
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
				if err := s.systemMessage("需要管理员权限。"); err != nil {
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

// DefaultRegistry returns a registry with all built-in commands.
func DefaultRegistry() *CommandRegistry {
	reg := NewCommandRegistry("/")

	// --- General commands ---

	reg.Register("help", "显示所有可用命令", false, func(ctx *CommandContext) {
		var lines []string
		for name, entry := range reg.commands {
			adminTag := ""
			if entry.adminOnly {
				adminTag = " [管理]"
			}
			lines = append(lines, fmt.Sprintf("/%s - %s%s", name, entry.description, adminTag))
		}
		// Sort for consistent output
		joined := strings.Join(lines, "\n")
		if s, ok := ctx.Sender.(*Session); ok {
			_ = s.systemMessage(joined)
		}
	})

	reg.Register("ping", "测试延迟", false, func(ctx *CommandContext) {
		ctx.Reply("Pong!")
	})

	reg.Register("list", "显示房间成员列表", false, func(ctx *CommandContext) {
		ctx.Reply("当前房间成员:\n" + ctx.Room.MembersText())
	})

	reg.Register("info", "显示房间详细信息", false, func(ctx *CommandContext) {
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

		status := "等待中"
		if detail.InGame {
			if paused {
				status = "游戏中 (已暂停)"
			} else {
				status = "游戏中"
			}
		}
		fogText := fogTypeText(fog)
		info := fmt.Sprintf("房间: %s\n地图: %s\n人数: %d/%d\n状态: %s\n资金: %d\n收入: %.1f\n最大单位: %d\n迷雾: %s\n核弹: %s\n共享控制: %s",
			detail.Title, detail.MapName, detail.Players, detail.MaxPlayers,
			status, credits, income, maxUnits, fogText,
			boolText(!nuke), boolText(shared))
		ctx.Reply(info)
	})

	reg.Register("maps", "列出可用地图", false, func(ctx *CommandContext) {
		maps := ctx.Room.commands // access through room to get manager
		_ = maps                   // we need the manager
		// Get maps from the room's manager via the session
		if s, ok := ctx.Sender.(*Session); ok {
			mapsList := s.server.rooms.ListMaps()
			if len(mapsList) == 0 {
				ctx.Reply("没有可用的地图。")
				return
			}
			var sb strings.Builder
			sb.WriteString("可用地图:\n")
			for i, m := range mapsList {
				sb.WriteString(fmt.Sprintf("[%d] %s\n", i, m))
			}
			ctx.Reply(sb.String())
		}
	})

	// --- Admin game config commands ---

	reg.Register("map", "设置地图 (用法: /map <名称或索引>)", true, func(ctx *CommandContext) {
		if len(ctx.Args) == 0 {
			ctx.Reply("用法: /map <地图名称或索引>")
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
					ctx.Reply(fmt.Sprintf("未找到地图: %s", mapArg))
					return
				}
			}

			ctx.Room.mu.Lock()
			ctx.Room.MapName = mapName
			ctx.Room.mu.Unlock()

			ctx.Room.broadcastSystem(fmt.Sprintf("地图已更改为: %s", mapName))
			ctx.Room.broadcastServerInfo(s.server)
		}
	})

	reg.Register("credits", "设置初始资金 (用法: /credits <金额>)", true, func(ctx *CommandContext) {
		if len(ctx.Args) == 0 {
			ctx.Reply("用法: /credits <金额>")
			return
		}
		val, err := strconv.Atoi(ctx.Args[0])
		if err != nil || val < 0 {
			ctx.Reply("请输入有效的非负整数。")
			return
		}
		ctx.Room.mu.Lock()
		ctx.Room.Credits = int32(val)
		ctx.Room.mu.Unlock()

		ctx.Room.broadcastSystem(fmt.Sprintf("初始资金已设为: %d", val))
		if s, ok := ctx.Sender.(*Session); ok {
			ctx.Room.broadcastServerInfo(s.server)
		}
	})

	reg.Register("income", "设置收入倍率 (用法: /income <0.0-100.0>)", true, func(ctx *CommandContext) {
		if len(ctx.Args) == 0 {
			ctx.Reply("用法: /income <倍率>")
			return
		}
		val, err := strconv.ParseFloat(ctx.Args[0], 32)
		if err != nil || val < 0 || val > 100 {
			ctx.Reply("请输入 0-100 之间的数值。")
			return
		}
		ctx.Room.mu.Lock()
		ctx.Room.Income = float32(val)
		ctx.Room.mu.Unlock()

		ctx.Room.broadcastSystem(fmt.Sprintf("收入倍率已设为: %.1f", val))
		if s, ok := ctx.Sender.(*Session); ok {
			ctx.Room.broadcastServerInfo(s.server)
		}
	})

	reg.Register("fog", "设置战争迷雾 (用法: /fog <off|basic|los>)", true, func(ctx *CommandContext) {
		if len(ctx.Args) == 0 {
			ctx.Reply("用法: /fog <off|basic|los>")
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
			ctx.Reply("无效选项。可选: off, basic, los")
			return
		}
		ctx.Room.mu.Lock()
		ctx.Room.FogOfWar = fogType
		ctx.Room.mu.Unlock()

		ctx.Room.broadcastSystem(fmt.Sprintf("战争迷雾已设为: %s", fogTypeText(fogType)))
		if s, ok := ctx.Sender.(*Session); ok {
			ctx.Room.broadcastServerInfo(s.server)
		}
	})

	reg.Register("nukes", "设置核弹开关 (用法: /nukes <true|false>)", true, func(ctx *CommandContext) {
		if len(ctx.Args) == 0 {
			ctx.Reply("用法: /nukes <true|false>")
			return
		}
		val, err := parseBool(ctx.Args[0])
		if err != nil {
			ctx.Reply("请输入 true 或 false。")
			return
		}
		ctx.Room.mu.Lock()
		ctx.Room.DisableNuke = !val
		ctx.Room.mu.Unlock()

		ctx.Room.broadcastSystem(fmt.Sprintf("核弹已%s", boolText(val)))
		if s, ok := ctx.Sender.(*Session); ok {
			ctx.Room.broadcastServerInfo(s.server)
		}
	})

	reg.Register("startingunits", "设置初始单位数 (用法: /startingunits <数量>)", true, func(ctx *CommandContext) {
		if len(ctx.Args) == 0 {
			ctx.Reply("用法: /startingunits <数量>")
			return
		}
		val, err := strconv.Atoi(ctx.Args[0])
		if err != nil || val < 0 {
			ctx.Reply("请输入有效的非负整数。")
			return
		}
		ctx.Room.mu.Lock()
		ctx.Room.StartingUnits = int32(val)
		ctx.Room.mu.Unlock()

		ctx.Room.broadcastSystem(fmt.Sprintf("初始单位数已设为: %d", val))
		if s, ok := ctx.Sender.(*Session); ok {
			ctx.Room.broadcastServerInfo(s.server)
		}
	})

	reg.Register("maxunits", "设置每玩家最大单位数 (用法: /maxunits <数量>)", true, func(ctx *CommandContext) {
		if len(ctx.Args) == 0 {
			ctx.Reply("用法: /maxunits <数量>")
			return
		}
		val, err := strconv.Atoi(ctx.Args[0])
		if err != nil || val < 1 {
			ctx.Reply("请输入有效的正整数。")
			return
		}
		ctx.Room.mu.Lock()
		ctx.Room.MaxUnits = val
		ctx.Room.mu.Unlock()

		ctx.Room.broadcastSystem(fmt.Sprintf("最大单位数已设为: %d", val))
		ctx.Room.broadcastTeamList()
		if s, ok := ctx.Sender.(*Session); ok {
			ctx.Room.broadcastServerInfo(s.server)
		}
	})

	reg.Register("share", "开关共享控制 (用法: /share <true|false>)", true, func(ctx *CommandContext) {
		if len(ctx.Args) == 0 {
			ctx.Reply("用法: /share <true|false>")
			return
		}
		val, err := parseBool(ctx.Args[0])
		if err != nil {
			ctx.Reply("请输入 true 或 false。")
			return
		}
		ctx.Room.mu.Lock()
		ctx.Room.SharedControl = val
		ctx.Room.mu.Unlock()

		ctx.Room.broadcastSystem(fmt.Sprintf("共享控制已%s", boolText(val)))
		if s, ok := ctx.Sender.(*Session); ok {
			ctx.Room.broadcastServerInfo(s.server)
		}
	})

	// --- Team/slot management ---

	reg.Register("team", "设置玩家队伍 (用法: /team <玩家> <队伍号>)", true, func(ctx *CommandContext) {
		if len(ctx.Args) < 2 {
			ctx.Reply("用法: /team <玩家名或#槽位> <队伍号>")
			return
		}
		target, err := ctx.Room.FindTarget(ctx.Args[0])
		if err != nil {
			ctx.Reply("错误: " + err.Error())
			return
		}
		teamNum, err := strconv.Atoi(ctx.Args[1])
		if err != nil {
			ctx.Reply("请输入有效的队伍号。")
			return
		}
		ctx.Room.mu.Lock()
		target.team = int32(teamNum)
		ctx.Room.mu.Unlock()

		ctx.Room.broadcastSystem(fmt.Sprintf("%s 的队伍已设为 %d", target.Name(), teamNum))
		_ = ctx.Room.broadcastTeamList()
	})

	reg.Register("self_team", "设置自己的队伍 (用法: /self_team <队伍号>)", false, func(ctx *CommandContext) {
		if len(ctx.Args) == 0 {
			ctx.Reply("用法: /self_team <队伍号>")
			return
		}
		teamNum, err := strconv.Atoi(ctx.Args[0])
		if err != nil {
			ctx.Reply("请输入有效的队伍号。")
			return
		}
		if s, ok := ctx.Sender.(*Session); ok {
			ctx.Room.mu.Lock()
			s.team = int32(teamNum)
			ctx.Room.mu.Unlock()

			ctx.Room.broadcastSystem(fmt.Sprintf("%s 的队伍已设为 %d", s.Name(), teamNum))
			_ = ctx.Room.broadcastTeamList()
		}
	})

	reg.Register("move", "移动玩家到指定槽位 (用法: /move <玩家> <槽位>)", true, func(ctx *CommandContext) {
		if len(ctx.Args) < 2 {
			ctx.Reply("用法: /move <玩家名或#槽位> <目标槽位>")
			return
		}
		target, err := ctx.Room.FindTarget(ctx.Args[0])
		if err != nil {
			ctx.Reply("错误: " + err.Error())
			return
		}
		newSlot, err := strconv.Atoi(ctx.Args[1])
		if err != nil || newSlot < 0 || newSlot >= ctx.Room.MaxPlayers {
			ctx.Reply(fmt.Sprintf("无效槽位，有效范围: 0-%d", ctx.Room.MaxPlayers-1))
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
			ctx.Reply("未找到该玩家。")
			return
		}
		if oldSlot == newSlot {
			ctx.Reply("玩家已在该槽位。")
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
		ctx.Room.broadcastSystem(fmt.Sprintf("%s 已移动到槽位 %d", target.Name(), newSlot))
	})

	reg.Register("self_move", "移动自己到指定槽位 (用法: /self_move <槽位>)", false, func(ctx *CommandContext) {
		if len(ctx.Args) == 0 {
			ctx.Reply("用法: /self_move <目标槽位>")
			return
		}
		newSlot, err := strconv.Atoi(ctx.Args[0])
		if err != nil || newSlot < 0 || newSlot >= ctx.Room.MaxPlayers {
			ctx.Reply(fmt.Sprintf("无效槽位，有效范围: 0-%d", ctx.Room.MaxPlayers-1))
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
				ctx.Reply("未找到自己。")
				return
			}
			if oldSlot == newSlot {
				ctx.Reply("你已在该槽位。")
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
			ctx.Room.broadcastSystem(fmt.Sprintf("%s 已移动到槽位 %d", s.Name(), newSlot))
		}
	})

	// --- Game control ---

	reg.Register("kick", "踢出玩家 (用法: /kick <玩家名或#槽位>)", true, func(ctx *CommandContext) {
		if len(ctx.Args) == 0 {
			ctx.Reply("用法: /kick <玩家名或#槽位>")
			return
		}
		target, err := ctx.Room.FindTarget(ctx.Args[0])
		if err != nil {
			ctx.Reply("错误: " + err.Error())
			return
		}
		if s, ok := ctx.Sender.(*Session); ok {
			err = ctx.Room.Kick(s, target)
			if err != nil {
				ctx.Reply("踢出失败: " + err.Error())
			}
		}
	})

	reg.Register("transfer", "转让房主 (用法: /transfer <玩家名或#槽位>)", false, func(ctx *CommandContext) {
		if len(ctx.Args) == 0 {
			ctx.Reply("用法: /transfer <玩家名或#槽位>")
			return
		}
		target, err := ctx.Room.FindTarget(ctx.Args[0])
		if err != nil {
			ctx.Reply("错误: " + err.Error())
			return
		}
		if s, ok := ctx.Sender.(*Session); ok {
			err = ctx.Room.TransferOwnership(s, target)
			if err != nil {
				ctx.Reply("转移失败: " + err.Error())
			} else {
				ctx.Room.broadcastSystem(fmt.Sprintf("房主已转移给 %s", target.Name()))
			}
		}
	})

	reg.Register("start", "投票开始游戏 (管理员可直接开始)", false, func(ctx *CommandContext) {
		if ctx.Room.IsInGame() {
			ctx.Reply("游戏已经在运行中。")
			return
		}

		s, ok := ctx.Sender.(*Session)
		if !ok {
			return
		}

		// Admin can force start
		if ctx.Room.IsAdmin(s) {
			ctx.Room.broadcastSystem(fmt.Sprintf("管理员 %s 直接开始游戏。", s.Name()))
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
			ctx.Room.broadcastSystem(fmt.Sprintf("投票通过 (%d/%d)，游戏立即开始！", votes, required))
			_ = s.handleStartGame(protocol.Packet{})
		} else {
			ctx.Room.broadcastSystem(fmt.Sprintf("%s 投票开始游戏 (%d/%d)", s.Name(), votes, required))
		}
	})

	reg.Register("return", "返回大厅 (管理员)", true, func(ctx *CommandContext) {
		if !ctx.Room.IsInGame() {
			ctx.Reply("当前不在游戏中。")
			return
		}
		ctx.Room.SetInGame(false)
		ctx.Room.broadcast(protocol.EncodeReturnToBattleroom())
		ctx.Room.broadcastSystem("管理员已结束游戏，返回大厅。")
		_ = ctx.Room.broadcastTeamList()
	})

	reg.Register("admin", "管理管理员 (用法: /admin <add|remove> <玩家>)", false, func(ctx *CommandContext) {
		if len(ctx.Args) < 2 {
			ctx.Reply("用法: /admin <add|remove> <玩家名或#槽位>")
			return
		}
		action := strings.ToLower(ctx.Args[0])
		target, err := ctx.Room.FindTarget(ctx.Args[1])
		if err != nil {
			ctx.Reply("错误: " + err.Error())
			return
		}
		if s, ok := ctx.Sender.(*Session); ok {
			switch action {
			case "add":
				err = ctx.Room.SetAdmin(s, target, true)
				if err != nil {
					ctx.Reply("失败: " + err.Error())
				} else {
					ctx.Room.broadcastSystem(fmt.Sprintf("%s 已被设为管理员。", target.Name()))
				}
			case "remove":
				err = ctx.Room.SetAdmin(s, target, false)
				if err != nil {
					ctx.Reply("失败: " + err.Error())
				} else {
					ctx.Room.broadcastSystem(fmt.Sprintf("%s 已被移除管理员。", target.Name()))
				}
			default:
				ctx.Reply("用法: /admin <add|remove> <玩家>")
			}
		}
	})

	reg.Register("mute", "禁言玩家 (用法: /mute <玩家名或#槽位>)", true, func(ctx *CommandContext) {
		if len(ctx.Args) == 0 {
			ctx.Reply("用法: /mute <玩家名或#槽位>")
			return
		}
		target, err := ctx.Room.FindTarget(ctx.Args[0])
		if err != nil {
			ctx.Reply("错误: " + err.Error())
			return
		}
		if s, ok := ctx.Sender.(*Session); ok {
			err = ctx.Room.Mute(s, target)
			if err != nil {
				ctx.Reply("禁言失败: " + err.Error())
			} else {
				ctx.Room.broadcastSystem(fmt.Sprintf("%s 已被禁言。", target.Name()))
			}
		}
	})

	reg.Register("unmute", "解除禁言 (用法: /unmute <玩家名或#槽位>)", true, func(ctx *CommandContext) {
		if len(ctx.Args) == 0 {
			ctx.Reply("用法: /unmute <玩家名或#槽位>")
			return
		}
		target, err := ctx.Room.FindTarget(ctx.Args[0])
		if err != nil {
			ctx.Reply("错误: " + err.Error())
			return
		}
		if s, ok := ctx.Sender.(*Session); ok {
			err = ctx.Room.Unmute(s, target)
			if err != nil {
				ctx.Reply("解除禁言失败: " + err.Error())
			} else {
				ctx.Room.broadcastSystem(fmt.Sprintf("%s 已解除禁言。", target.Name()))
			}
		}
	})

	reg.Register("pause", "暂停游戏 (管理员)", true, func(ctx *CommandContext) {
		if !ctx.Room.IsInGame() {
			ctx.Reply("当前不在游戏中。")
			return
		}
		if ctx.Room.IsPaused() {
			ctx.Reply("游戏已经暂停。")
			return
		}
		ctx.Room.SetPaused(true)
		ctx.Room.broadcastSystem(fmt.Sprintf("游戏已由 %s 暂停。", ctx.Sender.Name()))
	})

	reg.Register("unpause", "恢复游戏 (管理员)", true, func(ctx *CommandContext) {
		if !ctx.Room.IsInGame() {
			ctx.Reply("当前不在游戏中。")
			return
		}
		if !ctx.Room.IsPaused() {
			ctx.Reply("游戏未暂停。")
			return
		}
		ctx.Room.SetPaused(false)
		ctx.Room.broadcastSystem(fmt.Sprintf("游戏已由 %s 恢复。", ctx.Sender.Name()))
	})

	// --- Vote commands ---

	reg.Register("sync", "发起同步投票", false, func(ctx *CommandContext) {
		if !ctx.Room.IsInGame() {
			ctx.Reply("当前不在游戏中。")
			return
		}

		s, ok := ctx.Sender.(*Session)
		if !ok {
			return
		}

		// Admin can force sync
		if ctx.Room.IsAdmin(s) {
			ctx.Room.broadcastSystem(fmt.Sprintf("管理员 %s 发起同步。", s.Name()))
			go ctx.Room.TriggerSync(s.server)
			return
		}

		// Check if there's already an active sync vote
		if vote := ctx.Room.GetActiveVote(); vote != nil && vote.Type == "sync" {
			// Cast vote
			votes, ok := ctx.Room.CastVote("sync", s.playerID)
			if !ok {
				ctx.Reply("投票已过期。")
				return
			}
			totalHumans := ctx.Room.HumanCountLocked()
			required := (totalHumans + 1) / 2
			if votes >= required {
				ctx.Room.ClearVote()
				ctx.Room.broadcastSystem(fmt.Sprintf("同步投票通过 (%d/%d)！", votes, required))
				go ctx.Room.TriggerSync(s.server)
			} else {
				ctx.Room.broadcastSystem(fmt.Sprintf("%s 赞成同步 (%d/%d)", s.Name(), votes, required))
			}
			return
		}

		// Start new vote
		ctx.Room.StartVote("sync", s.playerID)
		totalHumans := ctx.Room.HumanCountLocked()
		required := (totalHumans + 1) / 2
		if 1 >= required {
			ctx.Room.ClearVote()
			ctx.Room.broadcastSystem("同步投票通过！")
			go ctx.Room.TriggerSync(s.server)
		} else {
			ctx.Room.broadcastSystem(fmt.Sprintf("%s 发起同步投票 (1/%d)", s.Name(), required))
		}
	})

	reg.Register("gameover", "发起结束游戏投票", false, func(ctx *CommandContext) {
		if !ctx.Room.IsInGame() {
			ctx.Reply("当前不在游戏中。")
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
			ctx.Room.broadcastSystem(fmt.Sprintf("管理员 %s 结束了游戏。", s.Name()))
			_ = ctx.Room.broadcastTeamList()
			return
		}

		// Check if there's already an active gameover vote
		if vote := ctx.Room.GetActiveVote(); vote != nil && vote.Type == "gameover" {
			// Cast vote
			votes, ok := ctx.Room.CastVote("gameover", s.playerID)
			if !ok {
				ctx.Reply("投票已过期。")
				return
			}
			totalHumans := ctx.Room.HumanCountLocked()
			required := (totalHumans + 1) / 2
			if votes >= required {
				ctx.Room.ClearVote()
				ctx.Room.SetInGame(false)
				ctx.Room.broadcast(protocol.EncodeReturnToBattleroom())
				ctx.Room.broadcastSystem(fmt.Sprintf("结束游戏投票通过 (%d/%d)！", votes, required))
				_ = ctx.Room.broadcastTeamList()
			} else {
				ctx.Room.broadcastSystem(fmt.Sprintf("%s 赞成结束游戏 (%d/%d)", s.Name(), votes, required))
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
			ctx.Room.broadcastSystem("结束游戏投票通过！")
			_ = ctx.Room.broadcastTeamList()
		} else {
			ctx.Room.broadcastSystem(fmt.Sprintf("%s 发起结束游戏投票 (1/%d)", s.Name(), required))
		}
	})

	// --- Ban commands ---

	reg.Register("ban", "封禁玩家 (用法: /ban <玩家名或#槽位>)", true, func(ctx *CommandContext) {
		if len(ctx.Args) == 0 {
			ctx.Reply("用法: /ban <玩家名或#槽位>")
			return
		}
		target, err := ctx.Room.FindTarget(ctx.Args[0])
		if err != nil {
			ctx.Reply("错误: " + err.Error())
			return
		}
		if s, ok := ctx.Sender.(*Session); ok {
			if !ctx.Room.IsAdmin(s) {
				ctx.Reply("需要管理员权限。")
				return
			}
			ctx.Room.BanPlayer(target.playerID)
			ctx.Room.broadcastSystem(fmt.Sprintf("%s 已被封禁。", target.Name()))
			// Kick the banned player
			_ = target.Send(protocol.EncodeKick("你已被封禁。"))
			target.close()
		}
	})

	reg.Register("unban", "解封玩家 (用法: /unban <玩家ID>)", true, func(ctx *CommandContext) {
		if len(ctx.Args) == 0 {
			ctx.Reply("用法: /unban <玩家ID>")
			return
		}
		playerID := ctx.Args[0]
		if !ctx.Room.IsBanned(playerID) {
			ctx.Reply("该玩家未被封禁。")
			return
		}
		ctx.Room.UnbanPlayer(playerID)
		ctx.Room.broadcastSystem(fmt.Sprintf("玩家 %s 已被解封。", playerID))
	})

	reg.Register("version", "显示服务器版本信息", false, func(ctx *CommandContext) {
		ctx.Reply("MCAGS-RW Server v0.1.0")
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

func fogTypeText(fog int32) string {
	switch fog {
	case 0:
		return "关闭"
	case 1:
		return "基础"
	case 2:
		return "视线"
	default:
		return fmt.Sprintf("未知(%d)", fog)
	}
}

func boolText(val bool) string {
	if val {
		return "开启"
	}
	return "关闭"
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


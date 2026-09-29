package relay

import (
	"fmt"
	"rwgin/internal/protocol"
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

// CommandRegistry manages the available commands.
type CommandRegistry struct {
	commands map[string]CommandHandler
	prefix   string
}

// NewCommandRegistry creates a new registry with the given prefix.
func NewCommandRegistry(prefix string) *CommandRegistry {
	return &CommandRegistry{
		commands: make(map[string]CommandHandler),
		prefix:   prefix,
	}
}

// Register adds a new command to the registry.
func (r *CommandRegistry) Register(name string, handler CommandHandler) {
	r.commands[strings.ToLower(name)] = handler
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

	handler, ok := r.commands[cmdName]
	if !ok {
		return false
	}

	ctx := &CommandContext{
		Room:    room,
		Sender:  sender,
		Command: cmdName,
		Args:    args,
	}

	handler(ctx)
	return true
}

// DefaultRegistry returns a registry with some basic commands.
func DefaultRegistry() *CommandRegistry {
	reg := NewCommandRegistry("/")

	reg.Register("help", func(ctx *CommandContext) {
		var cmds []string
		for name := range reg.commands {
			cmds = append(cmds, "/"+name)
		}
		ctx.Reply("可用指令: " + strings.Join(cmds, ", "))
	})

	reg.Register("ping", func(ctx *CommandContext) {
		ctx.Reply("Pong!")
	})

	reg.Register("list", func(ctx *CommandContext) {
		ctx.Reply("当前房间成员:\n" + ctx.Room.MembersText())
	})

	reg.Register("info", func(ctx *CommandContext) {
		detail := ctx.Room.Summary()
		info := fmt.Sprintf("房间: %s\n地图: %s\n人数: %d/%d\n状态: %s",
			detail.Title, detail.MapName, detail.Players, detail.MaxPlayers,
			map[bool]string{true: "游戏中", false: "等待中"}[detail.InGame])
		ctx.Reply(info)
	})

	reg.Register("kick", func(ctx *CommandContext) {
		if len(ctx.Args) == 0 {
			ctx.Reply("用法: /kick <玩家名或#槽位>")
			return
		}
		target, err := ctx.Room.FindTarget(ctx.Args[0])
		if err != nil {
			ctx.Reply("错误: " + err.Error())
			return
		}
		// In this implementation, we follow user decision: "fully open"
		// Normally we would check if ctx.Sender is admin.
		if s, ok := ctx.Sender.(*Session); ok {
			err = ctx.Room.Kick(s, target)
			if err != nil {
				ctx.Reply("踢出失败: " + err.Error())
			}
		}
	})

	reg.Register("transfer", func(ctx *CommandContext) {
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

	reg.Register("start", func(ctx *CommandContext) {
		if ctx.Room.IsInGame() {
			ctx.Reply("游戏已经在运行中。")
			return
		}

		if s, ok := ctx.Sender.(*Session); ok {
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
		}
	})

	return reg
}

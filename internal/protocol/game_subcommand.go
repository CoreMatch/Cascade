package protocol

import "fmt"

const (
	SubCommandFinish     = 4
	SubCommandChat       = 31
	SubCommandUnitAdd    = 32
	SubCommandUnitRemove = 33
	SubCommandUnitMove   = 35

	// Server-to-client only commands
	SubCommandServerUnitAdd  = 100
	SubCommandServerUnitMove = 101
)

// SubCommand defines the interface for a game subcommand.
type SubCommand interface {
	subCommand()
}

type SubCommandChatPacket struct {
	Message string
}

func (p SubCommandChatPacket) subCommand() {}

func (p *SubCommandChatPacket) Encode(w *Writer) error {
	w.Byte(SubCommandChat)
	return w.String(p.Message)
}

type SubCommandUnitAddPacket struct {
	Count      int32
	UnitType   string
	X, Y       float32
	Owner      string // This might need to be resolved to a player ID
	ShouldSync bool
}

func (p SubCommandUnitAddPacket) subCommand() {}

func (p *SubCommandUnitAddPacket) Encode(w *Writer) error {
	w.Byte(SubCommandUnitAdd)
	w.Byte(0) // Unknown byte
	w.Int32(p.Count)
	if err := w.String(p.UnitType); err != nil {
		return err
	}
	w.Float32(p.X)
	w.Float32(p.Y)
	if err := w.String(p.Owner); err != nil {
		return err
	}
	w.Bool(p.ShouldSync)
	return nil
}

type SubCommandUnitMovePacket struct {
	UnitIDs []int32
	X, Y    float32
}

func (p SubCommandUnitMovePacket) subCommand() {}

func (p *SubCommandUnitMovePacket) Encode(w *Writer) error {
	w.Byte(SubCommandUnitMove)
	w.Int32(int32(len(p.UnitIDs)))
	for _, id := range p.UnitIDs {
		w.Int32(id)
	}
	w.Float32(p.X)
	w.Float32(p.Y)
	return nil
}

type SubCommandServerUnitAddPacket struct {
	UnitID   int64
	UnitType string
	X, Y     float32
	Owner    string
}

func (p SubCommandServerUnitAddPacket) subCommand() {}

func (p *SubCommandServerUnitAddPacket) Encode(w *Writer) error {
	w.Byte(SubCommandServerUnitAdd)
	w.Int64(p.UnitID)
	if err := w.String(p.UnitType); err != nil {
		return err
	}
	w.Float32(p.X)
	w.Float32(p.Y)
	if err := w.String(p.Owner); err != nil {
		return err
	}
	return nil
}

type SubCommandServerUnitMovePacket struct {
	UnitID int64
	X, Y   float32
}

func (p SubCommandServerUnitMovePacket) subCommand() {}

func (p *SubCommandServerUnitMovePacket) Encode(w *Writer) error {
	w.Byte(SubCommandServerUnitMove)
	w.Int64(p.UnitID)
	w.Float32(p.X)
	w.Float32(p.Y)
	return nil
}

// ParseSubCommands parses the data from a GameCommandPacket into a slice of SubCommands.
func ParseSubCommands(data []byte) ([]SubCommand, error) {
	r := NewReader(data)
	var commands []SubCommand

	for r.Remaining() > 0 {
		cmdType, err := r.Byte()
		if err != nil {
			return nil, fmt.Errorf("read subcommand type: %w", err)
		}

		var cmd SubCommand
		switch cmdType {
		case SubCommandChat:
			message, err := r.String()
			if err != nil {
				return nil, fmt.Errorf("decode chat subcommand: %w", err)
			}
			cmd = SubCommandChatPacket{Message: message}
		case SubCommandUnitAdd:
			p := SubCommandUnitAddPacket{}
			if err := r.Skip(1); err != nil { // Unknown byte
				return nil, err
			}
			p.Count, err = r.Int32()
			if err != nil {
				return nil, err
			}
			p.UnitType, err = r.String()
			if err != nil {
				return nil, err
			}
			p.X, err = r.Float32()
			if err != nil {
				return nil, err
			}
			p.Y, err = r.Float32()
			if err != nil {
				return nil, err
			}
			p.Owner, err = r.String() // This is often a team index, not a player ID
			if err != nil {
				return nil, err
			}
			p.ShouldSync, err = r.Bool()
			if err != nil {
				return nil, err
			}
			cmd = p
		case SubCommandUnitMove:
			p := SubCommandUnitMovePacket{}
			count, err := r.Int32()
			if err != nil {
				return nil, err
			}
			p.UnitIDs = make([]int32, count)
			for i := int32(0); i < count; i++ {
				p.UnitIDs[i], err = r.Int32()
				if err != nil {
					return nil, err
				}
			}
			p.X, err = r.Float32()
			if err != nil {
				return nil, err
			}
			p.Y, err = r.Float32()
			if err != nil {
				return nil, err
			}
			cmd = p
		case SubCommandServerUnitAdd:
			return nil, fmt.Errorf("client sent a server-only command: %d", cmdType)
		case SubCommandServerUnitMove:
			return nil, fmt.Errorf("client sent a server-only command: %d", cmdType)
		default:
			// For unknown commands, we can't reliably continue parsing.
			// It's safer to stop here.
			return commands, fmt.Errorf("unknown subcommand type: %d", cmdType)
		}
		commands = append(commands, cmd)
	}

	return commands, nil
}

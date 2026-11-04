package protocol

// Packet types the edge proxy acts on. All other Rusted Warfare packet
// types are forwarded unmodified between client and backend (see knowledge
// entry 0004: byte passthrough), so they are not named here.
const (
	TypeRegisterPlayer = 110
	TypeChatReceive    = 140
	TypeChatBroadcast  = 141
	TypeKick           = 150

	TypePreregisterReceive = 160
	TypePreregister        = 161
)

type Packet struct {
	Type int32
	Body []byte
}

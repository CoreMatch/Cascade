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

	// TypeReconnectTo is the native Rusted Warfare "reconnect to another
	// server" packet. The edge proxy sends it to steer a vanilla client from
	// the lobby to a sub-server jump port without exposing backend addresses.
	TypeReconnectTo = 178
)

type Packet struct {
	Type int32
	Body []byte
}

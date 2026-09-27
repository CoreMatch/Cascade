package protocol

const (
	TypeDisconnect         = 111
	TypeRegisterPlayer     = 110
	TypeServerInfo         = 106
	TypeTeamList           = 115
	TypeHeartbeat          = 108
	TypeHeartbeatResponse  = 109
	TypeChatReceive        = 140
	TypeChatBroadcast      = 141
	TypeStartGame          = 120
	TypeReturnToBattleRoom = 122
	TypePreregisterReceive = 160
	TypePreregister        = 161
	TypeRelayPrompt        = 117
	TypeRelayPromptReply   = 118
	TypeRelayVersionInfo   = 163
	TypeRelayBecomeServer  = 170
	TypeRelayPow           = 151
	TypeRelayPowReceive    = 152
	TypeGameCommand        = 116
)

type Packet struct {
	Type int32
	Body []byte
}

type GameCommandPacket struct {
	Packet
	Data []byte
}

package protocol

const (
	// Game runtime packets
	TypeTick               = 10
	TypeGameCommand        = 20
	TypeSyncChecksum       = 30
	TypeSyncChecksumReply  = 31
	TypeSync               = 35

	// Server-to-client packets
	TypeServerInfo         = 106
	TypeHeartbeat          = 108
	TypeHeartbeatResponse  = 109
	TypeStartGame          = 120
	TypeReturnToBattleRoom = 122
	TypeKick               = 150
	TypeDisconnect         = 111
	TypeTeamList           = 115
	TypeChatBroadcast      = 141

	// Client-to-server packets
	TypeRegisterPlayer     = 110
	TypeReady              = 112
	TypeChatReceive        = 140

	// Registration packets
	TypePreregisterReceive = 160
	TypePreregister        = 161

	// Question packets
	TypeRelayPrompt        = 117
	TypeRelayPromptReply   = 118

	// Relay packets
	TypeRelayVersionInfo   = 163
	TypeRelayBecomeServer  = 170
	TypeRelayPow           = 151
	TypeRelayPowReceive    = 152
	TypeForwardClientAdd   = 172
	TypeForwardClientRemove = 173
	TypeForwardClientFrom  = 174
	TypeForwardClientTo    = 175
	TypeForwardClientToRepeated = 176
)

type Packet struct {
	Type int32
	Body []byte
}

type GameCommandPacket struct {
	Packet
	Data []byte
}

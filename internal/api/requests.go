package api

type roomCreateRequest struct {
	Code       string `json:"code"`
	Title      string `json:"title"`
	MaxPlayers int    `json:"maxPlayers"`
}

type roomSettingsRequest struct {
	Title         *string  `json:"title"`
	MapName       *string  `json:"mapName"`
	Credits       *int32   `json:"credits"`
	FogOfWar      *int32   `json:"fogOfWar"`
	Income        *float32 `json:"income"`
	DisableNuke   *bool    `json:"disableNuke"`
	SharedControl *bool    `json:"sharedControl"`
	StartingUnits *int32   `json:"startingUnits"`
	MaxUnits      *int     `json:"maxUnits"`
}

type playerActionRequest struct {
	PlayerID string `json:"playerId" binding:"required"`
}

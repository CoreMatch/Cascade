package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"rwgin/internal/protocol"
	"rwgin/internal/relay"
)

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

func Router(rooms *relay.Manager) *gin.Engine {
	engine := gin.New()
	engine.Use(gin.Logger(), gin.Recovery())

	// Health check
	engine.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// Room list
	engine.GET("/api/rooms", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"rooms": rooms.List()})
	})

	// Room detail
	engine.GET("/api/rooms/:code", func(c *gin.Context) {
		room, ok := rooms.Get(c.Param("code"))
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "room not found"})
			return
		}
		c.JSON(http.StatusOK, room.Detail())
	})

	// Create room
	engine.POST("/api/rooms", func(c *gin.Context) {
		var req roomCreateRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		room, err := rooms.Create(req.Code, req.Title, "", req.MaxPlayers)
		if err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusCreated, room.Summary())
	})

	// Delete room
	engine.DELETE("/api/rooms/:code", func(c *gin.Context) {
		room, ok := rooms.Get(c.Param("code"))
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "room not found"})
			return
		}

		// Kick all players first
		for _, player := range room.SnapshotPlayers() {
			if player != nil {
				_ = player.Send(protocol.EncodeKick("房间已被管理员删除。"))
				player.Close()
			}
		}

		rooms.Remove(room.Code)
		c.JSON(http.StatusOK, gin.H{"message": "room deleted"})
	})

	// Update room settings
	engine.PUT("/api/rooms/:code/settings", func(c *gin.Context) {
		room, ok := rooms.Get(c.Param("code"))
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "room not found"})
			return
		}

		var req roomSettingsRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		if req.Title != nil {
			room.SetTitle(*req.Title)
		}
		if req.MapName != nil {
			room.SetMapName(*req.MapName)
		}
		if req.Credits != nil {
			room.SetCredits(*req.Credits)
		}
		if req.FogOfWar != nil {
			room.SetFogOfWar(*req.FogOfWar)
		}
		if req.Income != nil {
			room.SetIncome(*req.Income)
		}
		if req.DisableNuke != nil {
			room.SetDisableNuke(*req.DisableNuke)
		}
		if req.SharedControl != nil {
			room.SetSharedControl(*req.SharedControl)
		}
		if req.StartingUnits != nil {
			room.SetStartingUnits(*req.StartingUnits)
		}
		if req.MaxUnits != nil {
			room.SetMaxUnits(*req.MaxUnits)
		}

		c.JSON(http.StatusOK, room.Summary())
	})

	// Kick player from room
	engine.POST("/api/rooms/:code/kick", func(c *gin.Context) {
		room, ok := rooms.Get(c.Param("code"))
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "room not found"})
			return
		}

		var req playerActionRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		player := room.FindByPlayerID(req.PlayerID)
		if player == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "player not found"})
			return
		}

		_ = player.Send(protocol.EncodeKick("你已被管理员踢出。"))
		player.Close()
		c.JSON(http.StatusOK, gin.H{"message": "player kicked"})
	})

	// Ban player from room
	engine.POST("/api/rooms/:code/ban", func(c *gin.Context) {
		room, ok := rooms.Get(c.Param("code"))
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "room not found"})
			return
		}

		var req playerActionRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		player := room.FindByPlayerID(req.PlayerID)
		if player == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "player not found"})
			return
		}

		room.BanPlayer(req.PlayerID)
		_ = player.Send(protocol.EncodeKick("你已被管理员封禁。"))
		player.Close()
		c.JSON(http.StatusOK, gin.H{"message": "player banned"})
	})

	// Get room players
	engine.GET("/api/rooms/:code/players", func(c *gin.Context) {
		room, ok := rooms.Get(c.Param("code"))
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "room not found"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"players": room.Detail().Members})
	})

	return engine
}
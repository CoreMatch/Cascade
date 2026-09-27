package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"rwgin/internal/relay"
)

type roomCreateRequest struct {
	Code       string `json:"code"`
	Title      string `json:"title"`
	MaxPlayers int    `json:"maxPlayers"`
}

func Router(rooms *relay.Manager) *gin.Engine {
	engine := gin.New()
	engine.Use(gin.Logger(), gin.Recovery())

	engine.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	engine.GET("/api/rooms", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"rooms": rooms.List()})
	})

	engine.GET("/api/rooms/:code", func(c *gin.Context) {
		room, ok := rooms.Get(c.Param("code"))
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "room not found"})
			return
		}
		c.JSON(http.StatusOK, room.Detail())
	})

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

	return engine
}

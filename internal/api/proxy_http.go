package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"rwgin/internal/auth"
	"rwgin/internal/backend"
	"rwgin/internal/database"
)

type ProxyService interface {
	Ready() bool
	Rooms() []backend.Room
	BackendStatuses() []backend.Status
	CreateRoom(context.Context, string, any) ([]byte, int, error)
	RoomRequest(context.Context, string, string, string, any) ([]byte, int, error)
}

// ProxyRouter exposes the existing management surface as a backend facade.
func ProxyRouter(server ProxyService, authClient *auth.Client, db database.Store) *gin.Engine {
	engine := gin.New()
	engine.Use(gin.Logger(), gin.Recovery())

	engine.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	engine.GET("/readyz", func(c *gin.Context) {
		if !server.Ready() {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	})
	engine.GET("/api/backends", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"backends": server.BackendStatuses()})
	})
	engine.GET("/api/rooms", func(c *gin.Context) {
		rooms := server.Rooms()
		if rooms == nil {
			rooms = []backend.Room{}
		}
		c.JSON(http.StatusOK, gin.H{"rooms": rooms})
	})
	engine.GET("/api/rooms/:code", func(c *gin.Context) {
		forwardRoom(c, server, http.MethodGet, c.Param("code"), "", nil)
	})
	engine.POST("/api/rooms", func(c *gin.Context) {
		var req roomCreateRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		raw, status, err := server.CreateRoom(c.Request.Context(), req.Code, req)
		writeForwarded(c, raw, status, err, http.StatusCreated)
	})
	engine.DELETE("/api/rooms/:code", func(c *gin.Context) {
		raw, status, err := server.RoomRequest(c.Request.Context(), http.MethodDelete, c.Param("code"), "", nil)
		if err == nil && status == http.StatusNoContent {
			c.JSON(http.StatusOK, gin.H{"message": "room deleted"})
			return
		}
		writeForwarded(c, raw, status, err, http.StatusOK)
	})
	engine.PUT("/api/rooms/:code/settings", func(c *gin.Context) {
		var req roomSettingsRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		forwardRoom(c, server, http.MethodPut, c.Param("code"), "/settings", req)
	})
	engine.POST("/api/rooms/:code/kick", func(c *gin.Context) {
		var req playerActionRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		raw, status, err := server.RoomRequest(c.Request.Context(), http.MethodPost, c.Param("code"), "/kick", req)
		if err == nil && status == http.StatusNoContent {
			c.JSON(http.StatusOK, gin.H{"message": "player kicked"})
			return
		}
		writeForwarded(c, raw, status, err, http.StatusOK)
	})
	engine.POST("/api/rooms/:code/ban", func(c *gin.Context) {
		var req playerActionRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		raw, status, err := server.RoomRequest(c.Request.Context(), http.MethodPost, c.Param("code"), "/ban", req)
		if err == nil && status == http.StatusNoContent {
			c.JSON(http.StatusOK, gin.H{"message": "player banned"})
			return
		}
		writeForwarded(c, raw, status, err, http.StatusOK)
	})
	engine.GET("/api/rooms/:code/players", func(c *gin.Context) {
		forwardRoom(c, server, http.MethodGet, c.Param("code"), "/players", nil)
	})

	registerAccountRoutes(engine, authClient, db)
	registerInternalRoutes(engine, authClient, db)
	return engine
}

func forwardRoom(c *gin.Context, server ProxyService, method, code, suffix string, body any) {
	raw, status, err := server.RoomRequest(c.Request.Context(), method, code, suffix, body)
	writeForwarded(c, raw, status, err, http.StatusOK)
}

func writeForwarded(c *gin.Context, raw []byte, status int, err error, successStatus int) {
	if err != nil {
		if errors.Is(err, backend.ErrRoomConflict) {
			c.JSON(http.StatusConflict, gin.H{"error": "ROOM_CONFLICT"})
			return
		}
		if errors.Is(err, backend.ErrBackendDown) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "BACKEND_NOT_READY"})
			return
		}
		if apiErr, ok := errors.AsType[*backend.APIError](err); ok {
			c.JSON(apiErr.Status, gin.H{"error": apiErr.Code})
			return
		}
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "BACKEND_UNAVAILABLE"})
		return
	}
	if status == http.StatusNoContent {
		c.Status(status)
		return
	}
	if status == 0 {
		status = successStatus
	}
	if len(raw) == 0 {
		c.JSON(status, gin.H{"success": true})
		return
	}
	c.Data(status, "application/json", raw)
}

func registerAccountRoutes(engine *gin.Engine, authClient *auth.Client, db database.Store) {
	if authClient == nil {
		return
	}
	engine.POST("/api/auth/verify", func(c *gin.Context) {
		var req struct {
			Identifier string `json:"identifier" binding:"required"`
			Password   string `json:"password" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		user, err := authClient.VerifyCredentials(req.Identifier, req.Password)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		if user == nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"uuid": user.UUID, "email": user.Email, "username": user.Username})
	})
	engine.POST("/api/auth/claim", func(c *gin.Context) {
		var req struct {
			GameUUID   string `json:"gameUUID" binding:"required"`
			HAUserUUID string `json:"haUserUUID" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if db == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "database not configured"})
			return
		}
		if err := db.ClaimBinding(req.GameUUID, req.HAUserUUID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true})
	})
	engine.GET("/api/auth/status/:gameUUID", func(c *gin.Context) {
		if db == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "database not configured"})
			return
		}
		binding, err := db.GetBindingByGameUUID(c.Param("gameUUID"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if binding == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "binding not found"})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"gameUUID": binding.GameUUID, "haUserUUID": binding.HAUserUUID,
			"playerName": binding.PlayerName, "cbh": binding.CBH,
			"quickAuth": binding.QuickAuth, "lastLogin": binding.LastLogin,
		})
	})
	engine.GET("/api/auth/bindings/:haUserUUID", func(c *gin.Context) {
		if db == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "database not configured"})
			return
		}
		bindings, err := db.GetBindingsByHAUUID(c.Param("haUserUUID"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		type bindingJSON struct {
			GameUUID   string `json:"gameUUID"`
			HAUserUUID string `json:"haUserUUID"`
			PlayerName string `json:"playerName"`
			CBH        int    `json:"cbh"`
			QuickAuth  bool   `json:"quickAuth"`
			LastLogin  string `json:"lastLogin"`
		}
		result := make([]bindingJSON, len(bindings))
		for i, binding := range bindings {
			result[i] = bindingJSON{
				GameUUID: binding.GameUUID, HAUserUUID: binding.HAUserUUID,
				PlayerName: binding.PlayerName, CBH: binding.CBH,
				QuickAuth: binding.QuickAuth, LastLogin: binding.LastLogin.Format("2006-01-02T15:04:05Z"),
			}
		}
		c.JSON(http.StatusOK, gin.H{"data": result})
	})
}

func registerInternalRoutes(engine *gin.Engine, authClient *auth.Client, db database.Store) {
	internal := engine.Group("/internal")
	internal.Use(func(c *gin.Context) {
		if authClient == nil || !authClient.ValidateInternalKey(c.GetHeader("X-Internal-Key")) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid internal key"})
			return
		}
		c.Next()
	})
	internal.GET("/heartbeat", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	internal.POST("/proxy-register", func(c *gin.Context) {
		if db == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "database not configured"})
			return
		}
		var req struct {
			GameUUID   string `json:"gameUUID" binding:"required"`
			PlayerName string `json:"playerName" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := db.CreateProxyBinding(req.GameUUID, req.PlayerName); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true})
	})
}

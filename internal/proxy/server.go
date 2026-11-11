package proxy

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"cascade/internal/auth"
	"cascade/internal/backend"
	"cascade/internal/config"
	"cascade/internal/database"
	"cascade/internal/plugin"
	"cascade/internal/protocol"
	"cascade/internal/redis"
)

// authCache is the subset of the Redis auth-state operations the session
// logic needs; tests substitute a fake.
type authCache interface {
	IsAuthenticated(gameUUID string) (string, bool)
	SetAuthenticated(gameUUID, haUserUUID string, ttl time.Duration) error
}

type Server struct {
	cfg       config.Config
	logger    *log.Logger
	db        database.Store
	rdb       authCache
	auth      *auth.Client
	directory *backend.Directory
	signer    *tokenSigner
	// mcaRouter steers authenticated lobby players to their MCA formed-match
	// backend; nil when the mca config block is disabled (zero regression).
	mcaRouter matchRouter
	// plugins is the out-of-process plugin hub (Plugins.json); nil when no
	// manifest could be loaded is a startup error, an empty manifest yields a
	// hub that does nothing (zero regression).
	plugins     *plugin.Hub
	connections atomic.Int32
}

func NewServer(cfg config.Config, logger *log.Logger, db database.Store, rdb *redis.Client, authClient *auth.Client, plugins *plugin.Hub) (*Server, error) {
	directory, err := backend.NewDirectory(cfg)
	if err != nil {
		return nil, err
	}
	signer, err := loadSigner(cfg.ProxyIssuer, cfg.ProxyKeyID, cfg.ProxyPrivateKeyPath)
	if err != nil {
		return nil, err
	}
	var cache authCache
	if rdb != nil {
		cache = rdb
	}
	router, err := newMCARouter(cfg, directory, logger)
	if err != nil {
		return nil, fmt.Errorf("mca routing: %w", err)
	}
	server := &Server{
		cfg: cfg, logger: logger, db: db, rdb: cache, auth: authClient,
		directory: directory, signer: signer, mcaRouter: router, plugins: plugins,
	}
	if err := server.validate(); err != nil {
		return nil, err
	}
	return server, nil
}

// jumpPortFor returns the configured public jump port of a backend (0 when
// the backend has no jump entry). Used by /goto and by MCA assignment
// routing — both steer clients through the proxy's own jump ports so backend
// addresses stay private.
func (s *Server) jumpPortFor(backendID string) int {
	for _, be := range s.cfg.Backends {
		if be.ID == backendID {
			return be.JumpPort
		}
	}
	return 0
}

func (s *Server) Directory() *backend.Directory { return s.directory }

// PluginStatuses 返回进程外插件的状态快照（/api/plugins 的数据源）。
// 无 hub（测试形态）时返回空切片，端点形状保持 {"plugins": []}。
func (s *Server) PluginStatuses() []plugin.PluginStatus {
	if s.plugins == nil {
		return []plugin.PluginStatus{}
	}
	return s.plugins.Status()
}

func (s *Server) Run(ctx context.Context) error {
	s.directory.Start(ctx)

	// Lobby jump listeners: one extra public port per backend that declares
	// jump_port. A connection landing on a jump port routes straight to that
	// backend regardless of the Preregister room code — this is how a vanilla
	// client is steered from the lobby to a sub-server without the jump packet
	// ever carrying the backend's real address.
	for _, be := range s.cfg.Backends {
		if be.JumpPort <= 0 {
			continue
		}
		jumpAddr := fmt.Sprintf(":%d", be.JumpPort)
		jln, err := net.Listen("tcp", jumpAddr)
		if err != nil {
			return err
		}
		s.logger.Printf("proxy jump listener for backend %s on %s", be.ID, jumpAddr)
		go func(l net.Listener) {
			<-ctx.Done()
			_ = l.Close()
		}(jln)
		go func(l net.Listener, backendID string) {
			if err := s.serveGame(ctx, l, backendID); err != nil {
				s.logger.Printf("proxy jump listener for backend %s exited: %v", backendID, err)
			}
		}(jln, be.ID)
	}

	ln, err := net.Listen("tcp", s.cfg.GameListenAddr)
	if err != nil {
		return err
	}
	s.logger.Printf("proxy game listener started on %s", s.cfg.GameListenAddr)
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	return s.serveGame(ctx, ln, "")
}

// serveGame accepts game connections on ln until it closes. forcedBackendID is
// empty for the main listener (route by Preregister room code) and a backend ID
// for a jump listener (route straight to that backend).
func (s *Server) serveGame(ctx context.Context, ln net.Listener, forcedBackendID string) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if !s.acquireConnection() {
			_ = conn.Close()
			continue
		}
		go func() {
			defer s.connections.Add(-1)
			s.handleConnection(ctx, conn, forcedBackendID)
		}()
	}
}

func (s *Server) acquireConnection() bool {
	if s.cfg.MaxConnections <= 0 {
		s.connections.Add(1)
		return true
	}
	limit := int32(s.cfg.MaxConnections)
	for {
		current := s.connections.Load()
		if current >= limit {
			return false
		}
		if s.connections.CompareAndSwap(current, current+1) {
			return true
		}
	}
}

func (s *Server) Ready() bool { return s.directory.Ready() }

func (s *Server) Rooms() []backend.Room { return s.directory.SnapshotRooms() }

func (s *Server) BackendStatuses() []backend.Status { return s.directory.Statuses() }

func (s *Server) DefaultBackend() (*backend.Client, error) { return s.directory.Default() }

func (s *Server) BackendForRoom(code string) (*backend.Client, error) {
	return s.directory.RouteRoom(code)
}

func (s *Server) CreateRoom(ctx context.Context, code string, body any) ([]byte, int, error) {
	client, err := s.directory.RouteInitial(code)
	if err != nil {
		return nil, 0, err
	}
	raw, status, err := client.Request(ctx, "POST", "/rooms/create", body)
	return raw, status, err
}

func (s *Server) RoomRequest(ctx context.Context, method, code, suffix string, body any) ([]byte, int, error) {
	client, err := s.directory.RouteRoom(code)
	if err != nil {
		return nil, 0, err
	}
	path := "/rooms/" + url.PathEscape(code) + suffix
	return client.Request(ctx, method, path, body)
}

func (s *Server) handleConnection(ctx context.Context, clientConn net.Conn, forcedBackendID string) {
	defer clientConn.Close()
	if s.cfg.RegisterTimeoutSec > 0 {
		_ = clientConn.SetReadDeadline(time.Now().Add(time.Duration(s.cfg.RegisterTimeoutSec) * time.Second))
	}
	first, firstRaw, err := protocol.ReadPacketRaw(clientConn)
	if err != nil {
		return
	}
	if first.Type != protocol.TypePreregisterReceive {
		return
	}

	// Route selection. A jump-port connection (forcedBackendID != "") goes
	// straight to its backend regardless of the room code; a main-listener
	// connection routes by the Preregister room code as before.
	var upstream *backend.Client
	if forcedBackendID != "" {
		client, ok := s.directory.Client(forcedBackendID)
		if !ok || !s.directory.BackendHealthy(forcedBackendID) {
			s.sendKick(clientConn, "Backend unavailable")
			return
		}
		upstream = client
	} else {
		info, err := protocol.DecodePreregister(first.Body)
		if err != nil {
			s.sendKick(clientConn, "Invalid handshake")
			return
		}
		upstream, err = s.directory.RouteInitial(info.Query)
		if err != nil {
			s.sendKick(clientConn, "No available backend")
			return
		}
	}
	sessionID, err := backend.NewSessionID()
	if err != nil {
		return
	}
	backendConn, err := upstream.DialGame(ctx)
	if err != nil {
		s.sendKick(clientConn, "Backend unavailable")
		return
	}
	defer backendConn.Close()
	if err := backend.WritePreamble(backendConn, sessionID); err != nil {
		return
	}
	if err := writeFull(backendConn, firstRaw); err != nil {
		return
	}
	sess := &session{
		server: s, client: clientConn, backend: backendConn,
		backendClient: upstream, sessionID: sessionID,
		writeMu: &sync.Mutex{},
	}
	sess.run(ctx)
}

func (s *Server) sendKick(conn net.Conn, reason string) {
	_ = conn.SetWriteDeadline(time.Now().Add(500 * time.Millisecond))
	_ = protocol.WritePacket(conn, protocol.EncodeKick(reason))
}

func (s *Server) validate() error {
	if s.directory == nil || s.signer == nil {
		return errors.New("proxy is not initialized")
	}
	if s.cfg.MaxConnections < 0 {
		return fmt.Errorf("max_connections cannot be negative")
	}
	return nil
}

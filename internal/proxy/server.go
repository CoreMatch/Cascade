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

	"rwgin/internal/auth"
	"rwgin/internal/backend"
	"rwgin/internal/config"
	"rwgin/internal/database"
	"rwgin/internal/protocol"
	"rwgin/internal/redis"
)

type Server struct {
	cfg         config.Config
	logger      *log.Logger
	db          database.Store
	rdb         *redis.Client
	auth        *auth.Client
	directory   *backend.Directory
	signer      *tokenSigner
	connections atomic.Int32
}

func NewServer(cfg config.Config, logger *log.Logger, db database.Store, rdb *redis.Client, authClient *auth.Client) (*Server, error) {
	directory, err := backend.NewDirectory(cfg)
	if err != nil {
		return nil, err
	}
	signer, err := loadSigner(cfg.ProxyIssuer, cfg.ProxyKeyID, cfg.ProxyPrivateKeyPath)
	if err != nil {
		return nil, err
	}
	server := &Server{
		cfg: cfg, logger: logger, db: db, rdb: rdb, auth: authClient,
		directory: directory, signer: signer,
	}
	if err := server.validate(); err != nil {
		return nil, err
	}
	return server, nil
}

func (s *Server) Directory() *backend.Directory { return s.directory }

func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.GameListenAddr)
	if err != nil {
		return err
	}
	s.logger.Printf("proxy game listener started on %s", s.cfg.GameListenAddr)
	s.directory.Start(ctx)
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
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
			s.handleConnection(ctx, conn)
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

func (s *Server) handleConnection(ctx context.Context, clientConn net.Conn) {
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
	info, err := protocol.DecodePreregister(first.Body)
	if err != nil {
		s.sendKick(clientConn, "Invalid handshake")
		return
	}
	upstream, err := s.directory.RouteInitial(info.Query)
	if err != nil {
		s.sendKick(clientConn, "No available backend")
		return
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

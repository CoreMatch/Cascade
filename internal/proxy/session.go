package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"rwgin/internal/backend"
	"rwgin/internal/i18n"
	"rwgin/internal/protocol"
)

type session struct {
	server        *Server
	client        net.Conn
	backend       net.Conn
	backendClient *backend.Client
	sessionID     string
	writeMu       *sync.Mutex
	playerID      string
	playerName    string
	haUserUUID    string
	authenticated bool
	locale        string
	chatMessages  []time.Time
	bound         atomic.Bool
}

func (s *session) run(ctx context.Context) {
	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 2)
	go func() { errs <- s.clientToBackend(sessionCtx) }()
	go func() { errs <- s.backendToClient(sessionCtx) }()
	healthInterval := time.Duration(s.server.cfg.HealthIntervalSec) * time.Second
	if healthInterval <= 0 {
		healthInterval = 5 * time.Second
	}
	healthTicker := time.NewTicker(healthInterval)
	defer healthTicker.Stop()
	done := false
	for !done {
		select {
		case <-ctx.Done():
			done = true
		case <-errs:
			done = true
		case <-healthTicker.C:
			if !s.server.directory.BackendHealthy(s.backendClient.ID) {
				s.kick("Backend unavailable")
				done = true
			}
		}
	}
	cancel()
	_ = s.client.Close()
	_ = s.backend.Close()
}

func (s *session) clientToBackend(ctx context.Context) error {
	for {
		if deadline, ok := ctx.Deadline(); ok {
			_ = s.client.SetReadDeadline(deadline)
		} else if s.server.cfg.ReadTimeoutSec > 0 {
			_ = s.client.SetReadDeadline(time.Now().Add(time.Duration(s.server.cfg.ReadTimeoutSec) * time.Second))
		}
		packet, raw, err := protocol.ReadPacketRaw(s.client)
		if err != nil {
			return err
		}
		if packet.Type == protocol.TypeRegisterPlayer {
			if s.bound.Load() {
				return errors.New("duplicate player registration packet")
			}
			if err := s.bindRegisteredPlayer(ctx, packet.Body); err != nil {
				s.kick("Player identity could not be verified")
				return err
			}
			s.bound.Store(true)
		} else if !s.bound.Load() {
			return errors.New("client sent game data before identity binding")
		}
		if packet.Type == protocol.TypeChatReceive && s.handleAuthChat(ctx, packet.Body) {
			continue
		}
		if err := writeFull(s.backend, raw); err != nil {
			return err
		}
	}
}

func (s *session) backendToClient(ctx context.Context) error {
	for {
		if s.server.cfg.ReadTimeoutSec > 0 {
			_ = s.backend.SetReadDeadline(time.Now().Add(time.Duration(s.server.cfg.ReadTimeoutSec) * time.Second))
		}
		packet, raw, err := protocol.ReadPacketRaw(s.backend)
		if err != nil {
			return err
		}
		if !s.bound.Load() && packet.Type != protocol.TypePreregister {
			return errors.New("backend sent game data before identity binding")
		}
		s.writeMu.Lock()
		err = writeFull(s.client, raw)
		s.writeMu.Unlock()
		if err != nil {
			return err
		}
	}
}

func (s *session) bindRegisteredPlayer(ctx context.Context, body []byte) error {
	name, playerID, err := protocol.DecodeRegister(body)
	if err != nil {
		return fmt.Errorf("decode player registration: %w", err)
	}
	if playerID == "" {
		return errors.New("empty game player ID")
	}
	s.playerID = playerID
	s.playerName = name
	s.locale = i18n.LangEN
	if s.server.db != nil {
		if err := s.server.db.CreateProxyBinding(playerID, name); err != nil {
			return fmt.Errorf("create player binding: %w", err)
		}
		if locale, err := s.server.db.GetLang(playerID); err == nil {
			s.locale = locale
		}
	}
	s.restoreAuth()
	token, err := s.server.signer.Sign(s.backendClient.ID, s.sessionID, playerID, time.Now())
	if err != nil {
		return err
	}
	bindCtx, cancel := context.WithTimeout(ctx, time.Duration(s.server.cfg.PendingSessionTimeoutSec)*time.Second)
	defer cancel()
	if err := s.backendClient.BindIdentity(bindCtx, s.sessionID, token); err != nil {
		return fmt.Errorf("bind backend session identity: %w", err)
	}
	return nil
}

func (s *session) restoreAuth() {
	if s.server.rdb == nil || s.server.db == nil {
		return
	}
	if uuid, ok := s.server.rdb.IsAuthenticated(s.playerID); ok {
		if binding, err := s.server.db.GetBindingByGameUUID(s.playerID); err == nil && binding != nil {
			s.haUserUUID = uuid
			s.authenticated = true
			return
		}
	}
	if uuid, ok := s.server.rdb.CheckQuickAuth(s.playerID); ok {
		if binding, err := s.server.db.GetBindingByGameUUID(s.playerID); err == nil && binding != nil {
			s.haUserUUID = uuid
			s.authenticated = true
			_ = s.server.rdb.SetAuthenticated(s.playerID, uuid, 24*time.Hour)
		}
	}
}

func (s *session) handleAuthChat(ctx context.Context, body []byte) bool {
	message, err := protocol.DecodeChatReceive(body)
	if err != nil || !strings.HasPrefix(message, "/") {
		return false
	}
	parts := strings.Fields(message[1:])
	if len(parts) == 0 {
		return false
	}
	command := strings.ToLower(parts[0])
	args := parts[1:]
	switch command {
	case "login", "claim", "whoami", "lang":
	default:
		return false
	}
	rateLimit := s.server.cfg.ChatRateLimit
	if rateLimit > 0 && !s.allowCommand(rateLimit) {
		s.reply(i18n.T(s.locale, "msg.rate_limit"))
		return true
	}
	switch command {
	case "login":
		s.commandLogin(ctx, args)
	case "claim":
		s.commandClaim(ctx, args)
	case "whoami":
		s.commandWhoAmI()
	case "lang":
		s.commandLang(args)
	}
	return true
}

func (s *session) allowCommand(limit int) bool {
	now := time.Now()
	cutoff := now.Add(-time.Second)
	valid := s.chatMessages[:0]
	for _, sentAt := range s.chatMessages {
		if sentAt.After(cutoff) {
			valid = append(valid, sentAt)
		}
	}
	s.chatMessages = valid
	if len(s.chatMessages) >= limit {
		return false
	}
	s.chatMessages = append(s.chatMessages, now)
	return true
}

func (s *session) commandLogin(ctx context.Context, args []string) {
	if len(args) < 2 {
		s.reply(i18n.T(s.locale, "msg.login_usage"))
		return
	}
	if s.server.auth == nil {
		s.reply(i18n.T(s.locale, "msg.ha_not_configured"))
		return
	}
	if s.authenticated {
		s.reply(i18n.T(s.locale, "msg.already_authenticated"))
		return
	}
	user, err := s.server.auth.VerifyCredentials(args[0], args[1])
	if err != nil {
		s.reply(i18n.T(s.locale, "msg.auth_failed_service"))
		return
	}
	if user == nil {
		s.reply(i18n.T(s.locale, "msg.auth_failed_creds"))
		return
	}
	if s.server.db == nil {
		s.reply(i18n.T(s.locale, "msg.claim_bind_failed", "database unavailable"))
		return
	}
	if err := s.server.db.ClaimBinding(s.playerID, user.UUID); err != nil {
		s.reply(i18n.T(s.locale, "msg.claim_bind_failed", err.Error()))
		return
	}
	s.haUserUUID, s.authenticated = user.UUID, true
	if s.server.rdb != nil {
		_ = s.server.rdb.SetAuthenticated(s.playerID, user.UUID, 24*time.Hour)
	}
	s.reply(i18n.T(s.locale, "msg.auth_success", user.Username))
}

func (s *session) commandClaim(ctx context.Context, args []string) {
	if len(args) < 2 {
		s.reply(i18n.T(s.locale, "msg.claim_usage"))
		return
	}
	if s.server.auth == nil {
		s.reply(i18n.T(s.locale, "msg.ha_not_configured"))
		return
	}
	if s.server.db == nil {
		s.reply(i18n.T(s.locale, "msg.claim_not_found"))
		return
	}
	binding, err := s.server.db.GetBindingByGameUUID(s.playerID)
	if err != nil || binding == nil {
		s.reply(i18n.T(s.locale, "msg.claim_not_found"))
		return
	}
	if binding.CBH == 1 {
		s.reply(i18n.T(s.locale, "msg.claim_already"))
		return
	}
	password, email := args[0], args[1]
	if len(password) < 6 {
		s.reply(i18n.T(s.locale, "msg.claim_pwd_too_short"))
		return
	}
	result, err := s.server.auth.Register(email, s.playerName, password)
	if err != nil {
		s.reply(i18n.T(s.locale, "msg.claim_failed", err.Error()))
		return
	}
	if err := s.server.db.ClaimBinding(s.playerID, result.UUID); err != nil {
		s.reply(i18n.T(s.locale, "msg.claim_bind_failed", err.Error()))
		return
	}
	s.haUserUUID, s.authenticated = result.UUID, true
	if s.server.rdb != nil {
		_ = s.server.rdb.SetAuthenticated(s.playerID, result.UUID, 24*time.Hour)
	}
	s.reply(i18n.T(s.locale, "msg.claim_success"))
	s.reply(i18n.T(s.locale, "msg.claim_email", email))
	s.reply(i18n.T(s.locale, "msg.claim_remember"))
}

func (s *session) commandWhoAmI() {
	if s.server.auth == nil {
		s.reply(i18n.T(s.locale, "msg.ha_not_configured"))
		return
	}
	if s.authenticated {
		s.reply(i18n.T(s.locale, "msg.whoami_authenticated"))
		s.reply(i18n.T(s.locale, "msg.whoami_uuid", s.haUserUUID))
		return
	}
	s.reply(i18n.T(s.locale, "msg.whoami_guest"))
	s.reply(i18n.T(s.locale, "msg.whoami_hint"))
}

func (s *session) commandLang(args []string) {
	if len(args) == 0 {
		s.reply(i18n.T(s.locale, "msg.usage.lang"))
		return
	}
	switch strings.ToLower(args[0]) {
	case "en", "zh":
	default:
		s.reply(i18n.T(s.locale, "msg.usage.lang"))
		return
	}
	locale := i18n.NormalizeLang(args[0])
	s.locale = locale
	if s.server.db != nil {
		_ = s.server.db.SetLang(s.playerID, locale)
	}
	s.reply(i18n.T(s.locale, "msg.lang_changed"))
}

func (s *session) reply(message string) {
	packet, err := protocol.EncodeSystemMessage(message)
	if err != nil {
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = protocol.WritePacket(s.client, packet)
}

func (s *session) kick(reason string) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = s.client.SetWriteDeadline(time.Now().Add(500 * time.Millisecond))
	_ = protocol.WritePacket(s.client, protocol.EncodeKick(reason))
}

func writeFull(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

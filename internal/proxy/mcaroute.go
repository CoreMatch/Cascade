package proxy

// MCA assignment routing: after a player's identity is bound and they sit on
// the default (lobby) backend, query MatchCore Arena once for a formed-match
// assignment and steer the player to the assigned sub-server. This is the
// RW-side equivalent of MatchCoreArena-VelocityGate on Velocity.
//
// Division of labor:
//   - The plugin body (CoreMatch/MCA-Cascade, pure Go library) owns the MCA
//     HTTP contract, envelope parsing and the host:port -> backend mapping
//     decision. It never fails silently: an unusable MCA answers
//     ErrUnavailable, never a fabricated "no assignment".
//   - This file owns the proxy-side concerns: when to query (authenticated
//     players on the lobby only, once per session), the runtime gates
//     (backend health, jump_port, public host) and steering the client via
//     the same PACKET_RECONNECT_TO + jump-port mechanism as /goto.
//
// Every failure path degrades to "stay in the lobby" with a deterministic,
// observable outcome; a player is never stuck by this feature.

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"cascade/internal/backend"
	"cascade/internal/config"
	"cascade/internal/i18n"
	"cascade/internal/protocol"

	mcacascade "github.com/CoreMatch/MCA-Cascade"
)

// matchRouter answers "where should this authenticated player go?". A nil
// matchRouter means the feature is disabled. Tests substitute a fake.
type matchRouter interface {
	// Route queries MCA for the player's assignment and decides. A non-nil
	// error means the query itself failed (MCA unreachable/timeout/unexpected
	// response); business outcomes such as "no assignment" travel inside the
	// Decision. The two must stay distinguishable in logs.
	Route(ctx context.Context, userUUID string) (mcacascade.Decision, error)
}

// mcaRouter is the production matchRouter.
type mcaRouter struct {
	decideCfg mcacascade.Config
	client    mcacascade.Client
	timeout   time.Duration
}

func (r *mcaRouter) Route(ctx context.Context, userUUID string) (mcacascade.Decision, error) {
	queryCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	asn, err := r.client.Query(queryCtx, userUUID)
	if err != nil {
		return mcacascade.Decision{}, err
	}
	return mcacascade.Decide(r.decideCfg, asn), nil
}

// newMCARouter builds the matchRouter from configuration. It returns (nil,
// nil) when the feature is disabled — the proxy then behaves exactly as
// before. When enabled but misconfigured it returns an error: the proxy must
// refuse to start rather than silently skip routing.
func newMCARouter(cfg config.Config, directory *backend.Directory, logger *log.Logger) (matchRouter, error) {
	if !cfg.MCA.Enabled {
		return nil, nil
	}
	targets := make(map[string]string, len(cfg.MCA.Targets))
	for _, item := range cfg.MCA.Targets {
		key := mcacascade.NormalizeTarget(item.Target)
		if key == "" {
			return nil, fmt.Errorf("mca: target %q is not a valid host:port", item.Target)
		}
		backendID := strings.TrimSpace(item.Backend)
		if backendID == "" {
			return nil, fmt.Errorf("mca: target %q has an empty backend id", key)
		}
		if _, exists := directory.Client(backendID); !exists {
			return nil, fmt.Errorf("mca: target %q maps to unknown backend %q", key, backendID)
		}
		if _, dup := targets[key]; dup {
			return nil, fmt.Errorf("mca: duplicate target %q", key)
		}
		targets[key] = backendID
	}
	timeout := time.Duration(cfg.MCA.QueryTimeoutMSec) * time.Millisecond
	if timeout <= 0 {
		timeout = mcacascade.DefaultQueryTimeout
	}
	decideCfg := mcacascade.Config{
		BaseURL: cfg.MCA.BaseURL,
		Token:   cfg.MCA.Token,
		Timeout: timeout,
		Targets: targets,
	}
	if err := decideCfg.Validate(); err != nil {
		return nil, err
	}
	logger.Printf("[mca] assignment routing enabled: base=%s targets=%d timeout=%v",
		decideCfg.BaseURL, len(targets), timeout)
	return &mcaRouter{
		decideCfg: decideCfg,
		client:    mcacascade.NewHTTPClient(decideCfg.BaseURL, decideCfg.Token, timeout),
		timeout:   timeout,
	}, nil
}

// maybeRouteToMatch triggers the one-shot MCA assignment query for this
// session. Call sites: right after identity binding (when auth was restored
// from cache) and right after a successful /login or /claim. Guards:
//   - feature disabled or player not authenticated -> no query (guests have
//     no HRPAuth UUID, the assignment endpoint cannot resolve them);
//   - player not on the default backend -> no query (jump-port connections
//     and direct room-code connections are never re-routed, which also
//     prevents lobby<->target reconnect loops);
//   - once per session (atomic), so the two call sites cannot double-fire.
//
// The query runs asynchronously: a slow MCA only delays the steering, it can
// never block the player's lobby session.
func (s *session) maybeRouteToMatch(ctx context.Context) {
	router := s.server.mcaRouter
	if router == nil || !s.authenticated || s.haUserUUID == "" {
		return
	}
	if s.backendClient.ID != s.server.cfg.DefaultBackendID {
		return
	}
	if !s.routeOnce.CompareAndSwap(false, true) {
		return
	}
	// locale/playerID/sessionID are captured or immutable at this point;
	// locale can still change via /lang on the session goroutine, so snapshot it.
	go s.runMatchRoute(ctx, router, s.locale)
}

func (s *session) runMatchRoute(ctx context.Context, router matchRouter, locale string) {
	decision, err := router.Route(ctx, s.haUserUUID)
	if ctx.Err() != nil {
		// Session ended while querying: drop the decision silently.
		return
	}
	if err != nil {
		s.server.logger.Printf("[mca] WARN session %s player %s: assignment query failed, staying in lobby: %v",
			s.sessionID, s.playerID, err)
		return
	}
	s.applyMatchDecision(decision, locale)
}

// applyMatchDecision maps a decision to log lines, an optional player message
// and (only for OutcomeRoute) the actual steering. Every outcome answers the
// operational question "why was this player (not) sent where".
func (s *session) applyMatchDecision(d mcacascade.Decision, locale string) {
	logger := s.server.logger
	switch d.Outcome {
	case mcacascade.OutcomeNoAssignment:
		logger.Printf("[mca] session %s player %s: no assignment, staying in lobby",
			s.sessionID, s.playerID)
	case mcacascade.OutcomeNoRoom:
		logger.Printf("[mca] session %s player %s: no room available (reason=%s), staying in lobby",
			s.sessionID, s.playerID, d.Reason)
		s.reply(i18n.T(locale, "msg.route_no_room"))
	case mcacascade.OutcomeNoJoinTarget:
		logger.Printf("[mca] WARN session %s player %s: group %d (%s) carries no join_target, staying in lobby",
			s.sessionID, s.playerID, d.GroupID, d.MatchType)
		s.reply(i18n.T(locale, "msg.route_no_target"))
	case mcacascade.OutcomeUnknownTarget:
		logger.Printf("[mca] WARN session %s player %s: join_target %s not in mapping, staying in lobby",
			s.sessionID, s.playerID, d.JoinTarget)
		s.reply(i18n.T(locale, "msg.route_unknown_target"))
	case mcacascade.OutcomeRoute:
		s.routeToBackend(d, locale)
	default:
		logger.Printf("[mca] WARN session %s player %s: unknown outcome %v, staying in lobby",
			s.sessionID, s.playerID, d.Outcome)
	}
}

// routeToBackend performs the runtime gates the library cannot know about
// (backend health, jump_port, public host) and then steers the client with
// PACKET_RECONNECT_TO pointing at the proxy's own jump port — never the
// backend's real address. Each failed gate keeps the player in the lobby
// with a readable message.
func (s *session) routeToBackend(d mcacascade.Decision, locale string) {
	logger := s.server.logger
	backendID := d.BackendID
	if backendID == s.backendClient.ID {
		logger.Printf("[mca] session %s player %s: already on target backend %s, no jump needed",
			s.sessionID, s.playerID, backendID)
		return
	}
	if !s.server.directory.BackendHealthy(backendID) {
		logger.Printf("[mca] WARN session %s player %s: target backend %s not healthy, staying in lobby",
			s.sessionID, s.playerID, backendID)
		s.reply(i18n.T(locale, "msg.route_target_unready"))
		return
	}
	jumpPort := s.server.jumpPortFor(backendID)
	if jumpPort <= 0 {
		logger.Printf("[mca] WARN session %s player %s: target backend %s has no jump_port, staying in lobby",
			s.sessionID, s.playerID, backendID)
		s.reply(i18n.T(locale, "msg.route_no_jump"))
		return
	}
	if strings.TrimSpace(s.server.cfg.PublicGameHost) == "" {
		logger.Printf("[mca] WARN session %s player %s: public_game_host not configured, staying in lobby",
			s.sessionID, s.playerID)
		s.reply(i18n.T(locale, "msg.jump_disabled"))
		return
	}
	target := fmt.Sprintf("%s:%d", s.server.cfg.PublicGameHost, jumpPort)
	logger.Printf("[mca] session %s player %s: routing to backend %s (group=%d match=%s team=%s) via %s",
		s.sessionID, s.playerID, backendID, d.GroupID, d.MatchType, d.TeamID, target)
	s.reply(i18n.T(locale, "msg.route_ok"))
	packet := protocol.EncodeReconnect(target)
	s.writeMu.Lock()
	_ = protocol.WritePacket(s.client, packet)
	s.writeMu.Unlock()
}

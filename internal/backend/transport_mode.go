package backend

import (
	"fmt"
	"log"
	"strings"

	"cascade/internal/config"
)

// TransportMode is the transport the proxy speaks toward a backend.
//
// Contract v1.2 ("Proxy transport selection"): the proxy derives the
// transport from the trust strategy declared for the backend — plaintext
// for open/ip, TLS with a client certificate for pinned, and the safest
// available transport (TLS with a client certificate) when the declaration
// is missing or unparseable. The proxy fails closed; it never falls back to
// plaintext silently.
type TransportMode int

const (
	// TransportMTLS: TLS with a client certificate on the control plane and
	// the game dial (the pinned-grade transport).
	TransportMTLS TransportMode = iota
	// TransportPlain: plaintext TCP on the control plane and the game dial.
	// The 24-byte CASC preamble and the identity JWT flow are unchanged.
	TransportPlain
)

func (m TransportMode) String() string {
	if m == TransportPlain {
		return "plain"
	}
	return "mtls"
}

// Trust strategy declarations as named by the contract ("Backend trust
// strategies").
const (
	TrustOpen   = "open"
	TrustIP     = "ip"
	TrustPinned = "pinned"
)

// ResolveTransport derives the transport for one backend from its declared
// trust strategy and tls_mode override:
//
//   - tls_mode "plain" forces plaintext (rejected for pinned declarations:
//     the pinned strategy checks TLS client-certificate fingerprints, which
//     plaintext cannot carry);
//   - tls_mode "mtls" forces TLS with a client certificate;
//   - tls_mode "auto" (or empty) derives from trust_mode: open/ip ->
//     plaintext, pinned -> mTLS, and a missing declaration -> mTLS (the
//     safest available transport); an unparseable declaration is treated as
//     missing, with a warning, and also resolves to mTLS (fail closed).
func ResolveTransport(cfg config.BackendConfig) (TransportMode, error) {
	tlsMode := strings.ToLower(strings.TrimSpace(cfg.TLSMode))
	switch tlsMode {
	case "", "auto":
		trust, ok := parseTrustMode(cfg.TrustMode)
		if !ok {
			if strings.TrimSpace(cfg.TrustMode) != "" {
				log.Printf("[backend] %s: unparseable trust_mode %q; defaulting to the safest transport (mtls)",
					cfg.ID, cfg.TrustMode)
			}
			return TransportMTLS, nil
		}
		switch trust {
		case TrustOpen, TrustIP:
			return TransportPlain, nil
		default: // TrustPinned
			return TransportMTLS, nil
		}
	case "mtls":
		return TransportMTLS, nil
	case "plain":
		if trust, ok := parseTrustMode(cfg.TrustMode); ok && trust == TrustPinned {
			return TransportMTLS, fmt.Errorf(
				"backend %s: tls_mode \"plain\" conflicts with trust_mode \"pinned\" (pinned checks TLS client-certificate fingerprints)",
				cfg.ID)
		}
		return TransportPlain, nil
	default:
		return TransportMTLS, fmt.Errorf("backend %s: unknown tls_mode %q (want auto, mtls, or plain)", cfg.ID, cfg.TLSMode)
	}
}

// parseTrustMode normalizes a declared trust strategy. The second result is
// false when the declaration is missing or unparseable.
func parseTrustMode(s string) (string, bool) {
	switch trust := strings.ToLower(strings.TrimSpace(s)); trust {
	case TrustOpen, TrustIP, TrustPinned:
		return trust, true
	default:
		return "", false
	}
}

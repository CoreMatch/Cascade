package backend

import (
	"strings"
	"testing"

	"cascade/internal/config"
)

func TestResolveTransportDerivesFromDeclaredTrustStrategy(t *testing.T) {
	for _, test := range []struct {
		name      string
		trustMode string
		tlsMode   string
		want      TransportMode
		wantErr   string
	}{
		{name: "open declaration uses plaintext", trustMode: "open", want: TransportPlain},
		{name: "ip declaration uses plaintext", trustMode: "ip", want: TransportPlain},
		{name: "pinned declaration uses mtls", trustMode: "pinned", want: TransportMTLS},
		{name: "missing declaration defaults to the safest transport (mtls)", trustMode: "", want: TransportMTLS},
		{name: "unparseable declaration fails closed to mtls", trustMode: "wide-open", want: TransportMTLS},
		{name: "declaration parsing is case-insensitive", trustMode: "OPEN", want: TransportPlain},
		{name: "explicit mtls override wins over open declaration", trustMode: "open", tlsMode: "mtls", want: TransportMTLS},
		{name: "explicit plain override wins over missing declaration", trustMode: "", tlsMode: "plain", want: TransportPlain},
		{name: "explicit plain override wins over ip declaration", trustMode: "ip", tlsMode: "plain", want: TransportPlain},
		{name: "auto is equivalent to the empty tls_mode", trustMode: "open", tlsMode: "auto", want: TransportPlain},
		{name: "plain override conflicts with pinned declaration", trustMode: "pinned", tlsMode: "plain", wantErr: "conflicts with trust_mode \"pinned\""},
		{name: "unknown tls_mode is rejected", trustMode: "open", tlsMode: "opportunistic", wantErr: "unknown tls_mode"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := ResolveTransport(config.BackendConfig{
				ID: "backend-a", TrustMode: test.trustMode, TLSMode: test.tlsMode,
			})
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("ResolveTransport error = %v, want substring %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveTransport: %v", err)
			}
			if got != test.want {
				t.Fatalf("ResolveTransport = %s, want %s", got, test.want)
			}
		})
	}
}

func TestTransportModeString(t *testing.T) {
	if TransportMTLS.String() != "mtls" || TransportPlain.String() != "plain" {
		t.Fatalf("transport mode strings = %q/%q, want mtls/plain", TransportMTLS, TransportPlain)
	}
}

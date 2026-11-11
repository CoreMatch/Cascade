package main

// 模板自测试：不依赖 Cascade 宿主，单独验证契约机制（签名、协商入口、
// 事件分发、裁决形状）。整个模板目录可原样拷到独立仓库编译，测试随行。

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

const selfTestSecretHex = "00112233445566778899aabbccddeeff"

func selfTestSecret(t *testing.T) []byte {
	t.Helper()
	raw, err := DecodeHexSecret(selfTestSecretHex)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// signedRequest 按契约给请求打签（模拟 Cascade 侧行为）
func signedRequest(t *testing.T, secret []byte, method, url string, body []byte) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Now().Unix()
	req.Header.Set(headerTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(headerSignature, Sign(secret, ts, body))
	return req
}

func TestSignVerifyRoundtrip(t *testing.T) {
	secret := selfTestSecret(t)
	body := []byte(`{"event":"player.route"}`)
	ts := time.Now().Unix()
	sig := Sign(secret, ts, body)
	if !strings.HasPrefix(sig, signaturePrefix) {
		t.Fatalf("signature %q missing prefix", sig)
	}
	if err := Verify(secret, strconv.FormatInt(ts, 10), sig, body); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
}

func TestVerifyRejections(t *testing.T) {
	secret := selfTestSecret(t)
	body := []byte(`{"a":1}`)
	ts := time.Now().Unix()
	sig := Sign(secret, ts, body)

	cases := []struct {
		name    string
		ts, sig string
		body    []byte
	}{
		{"tampered body", strconv.FormatInt(ts, 10), sig, []byte(`{"a":2}`)},
		{"stale timestamp", strconv.FormatInt(ts-400, 10), Sign(secret, ts-400, body), body},
		{"future timestamp", strconv.FormatInt(ts+400, 10), Sign(secret, ts+400, body), body},
		{"wrong secret", strconv.FormatInt(ts, 10), Sign([]byte("ffffffffffffffff"), ts, body), body},
		{"bad timestamp format", "not-a-number", sig, body},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := Verify(secret, tc.ts, tc.sig, tc.body); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
	for _, missing := range []struct{ ts, sig string }{
		{"", sig}, {strconv.FormatInt(ts, 10), ""}, {"", ""},
	} {
		if err := Verify(secret, missing.ts, missing.sig, body); err == nil {
			t.Fatalf("missing headers accepted: %+v", missing)
		}
	}
}

func TestDecodeHexSecret(t *testing.T) {
	if _, err := DecodeHexSecret(selfTestSecretHex); err != nil {
		t.Fatalf("valid secret rejected: %v", err)
	}
	for _, bad := range []string{"", "abcd", "zz23456789abcdef0123456789abcdef", "00112233445566778899aabbccddeeff00!"} {
		if _, err := DecodeHexSecret(bad); err == nil {
			t.Fatalf("invalid secret %q accepted", bad)
		}
	}
}

func newSelfTestPlugin(t *testing.T) *Plugin {
	t.Helper()
	p := NewPlugin("self-test", "0.0.1", selfTestSecret(t))
	p.Handle("player.route", true, func(body []byte) (int, []byte) {
		return http.StatusOK, VerdictJSON(Verdict{Allowed: true})
	})
	p.Handle("player.registered", false, func(body []byte) (int, []byte) {
		return http.StatusOK, []byte(`{}`)
	})
	return p
}

func TestManifestEndpoint(t *testing.T) {
	p := newSelfTestPlugin(t)
	srv := httptest.NewServer(p)
	defer srv.Close()

	// 未签名请求必须 401
	resp, err := http.Get(srv.URL + "/v1/manifest")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unsigned manifest status = %d, want 401", resp.StatusCode)
	}

	// 签名请求 → 200 + 签名应答 + 契约字段
	req := signedRequest(t, selfTestSecret(t), http.MethodGet, srv.URL+"/v1/manifest", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("manifest status = %d", resp.StatusCode)
	}
	if err := Verify(selfTestSecret(t), resp.Header.Get(headerTimestamp), resp.Header.Get(headerSignature), body); err != nil {
		t.Fatalf("manifest response signature: %v", err)
	}
	var m struct {
		Schema   string                     `json:"schema"`
		Protocol string                     `json:"protocol"`
		ID       string                     `json:"id"`
		Version  string                     `json:"version"`
		Events   map[string]EventCapability `json:"events"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	if m.Schema != ManifestSchemaValue || m.Protocol != ProtocolID || m.ID != "self-test" {
		t.Fatalf("manifest fields: %+v", m)
	}
	if cap, ok := m.Events["player.route"]; !ok || !cap.Veto || len(cap.Versions) != 1 || cap.Versions[0] != 1 {
		t.Fatalf("player.route capability: %+v", m.Events)
	}
	if cap, ok := m.Events["player.registered"]; !ok || cap.Veto {
		t.Fatalf("player.registered capability: %+v", m.Events)
	}
}

func TestEventDispatchAndSignatures(t *testing.T) {
	p := newSelfTestPlugin(t)
	srv := httptest.NewServer(p)
	defer srv.Close()

	payload := []byte(`{"eventId":"evt_x","event":"player.route","eventVersion":1}`)

	// 未签名事件 → 401
	resp, err := http.Post(srv.URL+"/v1/events/player.route", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unsigned event status = %d, want 401", resp.StatusCode)
	}

	// 签名事件 → 处理器应答 + 签名
	req := signedRequest(t, selfTestSecret(t), http.MethodPost, srv.URL+"/v1/events/player.route", payload)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("event status = %d", resp.StatusCode)
	}
	if err := Verify(selfTestSecret(t), resp.Header.Get(headerTimestamp), resp.Header.Get(headerSignature), body); err != nil {
		t.Fatalf("event response signature: %v", err)
	}
	var v Verdict
	if err := json.Unmarshal(body, &v); err != nil || !v.Allowed {
		t.Fatalf("verdict = %+v (%v)", v, err)
	}

	// 未订阅事件 → 404（配置漂移信号）
	req = signedRequest(t, selfTestSecret(t), http.MethodPost, srv.URL+"/v1/events/game.over", payload)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unsubscribed event status = %d, want 404", resp.StatusCode)
	}

	// 错误密钥签名 → 401
	wrongKey := make([]byte, 16)
	req = signedRequest(t, wrongKey, http.MethodPost, srv.URL+"/v1/events/player.route", payload)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong-key event status = %d, want 401", resp.StatusCode)
	}
}

func TestVerdictJSONShape(t *testing.T) {
	allow := VerdictJSON(Verdict{Allowed: true})
	if string(allow) != `{"allowed":true}` {
		t.Fatalf("allow verdict = %s", allow)
	}
	deny := VerdictJSON(Verdict{Allowed: false, Reason: "维护中"})
	if !strings.Contains(string(deny), `"allowed":false`) || !strings.Contains(string(deny), "维护中") {
		t.Fatalf("deny verdict = %s", deny)
	}
}

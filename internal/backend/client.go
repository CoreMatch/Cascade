package backend

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"rwgin/internal/config"
)

type Room struct {
	Code       string       `json:"code"`
	Title      string       `json:"title"`
	MapName    string       `json:"mapName"`
	MaxPlayers int          `json:"maxPlayers"`
	Players    int          `json:"players"`
	InGame     bool         `json:"inGame"`
	OwnerID    string       `json:"ownerId"`
	OwnerName  string       `json:"ownerName"`
	AdminCount int          `json:"adminCount"`
	CreatedAt  time.Time    `json:"createdAt"`
	Members    []RoomPlayer `json:"members,omitempty"`
}

type RoomPlayer struct {
	PlayerID string `json:"playerId"`
	Name     string `json:"name"`
	Slot     int    `json:"slot"`
	IsOwner  bool   `json:"isOwner"`
	IsAdmin  bool   `json:"isAdmin"`
	IsAI     bool   `json:"isAi"`
}

type Snapshot struct {
	BackendID   string    `json:"backendId"`
	GeneratedAt time.Time `json:"generatedAt"`
	Rooms       []Room    `json:"rooms"`
}

type Health struct {
	BackendID string `json:"backendId"`
	Status    string `json:"status"`
}

type Status struct {
	ID            string    `json:"id"`
	Healthy       bool      `json:"healthy"`
	Failures      int       `json:"failures"`
	Successes     int       `json:"successes"`
	LastProbe     time.Time `json:"lastProbe,omitempty"`
	LastSnapshot  time.Time `json:"lastSnapshot,omitempty"`
	RoomCount     int       `json:"roomCount"`
	ControlURL    string    `json:"controlUrl"`
	GameAddress   string    `json:"gameAddress"`
	ConflictRooms []string  `json:"conflictRooms,omitempty"`
}

type APIError struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

type Client struct {
	ID          string
	ControlURL  string
	GameAddress string
	http        *http.Client
	tlsConfig   *tls.Config
}

func NewClient(cfg config.BackendConfig, certPath, keyPath, caPath string) (*Client, error) {
	controlURL, err := url.Parse(cfg.ControlURL)
	if err != nil || controlURL.Scheme != "https" || controlURL.Host == "" {
		return nil, errors.New("backend control_url must be an absolute https URL")
	}
	tlsConfig, err := makeTLSConfig(certPath, keyPath, caPath)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{
		TLSClientConfig: tlsConfig.Clone(),
		DialContext: (&net.Dialer{
			Timeout:   3 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:        32,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     90 * time.Second,
	}
	return &Client{
		ID:          cfg.ID,
		ControlURL:  strings.TrimRight(cfg.ControlURL, "/"),
		GameAddress: cfg.GameAddress,
		http:        &http.Client{Transport: transport, Timeout: 4 * time.Second},
		tlsConfig:   tlsConfig,
	}, nil
}

func makeTLSConfig(certPath, keyPath, caPath string) (*tls.Config, error) {
	if certPath == "" || keyPath == "" || caPath == "" {
		return nil, errors.New("backend mTLS client certificate, key, and CA paths are required")
	}
	keyInfo, err := os.Stat(keyPath)
	if err != nil {
		return nil, fmt.Errorf("stat backend client key: %w", err)
	}
	if keyInfo.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("backend client private key permissions must not allow group or other access")
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load backend client certificate: %w", err)
	}
	pem, err := os.ReadFile(caPath)
	if err != nil {
		return nil, fmt.Errorf("read backend CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil, errors.New("backend CA file contains no valid certificates")
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
		RootCAs:      roots,
	}, nil
}

func (c *Client) DialGame(ctx context.Context) (net.Conn, error) {
	if c.GameAddress == "" {
		return nil, errors.New("backend game address is empty")
	}
	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second},
		Config:    c.tlsConfig.Clone(),
	}
	return dialer.DialContext(ctx, "tcp", c.GameAddress)
}

func (c *Client) Health(ctx context.Context) error {
	var result Health
	if err := c.do(ctx, http.MethodGet, "/health", nil, &result, true); err != nil {
		return err
	}
	if result.Status != "ready" || result.BackendID != c.ID {
		return errors.New("backend is not ready or returned a mismatched backend ID")
	}
	return nil
}

func (c *Client) Rooms(ctx context.Context) (Snapshot, error) {
	var snapshot Snapshot
	err := c.do(ctx, http.MethodGet, "/rooms", nil, &snapshot, true)
	if err != nil {
		return Snapshot{}, err
	}
	if snapshot.BackendID != c.ID {
		return Snapshot{}, errors.New("room snapshot backend ID mismatch")
	}
	if snapshot.GeneratedAt.IsZero() || snapshot.Rooms == nil {
		return Snapshot{}, errors.New("room snapshot is missing generatedAt or rooms")
	}
	return snapshot, nil
}

func (c *Client) BindIdentity(ctx context.Context, sessionID, token string) error {
	body := map[string]string{"token": token}
	return c.do(ctx, http.MethodPut, "/sessions/"+url.PathEscape(sessionID)+"/identity", body, nil, true)
}

func (c *Client) Request(ctx context.Context, method, path string, body any) (json.RawMessage, int, error) {
	var raw json.RawMessage
	retry := method == http.MethodGet || method == http.MethodPut || method == http.MethodDelete
	status, err := c.doStatus(ctx, method, path, body, &raw, retry)
	return raw, status, err
}

func (c *Client) do(ctx context.Context, method, path string, body, out any, retry bool) error {
	_, err := c.doStatus(ctx, method, path, body, out, retry)
	return err
}

func (c *Client) doStatus(ctx context.Context, method, path string, body, out any, retry bool) (int, error) {
	payload, err := json.Marshal(body)
	if body == nil {
		payload = nil
	}
	if err != nil {
		return 0, err
	}
	attempts := 1
	if retry {
		attempts = 3
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, c.ControlURL+"/v1"+path, bytes.NewReader(payload))
		if err != nil {
			return 0, err
		}
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
		} else {
			responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
			_ = resp.Body.Close()
			if readErr != nil {
				lastErr = readErr
			} else if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				var apiErr APIError
				apiErr.Status = resp.StatusCode
				if json.Unmarshal(responseBody, &apiErr) != nil {
					apiErr.Code = "BACKEND_ERROR"
					apiErr.Message = strings.TrimSpace(string(responseBody))
				}
				if apiErr.Code == "" {
					apiErr.Code = "BACKEND_ERROR"
				}
				return resp.StatusCode, &apiErr
			} else {
				if out != nil && len(responseBody) != 0 {
					if err := json.Unmarshal(responseBody, out); err != nil {
						return resp.StatusCode, fmt.Errorf("decode backend response: %w", err)
					}
				}
				return resp.StatusCode, nil
			}
		}
		if attempt+1 < attempts {
			delay := time.Duration(attempt+1) * 100 * time.Millisecond
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return 0, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return 0, fmt.Errorf("backend request failed after %d attempt(s): %w", attempts, lastErr)
}

func (c *Client) BaseURL() string { return c.ControlURL }

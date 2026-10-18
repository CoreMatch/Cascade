package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Client is an HTTP client for HRPAuth core API using OAuth2 client_credentials.
type Client struct {
	baseURL      string
	clientID     string
	clientSecret string
	http         *http.Client

	tokenMu    sync.Mutex
	token      string
	tokenExpiry time.Time
}

// New creates a new HRPAuth core client.
func New(baseURL, clientID, clientSecret string) *Client {
	return &Client{
		baseURL:      strings.TrimRight(baseURL, "/"),
		clientID:     clientID,
		clientSecret: clientSecret,
		http: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// UserInfo holds the verified user information from HA core.
type UserInfo struct {
	UUID     string `json:"uuid"`
	Email    string `json:"email"`
	Username string `json:"username"`
}

// tokenResponse is the OAuth2 token endpoint response.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	Scope       string `json:"scope"`
}

// getToken returns a valid access_token, refreshing if needed.
func (c *Client) getToken() (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()

	if c.token != "" && time.Now().Before(c.tokenExpiry) {
		return c.token, nil
	}

	data := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
	}

	resp, err := c.http.PostForm(c.baseURL+"/oauth/token", data)
	if err != nil {
		return "", fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("token request failed (status %d): %s", resp.StatusCode, string(b))
	}

	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}

	c.token = tr.AccessToken
	// Refresh a bit early to avoid edge cases
	c.tokenExpiry = time.Now().Add(time.Duration(tr.ExpiresIn)*time.Second - 30*time.Second)

	return c.token, nil
}

// VerifyCredentials calls POST /internal/verify-credentials on HA core.
// Returns the user info on success, nil on invalid credentials.
func (c *Client) VerifyCredentials(identifier, password string) (*UserInfo, error) {
	token, err := c.getToken()
	if err != nil {
		return nil, fmt.Errorf("get token: %w", err)
	}

	body := map[string]string{
		"identifier": identifier,
		"password":   password,
	}

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/internal/verify-credentials", &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var user UserInfo
		if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
			return nil, fmt.Errorf("decode response: %w", err)
		}
		return &user, nil
	case http.StatusUnauthorized:
		return nil, nil // Invalid credentials
	default:
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(b))
	}
}
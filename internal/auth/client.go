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
	serviceName  string
	http         *http.Client

	tokenMu    sync.Mutex
	token      string
	tokenExpiry time.Time

	internalKeyMu sync.RWMutex
	internalKey   string
}

// New creates a new HRPAuth core client.
// serviceName is used when registering with Core's /services/auth-key endpoint.
func New(baseURL, clientID, clientSecret, serviceName string) *Client {
	return &Client{
		baseURL:      strings.TrimRight(baseURL, "/"),
		clientID:     clientID,
		clientSecret: clientSecret,
		serviceName:  serviceName,
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

// RegisterResult holds the response from POST /register on HA core.
type RegisterResult struct {
	Success bool   `json:"success"`
	UID     int    `json:"uid"`
	UUID    string `json:"uuid"`
	Message string `json:"message"`
}

// Register calls POST /register on HA core to create a new user account.
func (c *Client) Register(email, username, password string) (*RegisterResult, error) {
	token, err := c.getToken()
	if err != nil {
		return nil, fmt.Errorf("get token: %w", err)
	}

	body := map[string]string{
		"email":    email,
		"username": username,
		"password": password,
	}

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/register", &buf)
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

	var result RegisterResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return &result, fmt.Errorf("register failed (status %d): %s", resp.StatusCode, result.Message)
	}

	return &result, nil
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

// FetchInternalKey requests a temporary internal key from HRPAuth Core
// via POST /services/auth-key. The key is stored in memory and used to
// validate inbound requests from Core (X-Internal-Key header).
func (c *Client) FetchInternalKey() error {
	token, err := c.getToken()
	if err != nil {
		return fmt.Errorf("get token: %w", err)
	}

	body := map[string]string{
		"name": c.serviceName,
	}

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/services/auth-key", &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("auth-key request failed (status %d): %s", resp.StatusCode, string(b))
	}

	var result struct {
		InternalKey string `json:"internal_key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	c.internalKeyMu.Lock()
	c.internalKey = result.InternalKey
	c.internalKeyMu.Unlock()

	return nil
}

// InternalKey returns the currently stored internal key.
func (c *Client) InternalKey() string {
	c.internalKeyMu.RLock()
	defer c.internalKeyMu.RUnlock()
	return c.internalKey
}

// ValidateInternalKey checks if the given key matches the stored internal key.
// Returns true if valid, false otherwise. Always returns false if no key is set.
func (c *Client) ValidateInternalKey(key string) bool {
	c.internalKeyMu.RLock()
	defer c.internalKeyMu.RUnlock()
	return c.internalKey != "" && c.internalKey == key
}
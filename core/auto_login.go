package core

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"bnf_go_engine/config"
)

// AutoLogin performs headless Zerodha OAuth login.
// Port of Python core/auto_login.py
type AutoLogin struct {
	APIKey      string
	APISecret   string
	UserID      string
	Password    string
	TOTPSecret  string
	RedirectURL string
}

func NewAutoLogin() *AutoLogin {
	return &AutoLogin{
		APIKey:      config.KiteAPIKey,
		APISecret:   config.KiteAPISecret,
		UserID:      config.ZerodhaUserID,
		Password:    config.ZerodhaPassword,
		TOTPSecret:  config.ZerodhaTOTPSecret,
		RedirectURL: config.KiteRedirectURL,
	}
}

func (a *AutoLogin) Validate() error {
	missing := []string{}
	if a.APIKey == "" {
		missing = append(missing, "KITE_API_KEY")
	}
	if a.APISecret == "" {
		missing = append(missing, "KITE_API_SECRET")
	}
	if a.UserID == "" {
		missing = append(missing, "ZERODHA_USER_ID")
	}
	if a.Password == "" {
		missing = append(missing, "ZERODHA_PASSWORD")
	}
	if a.TOTPSecret == "" {
		missing = append(missing, "ZERODHA_TOTP_SECRET")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing env keys: %v", missing)
	}
	return nil
}

// GenerateTOTP generates a 6-digit TOTP code (RFC 6238)
func (a *AutoLogin) GenerateTOTP() (string, error) {
	if a.TOTPSecret == "" {
		return "", fmt.Errorf("TOTP secret is empty")
	}

	// Decode base32 secret
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(
		strings.ToUpper(strings.TrimRight(a.TOTPSecret, "=")))
	if err != nil {
		return "", fmt.Errorf("invalid TOTP secret: %v", err)
	}

	// Wait for fresh TOTP window (>5s remaining)
	remaining := 30 - (int(time.Now().Unix()) % 30)
	if remaining < 5 {
		time.Sleep(time.Duration(remaining+1) * time.Second)
	}

	// Compute HOTP
	counter := uint64(time.Now().Unix()) / 30
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, counter)

	mac := hmac.New(sha1.New, secret)
	mac.Write(buf)
	hash := mac.Sum(nil)

	offset := hash[len(hash)-1] & 0x0f
	code := int64(binary.BigEndian.Uint32(hash[offset:offset+4]) & 0x7fffffff)
	otp := code % int64(math.Pow10(6))

	return fmt.Sprintf("%06d", otp), nil
}

// Login performs the full Zerodha OAuth flow
func (a *AutoLogin) Login() (string, error) {
	if err := a.Validate(); err != nil {
		return "", err
	}

	totp, err := a.GenerateTOTP()
	if err != nil {
		return "", fmt.Errorf("TOTP generation failed: %v", err)
	}
	log.Printf("[AutoLogin] TOTP generated: %s", totp[:2]+"****")

	// Create HTTP client that doesn't follow redirects
	jar := &cookieJar{cookies: make(map[string][]*http.Cookie)}
	client := &http.Client{
		Timeout: 30 * time.Second,
		Jar:     jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// Step 1: POST login credentials
	loginData := url.Values{
		"user_id":  {a.UserID},
		"password": {a.Password},
	}
	resp, err := client.PostForm("https://kite.zerodha.com/api/login", loginData)
	if err != nil {
		return "", fmt.Errorf("login POST failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	var loginResult struct {
		Status string `json:"status"`
		Data   struct {
			RequestID string `json:"request_id"`
		} `json:"data"`
	}
	json.Unmarshal(body, &loginResult)

	if loginResult.Data.RequestID == "" {
		return "", fmt.Errorf("no request_id from login: %s", string(body))
	}
	log.Printf("[AutoLogin] Login step 1 OK — request_id: %s", loginResult.Data.RequestID[:8])

	// Step 2: POST TOTP
	totpData := url.Values{
		"user_id":     {a.UserID},
		"request_id":  {loginResult.Data.RequestID},
		"twofa_value": {totp},
		"twofa_type":  {"totp"},
		"skip_session": {""},
	}
	resp, err = client.PostForm("https://kite.zerodha.com/api/twofa", totpData)
	if err != nil {
		return "", fmt.Errorf("TOTP POST failed: %v", err)
	}
	resp.Body.Close()

	// Step 3: Navigate to OAuth URL to get request_token redirect
	oauthURL := fmt.Sprintf("https://kite.zerodha.com/connect/login?v=3&api_key=%s", a.APIKey)
	resp, err = client.Get(oauthURL)
	if err != nil {
		return "", fmt.Errorf("OAuth redirect failed: %v", err)
	}
	resp.Body.Close()

	location := resp.Header.Get("Location")
	if !strings.Contains(location, "request_token=") {
		// Try following one more redirect
		if location != "" {
			resp, err = client.Get(location)
			if err == nil {
				location = resp.Header.Get("Location")
				resp.Body.Close()
			}
		}
	}

	if !strings.Contains(location, "request_token=") {
		return "", fmt.Errorf("no request_token in redirect: %s", location)
	}

	requestToken := ""
	parts := strings.Split(location, "request_token=")
	if len(parts) > 1 {
		requestToken = strings.Split(parts[1], "&")[0]
	}
	log.Printf("[AutoLogin] Got request_token: %s...", requestToken[:8])

	// Step 4: Exchange request_token → access_token
	checksum := fmt.Sprintf("%x", sha256.Sum256([]byte(a.APIKey+requestToken+a.APISecret)))

	sessionData := url.Values{
		"api_key":       {a.APIKey},
		"request_token": {requestToken},
		"checksum":      {checksum},
	}
	resp, err = http.PostForm("https://api.kite.trade/session/token", sessionData)
	if err != nil {
		return "", fmt.Errorf("session exchange failed: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()

	var sessionResult struct {
		Status string `json:"status"`
		Data   struct {
			AccessToken string `json:"access_token"`
		} `json:"data"`
		Message string `json:"message"`
	}
	json.Unmarshal(body, &sessionResult)

	if sessionResult.Data.AccessToken == "" {
		return "", fmt.Errorf("empty access_token: %s", sessionResult.Message)
	}

	// Step 5: Persist to .env
	a.updateEnvFile(sessionResult.Data.AccessToken)
	config.KiteAccessToken = sessionResult.Data.AccessToken
	os.Setenv("KITE_ACCESS_TOKEN", sessionResult.Data.AccessToken)

	log.Printf("[AutoLogin] ✅ Token refreshed: %s...", sessionResult.Data.AccessToken[:10])
	return sessionResult.Data.AccessToken, nil
}

// Run is called by the scheduler at 8:30 AM
func (a *AutoLogin) Run() bool {
	token, err := a.Login()
	if err != nil {
		log.Printf("[AutoLogin] FAILED: %v", err)
		return false
	}
	log.Printf("[AutoLogin] Success — %s...", token[:10])
	return true
}

func (a *AutoLogin) updateEnvFile(accessToken string) {
	envPaths := []string{"./.env"}
	bnfRoot := os.Getenv("ENGINE_ROOT")
	if bnfRoot != "" {
		envPaths = append([]string{bnfRoot + "/.env"}, envPaths...)
	}

	for _, path := range envPaths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		lines := strings.Split(string(data), "\n")
		updated := false
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "KITE_ACCESS_TOKEN") {
				lines[i] = fmt.Sprintf("KITE_ACCESS_TOKEN=%s", accessToken)
				updated = true
				break
			}
		}
		if !updated {
			lines = append(lines, fmt.Sprintf("KITE_ACCESS_TOKEN=%s", accessToken))
		}

		os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0644)
		log.Printf("[AutoLogin] Updated %s with new access_token", path)
		return
	}
}

// Simple cookie jar implementation
type cookieJar struct {
	cookies map[string][]*http.Cookie
}

func (j *cookieJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	j.cookies[u.Host] = append(j.cookies[u.Host], cookies...)
}

func (j *cookieJar) Cookies(u *url.URL) []*http.Cookie {
	return j.cookies[u.Host]
}

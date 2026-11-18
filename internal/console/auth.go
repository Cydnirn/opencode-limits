package console

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/cydnirn/opencode-limits/internal/config"
)

// ClientID is the public OAuth client the opencode CLI uses for device grants.
const ClientID = "opencode-cli"

// ErrNoCredential means no credential file exists, so a device login is needed.
var ErrNoCredential = fmt.Errorf("no opencode-console credential found")

// Store reads and writes the credential file.
type Store struct {
	Path string
	// raw is the credential document as read, so keys this tool does not model
	// survive a rewrite. Without this, persisting a refreshed token would strip
	// fields belonging to other tools.
	raw map[string]json.RawMessage

	mu sync.Mutex
}

// Load reads and parses the credential file.
func (s *Store) Load() (Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *Store) loadLocked() (Credential, error) {
	b, err := os.ReadFile(s.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return Credential{}, ErrNoCredential
		}
		return Credential{}, fmt.Errorf("read credential %s: %w", s.Path, err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return Credential{}, fmt.Errorf("parse credential %s: %w", s.Path, err)
	}
	var c Credential
	if err := json.Unmarshal(b, &c); err != nil {
		return Credential{}, fmt.Errorf("parse credential %s: %w", s.Path, err)
	}
	s.raw = raw
	return c, nil
}

// Save writes c back into the credential file, preserving every key it does not
// model. The write is atomic (temp file + rename) and mode 0600.
func (s *Store) Save(c Credential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(c)
}

func (s *Store) saveLocked(c Credential) error {
	doc := map[string]json.RawMessage{}
	for k, v := range s.raw {
		doc[k] = v
	}
	set := func(key, val string) {
		b, _ := json.Marshal(val)
		doc[key] = b
	}
	set("type", c.Type)
	set("server", c.Server)
	set("access", c.Access)
	set("refresh", c.Refresh)
	set("accountID", c.AccountID)
	set("email", c.Email)
	set("orgID", c.OrgID)
	set("orgName", c.OrgName)
	exp, _ := json.Marshal(c.Expires)
	doc["expires"] = exp

	buf, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode credential: %w", err)
	}
	buf = append(buf, '\n')
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return fmt.Errorf("create credential dir: %w", err)
	}
	if err := config.WriteFileAtomic(s.Path, buf); err != nil {
		return fmt.Errorf("persist credential: %w", err)
	}
	if err := os.Chmod(s.Path, 0o600); err != nil {
		return fmt.Errorf("chmod credential: %w", err)
	}
	s.raw = doc
	return nil
}

// wireTokenResp is the /auth/device/token response for either grant type.
type wireTokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	OrgID        string `json:"org_id"`
	Error        string `json:"error"`
	Description  string `json:"error_description"`
}

// wireDeviceCode is the /auth/device/code response.
type wireDeviceCode struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int64  `json:"expires_in"`
	Interval                int64  `json:"interval"`
}

// AuthError reports a refresh or device-token failure with the server's code.
type AuthError struct {
	Status      int
	Code        string
	Description string
}

func (e *AuthError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("authentication failed (%s): %s", e.Code, e.Description)
	}
	return fmt.Sprintf("authentication failed: HTTP %d", e.Status)
}

// IsInvalidGrant reports whether the refresh token is dead, meaning a fresh
// device login is the only way forward.
func (e *AuthError) IsInvalidGrant() bool { return e.Code == "invalid_grant" }

// Refresher exchanges a refresh token for a new access token.
//
// Refresh tokens rotate: a token is single-use and the response carries its
// replacement. Callers MUST persist the returned credential before making any
// further request, or the next refresh will be rejected with invalid_grant.
type Refresher struct {
	HTTP   *http.Client
	Server string
}

// Refresh performs the refresh grant.
func (r Refresher) Refresh(c Credential) (Credential, error) {
	server := r.Server
	if server == "" {
		server = c.Server
	}
	if server == "" {
		server = config.DefaultServer
	}
	body, _ := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": c.Refresh,
		"client_id":     ClientID,
	})
	hc := r.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequest(http.MethodPost, trimSlash(server)+"/auth/device/token", bytes.NewReader(body))
	if err != nil {
		return c, fmt.Errorf("build refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := hc.Do(req)
	if err != nil {
		return c, fmt.Errorf("refresh request: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return c, fmt.Errorf("read refresh response: %w", err)
	}

	var wr wireTokenResp
	_ = json.Unmarshal(raw, &wr)

	if resp.StatusCode != http.StatusOK || wr.AccessToken == "" {
		ae := &AuthError{Status: resp.StatusCode, Code: wr.Error, Description: wr.Description}
		var generic struct {
			Tag         string `json:"_tag"`
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		if json.Unmarshal(raw, &generic) == nil {
			if ae.Code == "" {
				ae.Code = generic.Error
			}
			if ae.Description == "" {
				ae.Description = generic.Description
			}
			if ae.Description == "" {
				ae.Description = generic.Tag
			}
		}
		if ae.Description == "" {
			ae.Description = truncate(string(raw), 200)
		}
		return c, ae
	}

	out := c
	out.Access = wr.AccessToken
	if wr.RefreshToken != "" {
		out.Refresh = wr.RefreshToken
	}
	if wr.ExpiresIn > 0 {
		out.Expires = time.Now().Add(time.Duration(wr.ExpiresIn) * time.Second).UnixMilli()
	}
	if wr.OrgID != "" {
		out.OrgID = wr.OrgID
	}
	if out.Server == "" {
		out.Server = trimSlash(server)
	}
	return out, nil
}

// —— device code login ——

// DeviceCode is the user-facing half of a device login.
type DeviceCode struct {
	DeviceCode string
	UserCode   string
	VerifyURL  string
	Interval   time.Duration
	ExpiresIn  time.Duration
}

// BeginDeviceLogin starts a device authorization grant.
func BeginDeviceLogin(hc *http.Client, server string) (DeviceCode, error) {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	body, _ := json.Marshal(map[string]any{
		"client_id":          ClientID,
		"supports_org_scope": true,
	})
	req, err := http.NewRequest(http.MethodPost, trimSlash(server)+"/auth/device/code", bytes.NewReader(body))
	if err != nil {
		return DeviceCode{}, fmt.Errorf("build device code request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := hc.Do(req)
	if err != nil {
		return DeviceCode{}, fmt.Errorf("device code request: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return DeviceCode{}, fmt.Errorf("read device code response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return DeviceCode{}, &AuthError{Status: resp.StatusCode, Description: truncate(string(raw), 200)}
	}
	var w wireDeviceCode
	if err := json.Unmarshal(raw, &w); err != nil {
		return DeviceCode{}, fmt.Errorf("decode device code: %w", err)
	}
	if w.DeviceCode == "" || w.UserCode == "" {
		return DeviceCode{}, fmt.Errorf("device code response missing fields: %s", truncate(string(raw), 200))
	}
	interval := time.Duration(w.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return DeviceCode{
		DeviceCode: w.DeviceCode,
		UserCode:   w.UserCode,
		VerifyURL:  w.VerificationURIComplete,
		Interval:   interval,
		ExpiresIn:  time.Duration(w.ExpiresIn) * time.Second,
	}, nil
}

// PollDeviceToken polls until the user approves or the code expires. It honours
// the server's interval and backs off on slow_down. onTick, when non-nil, is
// called before each poll with the time left.
func PollDeviceToken(hc *http.Client, server string, dc DeviceCode, onTick func(time.Duration)) (Credential, error) {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	deadline := time.Now().Add(dc.ExpiresIn)
	interval := dc.Interval
	for {
		if dc.ExpiresIn > 0 && time.Now().After(deadline) {
			return Credential{}, fmt.Errorf("device code expired before approval")
		}
		time.Sleep(interval)
		if onTick != nil {
			onTick(time.Until(deadline))
		}
		body, _ := json.Marshal(map[string]string{
			"grant_type":  "urn:ietf:params:oauth:grant-type:device_code",
			"device_code": dc.DeviceCode,
			"client_id":   ClientID,
		})
		req, err := http.NewRequest(http.MethodPost, trimSlash(server)+"/auth/device/token", bytes.NewReader(body))
		if err != nil {
			return Credential{}, fmt.Errorf("build device token request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", userAgent)
		resp, err := hc.Do(req)
		if err != nil {
			return Credential{}, fmt.Errorf("device token request: %w", err)
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()

		var wr wireTokenResp
		_ = json.Unmarshal(raw, &wr)
		if wr.AccessToken != "" {
			return Credential{
				Type:    "oauth",
				Server:  trimSlash(server),
				Access:  wr.AccessToken,
				Refresh: wr.RefreshToken,
				Expires: time.Now().Add(time.Duration(wr.ExpiresIn) * time.Second).UnixMilli(),
				OrgID:   wr.OrgID,
			}, nil
		}
		switch wr.Error {
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 5 * time.Second
			continue
		default:
			return Credential{}, &AuthError{Status: resp.StatusCode, Code: wr.Error, Description: truncate(string(raw), 200)}
		}
	}
}

// DescribeAccount fills account identity for a freshly logged-in credential by
// calling /api/user and /api/orgs, matching what the opencode-console CLI
// stores so other tools see a complete file.
func DescribeAccount(hc *http.Client, cred Credential) (Credential, error) {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	get := func(path string) ([]byte, error) {
		req, err := http.NewRequest(http.MethodGet, trimSlash(cred.Server)+path, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+cred.Access)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", userAgent)
		resp, err := hc.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("GET %s: HTTP %d", path, resp.StatusCode)
		}
		return b, nil
	}

	if b, err := get("/api/user"); err == nil {
		var u struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		}
		if json.Unmarshal(b, &u) == nil {
			cred.AccountID = u.ID
			cred.Email = u.Email
		}
	}
	if b, err := get("/api/orgs"); err == nil {
		var orgs []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if json.Unmarshal(b, &orgs) == nil && len(orgs) > 0 {
			pick := orgs[0]
			for _, o := range orgs {
				if o.ID == cred.OrgID {
					pick = o
					break
				}
			}
			cred.OrgID = pick.ID
			cred.OrgName = pick.Name
		}
	}
	return cred, nil
}

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

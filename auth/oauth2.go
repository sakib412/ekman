package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// OAuth2Config describes a standards-compliant OAuth 2 provider.
type OAuth2Config struct {
	Name         string
	ClientID     string
	ClientSecret string
	AuthURL      string
	TokenURL     string
	Scopes       []string
	// Identity fetches the signed-in user's identity with the access token. Provider is
	// filled in by the caller.
	Identity func(ctx context.Context, client *http.Client, accessToken string) (Identity, error)
	// HTTPClient is used for the token and identity requests; default has a 10s timeout.
	HTTPClient *http.Client
}

// OAuth2 builds a Provider from cfg, for providers beyond Google and GitHub.
func OAuth2(cfg OAuth2Config) Provider {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &oauth2Provider{cfg: cfg}
}

type oauth2Provider struct {
	cfg OAuth2Config
}

func (p *oauth2Provider) Name() string { return p.cfg.Name }

// Challenge returns the PKCE S256 code challenge for verifier (RFC 7636).
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (p *oauth2Provider) AuthCodeURL(state, verifier, redirectURL string) string {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {p.cfg.ClientID},
		"redirect_uri":          {redirectURL},
		"state":                 {state},
		"code_challenge":        {Challenge(verifier)},
		"code_challenge_method": {"S256"},
	}
	if len(p.cfg.Scopes) > 0 {
		q.Set("scope", strings.Join(p.cfg.Scopes, " "))
	}
	sep := "?"
	if strings.Contains(p.cfg.AuthURL, "?") {
		sep = "&"
	}
	return p.cfg.AuthURL + sep + q.Encode()
}

type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func (p *oauth2Provider) Exchange(ctx context.Context, code, verifier, redirectURL string) (Identity, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURL},
		"client_id":     {p.cfg.ClientID},
		"client_secret": {p.cfg.ClientSecret},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Identity{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	var tok tokenResponse
	status, err := doJSON(p.cfg.HTTPClient, req, &tok)
	if err != nil {
		return Identity{}, fmt.Errorf("auth: %s token: %w", p.cfg.Name, err)
	}
	// GitHub reports failures as 200 with an error field, so check both.
	if tok.Error != "" || status/100 != 2 {
		return Identity{}, fmt.Errorf("auth: %s token: status %d: %s %s", p.cfg.Name, status, tok.Error, tok.ErrorDescription)
	}
	if tok.AccessToken == "" {
		return Identity{}, fmt.Errorf("auth: %s token: no access_token in response", p.cfg.Name)
	}
	if p.cfg.Identity == nil {
		return Identity{}, fmt.Errorf("auth: %s: no Identity func configured", p.cfg.Name)
	}
	id, err := p.cfg.Identity(ctx, p.cfg.HTTPClient, tok.AccessToken)
	if err != nil {
		return Identity{}, fmt.Errorf("auth: %s identity: %w", p.cfg.Name, err)
	}
	if id.Subject == "" {
		return Identity{}, fmt.Errorf("auth: %s identity: empty subject", p.cfg.Name)
	}
	id.Provider = p.cfg.Name
	return id, nil
}

// GetJSON performs an authenticated GET and decodes a 2xx JSON response into dst. It is a
// helper for OAuth2Config.Identity implementations.
func GetJSON(ctx context.Context, client *http.Client, accessToken, url string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	status, err := doJSON(client, req, dst)
	if err != nil {
		return err
	}
	if status/100 != 2 {
		return fmt.Errorf("GET %s: status %d", url, status)
	}
	return nil
}

func doJSON(client *http.Client, req *http.Request, dst any) (int, error) {
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if err := json.Unmarshal(body, dst); err != nil {
		if resp.StatusCode/100 != 2 {
			return resp.StatusCode, nil
		}
		return resp.StatusCode, errors.New("invalid JSON response")
	}
	return resp.StatusCode, nil
}

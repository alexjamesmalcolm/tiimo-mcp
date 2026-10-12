// Package oauth implements a deliberately single-owner OAuth authorization server.
// It is not an identity provider for shared/multiuser deployments. Access and
// refresh token records persist in SQLite; authorization requests stay in memory.
package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Config struct {
	PublicURL, ClientID, ClientSecret, Password string
	RedirectURIs                                []string
	DBPath                                      string
}
type grant struct {
	Redirect, State, Challenge, Resource string
	Expiry                               time.Time
}
type Provider struct {
	cfg            Config
	mu             sync.Mutex
	pending, codes map[string]grant
	store          *tokenStore
	window         time.Time
	attempts       int
}

func New(c Config) (*Provider, error) {
	u, e := url.Parse(c.PublicURL)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("PUBLIC_URL must be the HTTPS origin of the Railway service")
	}
	c.PublicURL = strings.TrimRight(c.PublicURL, "/")
	if c.ClientID == "" || len(c.ClientSecret) < 32 || len(c.Password) < 32 {
		return nil, errors.New("set OAUTH_CLIENT_ID, OAUTH_CLIENT_SECRET and MCP_LOGIN_PASSWORD (secrets at least 32 characters)")
	}
	if len(c.RedirectURIs) == 0 {
		c.RedirectURIs = []string{"https://chatgpt.com/connector_platform_oauth_redirect"}
	}
	for _, s := range c.RedirectURIs {
		u, e := url.Parse(s)
		if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
			return nil, errors.New("redirect URIs must be exact HTTPS URLs")
		}
	}
	binding, _ := json.Marshal([]string{c.PublicURL, c.ClientID, c.ClientSecret, c.Password})
	store, err := openStore(c.DBPath, digest(string(binding)))
	if err != nil {
		return nil, errors.New("cannot open OAuth SQLite database; check AUTH_DB_PATH and directory permissions")
	}
	return &Provider{cfg: c, pending: map[string]grant{}, codes: map[string]grant{}, store: store}, nil
}
func (p *Provider) Close() error { return p.store.close() }

func random() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func equal(a, b string) bool {
	ha := sha256.Sum256([]byte(a))
	hb := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}
func digest(s string) string {
	h := sha256.Sum256([]byte(s))
	return base64.RawURLEncoding.EncodeToString(h[:])
}
func jsonResponse(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func failure(w http.ResponseWriter, code string) {
	jsonResponse(w, 400, map[string]string{"error": code})
}
func (p *Provider) clean() {
	now := time.Now()
	for k, v := range p.pending {
		if now.After(v.Expiry) {
			delete(p.pending, k)
		}
	}
	for k, v := range p.codes {
		if now.After(v.Expiry) {
			delete(p.codes, k)
		}
	}

}
func (p *Provider) limited() bool {
	if time.Since(p.window) > time.Minute {
		p.window = time.Now()
		p.attempts = 0
	}
	p.attempts++
	return p.attempts > 30
}
func (p *Provider) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", p.resource)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", p.resource)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", p.discovery)
	mux.HandleFunc("GET /oauth/authorize", p.authorize)
	mux.HandleFunc("POST /oauth/approve", p.approve)
	mux.HandleFunc("POST /oauth/token", p.exchange)
}
func (p *Provider) resource(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, 200, map[string]any{"resource": p.cfg.PublicURL + "/mcp", "authorization_servers": []string{p.cfg.PublicURL}, "scopes_supported": []string{"tiimo"}, "bearer_methods_supported": []string{"header"}})
}
func (p *Provider) discovery(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, 200, map[string]any{"issuer": p.cfg.PublicURL, "authorization_endpoint": p.cfg.PublicURL + "/oauth/authorize", "token_endpoint": p.cfg.PublicURL + "/oauth/token", "response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code", "refresh_token"}, "code_challenge_methods_supported": []string{"S256"}, "token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"}, "scopes_supported": []string{"tiimo"}, "authorization_response_iss_parameter_supported": true})
}

var challengeRE = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
var verifierRE = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)
var loginPage = template.Must(template.New("login").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Connect Tiimo</title><body><main><h1>Connect your Tiimo planner</h1><p>This private server lets your connected MCP client read and manage the configured Tiimo account.</p><form method="post" action="/oauth/approve"><input type="hidden" name="request" value="{{.}}"><label>Server connection password <input name="password" type="password" autocomplete="current-password" required maxlength="512"></label><button>Connect</button></form><p>Use MCP_LOGIN_PASSWORD from your Railway settings, not your Tiimo password.</p></main></body></html>`))

func (p *Provider) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	validRedirect := false
	for _, u := range p.cfg.RedirectURIs {
		if q.Get("redirect_uri") == u {
			validRedirect = true
		}
	}
	if q.Get("client_id") != p.cfg.ClientID || !validRedirect || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || !challengeRE.MatchString(q.Get("code_challenge")) || q.Get("resource") != p.cfg.PublicURL+"/mcp" || len(q.Get("state")) > 2048 {
		failure(w, "invalid_request")
		return
	}
	if scope := q.Get("scope"); scope != "" && scope != "tiimo" {
		failure(w, "invalid_scope")
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clean()
	if p.limited() || len(p.pending) >= 128 {
		http.Error(w, "Try again later", http.StatusTooManyRequests)
		return
	}
	key := random()
	p.pending[digest(key)] = grant{Redirect: q.Get("redirect_uri"), State: q.Get("state"), Challenge: q.Get("code_challenge"), Resource: q.Get("resource"), Expiry: time.Now().Add(5 * time.Minute)}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; form-action 'self'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "same-origin")
	_ = loginPage.Execute(w, key)
}
func parse(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if e := r.ParseForm(); e != nil {
		failure(w, "invalid_request")
		return false
	}
	return true
}
func (p *Provider) approve(w http.ResponseWriter, r *http.Request) {
	if !parse(w, r) {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clean()
	key := digest(r.PostForm.Get("request"))
	g, ok := p.pending[key]
	if p.limited() {
		http.Error(w, "Try again later", http.StatusTooManyRequests)
		return
	}
	if !ok || !equal(r.PostForm.Get("password"), p.cfg.Password) {
		http.Error(w, "Invalid or expired connection request", http.StatusForbidden)
		return
	}
	delete(p.pending, key)
	if len(p.codes) >= 128 {
		http.Error(w, "Try again later", http.StatusTooManyRequests)
		return
	}
	code := random()
	g.Expiry = time.Now().Add(time.Minute)
	p.codes[digest(code)] = g
	u, _ := url.Parse(g.Redirect)
	q := u.Query()
	q.Set("code", code)
	q.Set("state", g.State)
	q.Set("iss", p.cfg.PublicURL)
	u.RawQuery = q.Encode()
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
}
func (p *Provider) exchange(w http.ResponseWriter, r *http.Request) {
	if !parse(w, r) {
		return
	}
	client, secret, hasBasic := r.BasicAuth()
	if !hasBasic {
		client = r.PostForm.Get("client_id")
		secret = r.PostForm.Get("client_secret")
	}
	if !equal(client, p.cfg.ClientID) || !equal(secret, p.cfg.ClientSecret) {
		jsonResponse(w, 401, map[string]string{"error": "invalid_client"})
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clean()
	resource := r.PostForm.Get("resource")
	if resource != p.cfg.PublicURL+"/mcp" {
		failure(w, "invalid_target")
		return
	}
	var codeKey, refreshHash string
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		codeKey = digest(r.PostForm.Get("code"))
		g, ok := p.codes[codeKey]
		verifier := r.PostForm.Get("code_verifier")
		if !ok || g.Resource != resource || g.Redirect != r.PostForm.Get("redirect_uri") || !verifierRE.MatchString(verifier) || !equal(digest(verifier), g.Challenge) {
			failure(w, "invalid_grant")
			return
		}
	case "refresh_token":
		refreshHash = digest(r.PostForm.Get("refresh_token"))
	default:
		failure(w, "unsupported_grant_type")
		return
	}
	a, f, err := p.store.issue(r.Context(), resource, refreshHash)
	if errors.Is(err, errInvalidGrant) {
		failure(w, "invalid_grant")
		return
	}
	if err != nil {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "temporarily_unavailable"})
		return
	}
	if codeKey != "" {
		delete(p.codes, codeKey)
	}

	jsonResponse(w, 200, map[string]any{"access_token": a, "refresh_token": f, "token_type": "Bearer", "expires_in": 3600, "scope": "tiimo"})
}
func (p *Provider) Protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		raw, ok := strings.CutPrefix(header, "Bearer ")
		if len(raw) > 512 {
			ok = false
		}
		valid := false
		if ok {
			var err error
			valid, err = p.store.valid(r.Context(), digest(raw), p.cfg.PublicURL+"/mcp")
			if err != nil {
				jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "temporarily_unavailable"})
				return
			}
		}

		if !valid {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+p.cfg.PublicURL+`/.well-known/oauth-protected-resource", scope="tiimo"`)
			jsonResponse(w, 401, map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

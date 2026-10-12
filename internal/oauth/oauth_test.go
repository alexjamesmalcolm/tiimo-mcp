package oauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func TestOAuthPKCEAndRefresh(t *testing.T) {
	p, e := New(Config{PublicURL: "https://tiimo.example", ClientID: "private-client", ClientSecret: strings.Repeat("s", 32), Password: strings.Repeat("p", 32)})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if err := p.Close(); err != nil {
			t.Error(err)
		}
	})
	mux := http.NewServeMux()
	p.Register(mux)
	mux.Handle("/mcp", p.Protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })))
	call := func(method, path string, values url.Values, bearer string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(values.Encode()))
		if method == "POST" {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	verifier := strings.Repeat("v", 43)
	resource := "https://tiimo.example/mcp"
	redirect := "https://chatgpt.com/connector_platform_oauth_redirect"
	args := url.Values{"client_id": {"private-client"}, "redirect_uri": {redirect}, "response_type": {"code"}, "code_challenge_method": {"S256"}, "code_challenge": {digest(verifier)}, "resource": {resource}, "state": {"my-state"}, "scope": {"tiimo"}}
	w := call("GET", "/oauth/authorize?"+args.Encode(), nil, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	match := regexp.MustCompile(`name="request" value="([^"]+)"`).FindStringSubmatch(w.Body.String())
	if len(match) != 2 {
		t.Fatal("missing csrf nonce")
	}
	w = call("POST", "/oauth/approve", url.Values{"request": {match[1]}, "password": {strings.Repeat("p", 32)}}, "")
	if w.Code != 303 {
		t.Fatal(w.Code)
	}
	location, _ := url.Parse(w.Header().Get("Location"))
	code := location.Query().Get("code")
	if location.Query().Get("state") != "my-state" || location.Query().Get("iss") != "https://tiimo.example" {
		t.Fatal("state/issuer lost")
	}
	tokenArgs := url.Values{"client_id": {"private-client"}, "client_secret": {strings.Repeat("s", 32)}, "grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {redirect}, "resource": {resource}}
	tokenArgs.Set("code_verifier", strings.Repeat("w", 43))
	if w = call("POST", "/oauth/token", tokenArgs, ""); w.Code != 400 {
		t.Fatal("bad PKCE accepted")
	}
	tokenArgs.Set("code_verifier", verifier)
	w = call("POST", "/oauth/token", tokenArgs, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var tokens map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &tokens)
	if w = call("POST", "/oauth/token", tokenArgs, ""); w.Code != 400 {
		t.Fatal("code replay allowed")
	}
	if w = call("POST", "/mcp", nil, tokens["access_token"].(string)); w.Code != 204 {
		t.Fatal("valid token denied", w.Code)
	}
	if w = call("POST", "/mcp", nil, "invalid"); w.Code != 401 || !strings.Contains(w.Header().Get("WWW-Authenticate"), "resource_metadata") {
		t.Fatal("missing auth challenge")
	}
	refreshArgs := url.Values{"client_id": {"private-client"}, "client_secret": {strings.Repeat("s", 32)}, "grant_type": {"refresh_token"}, "refresh_token": {tokens["refresh_token"].(string)}, "resource": {resource}}
	if w = call("POST", "/oauth/token", refreshArgs, ""); w.Code != 200 {
		t.Fatal("refresh failed")
	}
	if w = call("POST", "/oauth/token", refreshArgs, ""); w.Code != 400 {
		t.Fatal("refresh replay allowed")
	}
	args.Set("redirect_uri", "https://evil.example/callback")
	if w = call("GET", "/oauth/authorize?"+args.Encode(), nil, ""); w.Code != 400 {
		t.Fatal("unregistered redirect accepted")
	}
}

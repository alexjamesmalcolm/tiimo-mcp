package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"tiimo-mcp/internal/oauth"
	"tiimo-mcp/internal/tiimo"
	"tiimo-mcp/internal/tools"
)

func main() {
	transport := flag.String("transport", "http", "http or stdio")
	doctor := flag.Bool("doctor", false, "Verify Tiimo token with a read-only profiles request")
	flag.Parse()
	c := tiimo.New(os.Getenv("TIIMO_PROFILE_ID"), os.Getenv("TIIMO_TOKEN_FILE"), os.Getenv("TIIMO_READ_ONLY") == "true")
	if *doctor {
		data, e := c.Do(context.Background(), "GET", "/api/profiles", nil)
		if e != nil {
			log.Fatal(e)
		}
		_ = json.NewEncoder(os.Stdout).Encode(data)
		return
	}
	if _, e := c.Token(); e != nil {
		log.Fatal(e)
	}
	s := tools.New(c)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *transport == "stdio" {
		if e := s.Run(ctx, &mcp.StdioTransport{}); e != nil {
			log.Fatal(e)
		}
		return
	}
	if *transport != "http" {
		log.Fatal("transport must be http or stdio")
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	public := os.Getenv("PUBLIC_URL")
	if public == "" {
		if host := os.Getenv("RAILWAY_PUBLIC_DOMAIN"); host != "" {
			public = "https://" + host
		}
	}
	mux := http.NewServeMux()
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	host := "127.0.0.1"
	var h http.Handler = handler
	if public != "" {
		redirects := []string{}
		for v := range strings.SplitSeq(os.Getenv("OAUTH_REDIRECT_URIS"), ",") {
			if strings.TrimSpace(v) != "" {
				redirects = append(redirects, strings.TrimSpace(v))
			}
		}
		dbPath := os.Getenv("AUTH_DB_PATH")
		if dbPath == "" {
			dbPath = "/data/auth/auth.db"
		}
		auth, e := oauth.New(oauth.Config{DBPath: dbPath, PublicURL: public, ClientID: os.Getenv("OAUTH_CLIENT_ID"), ClientSecret: os.Getenv("OAUTH_CLIENT_SECRET"), Password: os.Getenv("MCP_LOGIN_PASSWORD"), RedirectURIs: redirects})
		if e != nil {
			log.Fatal(e)
		}
		defer auth.Close()
		auth.Register(mux)
		h = auth.Protect(handler)
		host = "0.0.0.0"
	}
	mux.Handle("/mcp", h)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"ok"}`)
	})
	// Reject browser origins except the configured service itself. Host validation
	// protects unauthenticated localhost mode from DNS rebinding.
	secured := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if origin := r.Header.Get("Origin"); origin != "" && origin != strings.TrimRight(public, "/") {
			http.Error(w, "Origin denied", 403)
			return
		}
		if public == "" {
			u, e := url.Parse("http://" + r.Host)
			if e != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "[::1]") {
				http.Error(w, "Host denied", 403)
				return
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1024*1024)
		mux.ServeHTTP(w, r)
	})
	server := &http.Server{Addr: host + ":" + port, Handler: secured, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 35 * time.Second, WriteTimeout: 45 * time.Second, IdleTimeout: 60 * time.Second}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(closeCtx)
	}()
	log.Printf("Tiimo MCP listening on %s", server.Addr)
	if e := server.ListenAndServe(); e != nil && e != http.ErrServerClosed {
		log.Fatal(e)
	}
	if ctx.Err() != nil {
		<-shutdownDone
	}
}

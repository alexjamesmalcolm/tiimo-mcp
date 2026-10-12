package tiimo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type Object = map[string]any

type Client struct {
	BaseURL   string
	HTTP      *http.Client
	Token     func() (string, error)
	ProfileID string
	ReadOnly  bool
	locks     [64]sync.Mutex
}

func New(profileID, tokenFile string, readOnly bool) *Client {
	return &Client{BaseURL: "https://api.tiimoapp.com", ProfileID: profileID, ReadOnly: readOnly,
		HTTP: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		Token: func() (string, error) {
			token := os.Getenv("TIIMO_TOKEN")
			if tokenFile != "" {
				b, err := os.ReadFile(tokenFile)
				if err != nil {
					return "", errors.New("cannot read TIIMO_TOKEN_FILE")
				}
				token = string(b)
			}
			token = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(token), "Bearer "))
			if token == "" || strings.ContainsAny(token, "\r\n") {
				return "", errors.New("set TIIMO_TOKEN or TIIMO_TOKEN_FILE outside chat")
			}
			return token, nil
		}}
}

func (c *Client) Lock(id string) func() {
	var n uint32
	for _, r := range id {
		n = n*31 + uint32(r)
	}
	m := &c.locks[n%64]
	m.Lock()
	return m.Unlock
}

func (c *Client) Do(ctx context.Context, method, path string, body any) (any, error) {
	if c.ReadOnly && method != http.MethodGet {
		return nil, errors.New("writes are disabled by TIIMO_READ_ONLY")
	}
	if !strings.HasPrefix(path, "/api/") || strings.Contains(path, "..") {
		return nil, errors.New("invalid API path")
	}
	token, err := c.Token()
	if err != nil {
		return nil, err
	}
	var data []byte
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return nil, errors.New("invalid request body")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("invalid request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "en-GB")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		if method != http.MethodGet {
			return nil, errors.New("tiimo request failed; the write may have succeeded. Read the item before retrying")
		}
		return nil, errors.New("tiimo request failed or timed out")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		switch res.StatusCode {
		case 401:
			return nil, errors.New("tiimo rejected the access token; replace TIIMO_TOKEN with a fresh token")
		case 403:
			return nil, errors.New("tiimo denied access to this profile or operation")
		case 429:
			return nil, errors.New("tiimo rate limit reached; wait before retrying")
		default:
			return nil, fmt.Errorf("tiimo returned HTTP %d; check the request against the saved API contract", res.StatusCode)
		}
	}
	if res.StatusCode == 204 {
		return Object{"success": true}, nil
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 2*1024*1024+1))
	if err != nil {
		return nil, errors.New("cannot read Tiimo response; verify any write before retrying")
	}
	if len(raw) > 2*1024*1024 {
		return nil, errors.New("tiimo response exceeds 2 MiB; narrow the date range")
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return Object{"success": true}, nil
	}
	var out any
	if err = json.Unmarshal(raw, &out); err != nil {
		return nil, errors.New("tiimo returned unexpected non-JSON data; verify any write before retrying")
	}
	return out, nil
}

func AsObject(v any) (Object, error) {
	o, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("unexpected Tiimo response shape; refusing to update")
	}
	return o, nil
}

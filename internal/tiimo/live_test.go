//go:build tiimo_live

package tiimo

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
)

// TestLiveTiimoProfiles makes one read-only request to Tiimo. The tiimo_live
// build tag keeps this credentialed integration check out of ordinary test runs.
func TestLiveTiimoProfiles(t *testing.T) {
	token := strings.TrimSpace(os.Getenv("TIIMO_TOKEN"))
	tokenFile := strings.TrimSpace(os.Getenv("TIIMO_TOKEN_FILE"))
	if token == "" && tokenFile == "" {
		t.Fatal("set TIIMO_TOKEN or TIIMO_TOKEN_FILE before running this live test")
	}

	client := New("", tokenFile, true)
	profiles, err := client.Do(context.Background(), http.MethodGet, "/api/profiles", nil)
	if err != nil {
		t.Fatalf("read-only GET /api/profiles failed: %v", err)
	}
	if profiles == nil {
		t.Fatal("Tiimo returned an empty profiles response")
	}

	t.Log("read-only GET /api/profiles succeeded")
}

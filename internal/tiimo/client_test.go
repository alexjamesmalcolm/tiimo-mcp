package tiimo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTaskProjectionPreservesDetails(t *testing.T) {
	original := Object{"taskId": "id", "title": "old", "notes": "keep", "duration": 1800.0, "todoTaskListId": "list", "color": "#aabbcc", "isChecked": false, "createdAt": "response only", "grouping": Object{"groupingType": "Priority", "groupLabel": "High", "sortOrder": 9.0}, "subTasks": []any{Object{"subTaskId": "sub-id", "title": "step", "color": "#112233", "isChecked": true, "index": 0.0, "createdAt": "response only"}}}
	body := UpdateTaskBody(original, Object{"title": "new"})
	if body["notes"] != "keep" || body["duration"] != 1800.0 || body["backgroundColor"] != "#aabbcc" || body["currentGrouping"] != "Priority" {
		t.Fatalf("lost fields: %#v", body)
	}
	if _, ok := body["createdAt"]; ok {
		t.Fatal("response-only field copied")
	}
	sub := body["subTasks"].([]any)[0].(map[string]any)
	if sub["subTaskId"] != "sub-id" || sub["backgroundColor"] != "#112233" || sub["isChecked"] != true {
		t.Fatalf("lost checklist: %#v", sub)
	}
	if _, ok := original["backgroundColor"]; ok {
		t.Fatal("mutated caller")
	}
	if _, ok := original["subTasks"].([]any)[0].(map[string]any)["backgroundColor"]; ok {
		t.Fatal("mutated nested caller")
	}
}
func TestHTTPErrorDoesNotLeakTokenOrRetryWrite(t *testing.T) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			t.Error("missing token")
		}
		w.WriteHeader(500)
		_, _ = w.Write([]byte("secret-token personal notes"))
	}))
	defer ts.Close()
	c := New("", "", false)
	c.BaseURL = ts.URL
	c.Token = func() (string, error) { return "secret-token", nil }
	_, err := c.Do(context.Background(), "POST", "/api/profiles", Object{"title": "x"})
	if err == nil || strings.Contains(err.Error(), "secret-token") || calls != 1 {
		t.Fatalf("unsafe error or retry: %v calls=%d", err, calls)
	}
}
func TestReadOnlyBlocksBeforeNetwork(t *testing.T) {
	c := New("", "", true)
	if _, err := c.Do(context.Background(), "POST", "/api/profiles", nil); err == nil {
		t.Fatal("write allowed")
	}
}
func TestRedirectDoesNotForwardCredentials(t *testing.T) {
	calls := 0
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer other.Close()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, http.StatusFound) }))
	defer ts.Close()
	c := New("", "", false)
	c.BaseURL = ts.URL
	c.Token = func() (string, error) { return "secret", nil }
	if _, err := c.Do(context.Background(), "GET", "/api/profiles", nil); err == nil {
		t.Fatal("redirect accepted")
	}
	if calls != 0 {
		t.Fatal("credentials forwarded")
	}
}

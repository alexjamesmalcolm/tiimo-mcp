package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"tiimo-mcp/internal/tiimo"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const testProfile = "11111111-1111-1111-1111-111111111111"
const testTask = "22222222-2222-2222-2222-222222222222"

func session(t *testing.T, c *tiimo.Client) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	a, b := mcp.NewInMemoryTransports()
	ss, e := New(c).Connect(ctx, a, nil)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, e := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, b, nil)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}
func TestMCPCreateAndPreservingUpdate(t *testing.T) {
	var create, update tiimo.Object
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case "GET":
			_ = json.NewEncoder(w).Encode(tiimo.Object{"taskId": testTask, "title": "existing", "duration": 1800, "notes": "retain", "color": "#123456", "isChecked": false, "subTasks": []any{}, "grouping": tiimo.Object{"groupingType": "Manual"}})
		case "POST":
			_ = json.NewDecoder(r.Body).Decode(&create)
			_ = json.NewEncoder(w).Encode(tiimo.Object{"taskId": testTask})
		case "PUT":
			_ = json.NewDecoder(r.Body).Decode(&update)
			_ = json.NewEncoder(w).Encode(update)
		default:
			t.Errorf("unexpected %s", r.Method)
		}
	}))
	defer ts.Close()
	c := tiimo.New(testProfile, "", false)
	c.BaseURL = ts.URL
	c.Token = func() (string, error) { return "mock", nil }
	cs := session(t, c)
	listed, e := cs.ListTools(context.Background(), nil)
	if e != nil {
		t.Fatal(e)
	}
	if len(listed.Tools) != 16 {
		t.Fatalf("tools=%d", len(listed.Tools))
	}
	res, e := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "create_task", Arguments: map[string]any{"title": "MAT — read section", "durationMinutes": 30, "subtasks": []string{"Read", "Practice"}}})
	if e != nil || res.IsError {
		t.Fatalf("create failed: %v %#v", e, res)
	}
	if create["duration"] != 1800.0 || len(create["subTasks"].([]any)) != 2 {
		t.Fatalf("bad create: %#v", create)
	}
	res, e = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "update_task", Arguments: map[string]any{"taskId": testTask, "title": "new"}})
	if e != nil || res.IsError {
		t.Fatalf("update failed: %v %#v", e, res)
	}
	if update["duration"] != 1800.0 || update["notes"] != "retain" || update["backgroundColor"] != "#123456" {
		t.Fatalf("lost details: %#v", update)
	}
	res, e = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "create_activity", Arguments: map[string]any{"title": "x", "start": "2026-10-07T10:00:00", "durationMinutes": 30}})
	if e != nil || !res.IsError {
		t.Fatal("timezone-less time not rejected")
	}
}
func TestMCPReadOnlyOmitsWrites(t *testing.T) {
	c := tiimo.New(testProfile, "", true)
	cs := session(t, c)
	v, e := cs.ListTools(context.Background(), nil)
	if e != nil {
		t.Fatal(e)
	}
	if len(v.Tools) != 6 {
		t.Fatalf("tools=%d", len(v.Tools))
	}
}
func TestStreamableHTTPHandshake(t *testing.T) {
	c := tiimo.New(testProfile, "", true)
	s := New(c)
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	defer ts.Close()
	cs, e := mcp.NewClient(&mcp.Implementation{Name: "test-http", Version: "1"}, nil).Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: ts.URL}, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer cs.Close()
	v, e := cs.ListTools(context.Background(), nil)
	if e != nil || len(v.Tools) != 6 {
		t.Fatalf("HTTP list: %v %#v", e, v)
	}
}

func TestNativeSchedulePreservesChecklist(t *testing.T) {
	var body tiimo.Object
	var path string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			_ = json.NewEncoder(w).Encode(tiimo.Object{"taskId": testTask, "title": "BSF notes", "notes": "Read first", "duration": 1200, "iconType": "UnicodeEmoji", "iconId": "📖", "color": "#123456", "isChecked": false, "tagIds": []string{"faith"}, "subTasks": []any{tiimo.Object{"title": "Read", "index": 0, "isChecked": true, "color": "#abcdef", "iconType": "None"}}})
			return
		}
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(204)
	}))
	defer ts.Close()
	c := tiimo.New(testProfile, "", false)
	c.BaseURL = ts.URL
	c.Token = func() (string, error) { return "mock", nil }
	cs := session(t, c)
	res, e := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "schedule_task", Arguments: map[string]any{"taskId": testTask, "start": "2026-10-07T10:00:00-04:00"}})
	if e != nil || res.IsError {
		t.Fatalf("schedule: %v %#v", e, res)
	}
	if path != "/api/profiles/"+testProfile+"/todo-tasks/convert/"+testTask || body["description"] != "Read first" || body["duration"] != 1200.0 || body["endTime"] != "2026-10-07T10:20:00-04:00" {
		t.Fatalf("bad conversion: %s %#v", path, body)
	}
	checklist := body["checklist"].(map[string]any)["checklistItems"].([]any)[0].(map[string]any)
	if checklist["isChecked"] != true || checklist["backgroundColor"] != "#abcdef" {
		t.Fatalf("checklist lost: %#v", checklist)
	}
}

func TestReschedulePreservesChecklistAndRejectsRepeats(t *testing.T) {
	repeating := false
	writes := 0
	var body tiimo.Object
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			_ = json.NewEncoder(w).Encode(tiimo.Object{"activityId": testTask, "title": "Read", "description": "keep", "duration": 1800, "isRepeating": repeating, "recurrenceType": "noRepeat", "hasTimedSubtasks": false, "allowEarlyStart": true, "checklist": tiimo.Object{"checklistId": testTask, "checklistItems": []any{tiimo.Object{"checklistItemId": testTask, "title": "step", "isChecked": true, "index": 0, "checkedAt": "response only"}}}})
			return
		}
		writes++
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(204)
	}))
	defer ts.Close()
	c := tiimo.New(testProfile, "", false)
	c.BaseURL = ts.URL
	c.Token = func() (string, error) { return "mock", nil }
	cs := session(t, c)
	args := map[string]any{"activityId": testTask, "start": "2026-10-07T12:00:00-04:00"}
	res, e := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "reschedule_activity", Arguments: args})
	if e != nil || res.IsError {
		t.Fatalf("reschedule failed: %v %#v", e, res)
	}
	if body["description"] != "keep" || body["endTime"] != "2026-10-07T12:30:00-04:00" || body["allowEarlyStart"] != true {
		t.Fatalf("details lost: %#v", body)
	}
	item := body["checklist"].(map[string]any)["checklistItems"].([]any)[0].(map[string]any)
	if item["isChecked"] != true {
		t.Fatal("checklist completion lost")
	}
	if _, ok := item["checkedAt"]; ok {
		t.Fatal("response-only field leaked")
	}
	repeating = true
	res, e = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "reschedule_activity", Arguments: args})
	if e != nil || !res.IsError || writes != 1 {
		t.Fatal("repeating activity modified")
	}
}

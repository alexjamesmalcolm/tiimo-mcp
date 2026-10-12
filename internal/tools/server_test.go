Sourcing ~/.zshenv
package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
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
	if _, ok := body["grouping"]; ok {
		t.Fatalf("timestamp scheduling should leave grouping unset: %#v", body["grouping"])
	}
	checklist := body["checklist"].(map[string]any)["checklistItems"].([]any)[0].(map[string]any)
	if checklist["isChecked"] != true || checklist["backgroundColor"] != "#abcdef" {
		t.Fatalf("checklist lost: %#v", checklist)
	}
}

func TestScheduleTaskSupportsAllTimeOfDayChoices(t *testing.T) {
	tests := []struct {
		name     string
		grouping any
	}{
		{name: "Anytime", grouping: nil},
		{name: "Morning", grouping: map[string]any{"groupingType": "TimeOfDay", "groupingLabel": "Morning"}},
		{name: "Afternoon", grouping: map[string]any{"groupingType": "TimeOfDay", "groupingLabel": "Day"}},
		{name: "Evening", grouping: map[string]any{"groupingType": "TimeOfDay", "groupingLabel": "Evening"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body tiimo.Object
			writes := 0
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet {
					_ = json.NewEncoder(w).Encode(tiimo.Object{"taskId": testTask, "title": "Probe", "notes": "", "duration": 600, "isChecked": false, "iconType": "UnicodeEmoji", "iconId": "📚", "tagIds": []string{}, "subTasks": []any{}})
					return
				}
				writes++
				_ = json.NewDecoder(r.Body).Decode(&body)
				_ = json.NewEncoder(w).Encode(tiimo.Object{"activityId": testTask})
			}))
			defer ts.Close()
			c := tiimo.New(testProfile, "", false)
			c.BaseURL = ts.URL
			c.Token = func() (string, error) { return "mock", nil }
			cs := session(t, c)
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "schedule_task", Arguments: map[string]any{
				"taskId": testTask, "date": "2026-10-13", "timeOfDay": tt.name,
			}})
			if err != nil || res.IsError {
				t.Fatalf("schedule task: %v %#v", err, res)
			}
			if writes != 1 || body["startTime"] != "2026-10-13T00:00:00" || body["endTime"] != "2026-10-13T00:10:00" || body["duration"] != 600.0 {
				t.Fatalf("unexpected conversion body: writes=%d body=%#v", writes, body)
			}
			gotGrouping, hasGrouping := body["grouping"]
			if !hasGrouping {
				t.Fatal("date plus timeOfDay must send grouping explicitly, including null for Anytime")
			}
			if !reflect.DeepEqual(gotGrouping, tt.grouping) {
				t.Fatalf("grouping = %#v; want %#v", gotGrouping, tt.grouping)
			}
		})
	}
}

func TestCreateActivitySupportsAllTimeOfDayChoices(t *testing.T) {
	tests := []struct {
		name     string
		grouping any
	}{
		{name: "Anytime", grouping: nil},
		{name: "Morning", grouping: map[string]any{"groupingType": "TimeOfDay", "groupingLabel": "Morning"}},
		{name: "Afternoon", grouping: map[string]any{"groupingType": "TimeOfDay", "groupingLabel": "Day"}},
		{name: "Evening", grouping: map[string]any{"groupingType": "TimeOfDay", "groupingLabel": "Evening"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body tiimo.Object
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method != http.MethodPost {
					t.Errorf("unexpected method %s", r.Method)
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				_ = json.NewEncoder(w).Encode(tiimo.Object{"activityId": testTask})
			}))
			defer ts.Close()
			c := tiimo.New(testProfile, "", false)
			c.BaseURL = ts.URL
			c.Token = func() (string, error) { return "mock", nil }
			cs := session(t, c)
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "create_activity", Arguments: map[string]any{
				"title": "Probe", "durationMinutes": 10, "date": "2026-10-13", "timeOfDay": tt.name,
			}})
			if err != nil || res.IsError {
				t.Fatalf("create activity: %v %#v", err, res)
			}
			if body["startTime"] != "2026-10-13T00:00:00" || body["endTime"] != "2026-10-13T00:10:00" || body["isAllDay"] != false {
				t.Fatalf("unexpected activity body: %#v", body)
			}
			gotGrouping, hasGrouping := body["grouping"]
			if !hasGrouping {
				t.Fatal("date plus timeOfDay must send grouping explicitly, including null for Anytime")
			}
			if !reflect.DeepEqual(gotGrouping, tt.grouping) {
				t.Fatalf("grouping = %#v; want %#v", gotGrouping, tt.grouping)
			}
		})
	}
}

func TestScheduleInputSchemaShowsDateTimeOrDaypartChoices(t *testing.T) {
	c := tiimo.New(testProfile, "", false)
	cs := session(t, c)
	listed, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	for _, tool := range listed.Tools {
		if tool.Name != "schedule_task" {
			continue
		}
		b, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, &schema); err != nil {
			t.Fatal(err)
		}
	}
	if schema == nil {
		t.Fatal("schedule_task schema missing")
	}
	props := schema["properties"].(map[string]any)
	daypart := props["timeOfDay"].(map[string]any)
	got := daypart["enum"].([]any)
	want := []any{"Anytime", "Morning", "Afternoon", "Evening"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("timeOfDay enum = %#v; want %#v", got, want)
	}
	if len(schema["oneOf"].([]any)) != 2 {
		t.Fatalf("schema should expose two exclusive schedule choices: %#v", schema["oneOf"])
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

func TestRescheduleActivitySetsOrClearsTimeOfDay(t *testing.T) {
	var body tiimo.Object
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(tiimo.Object{"activityId": testTask, "title": "Read", "description": "keep", "duration": 600, "isRepeating": false, "recurrenceType": "noRepeat", "hasTimedSubtasks": false, "grouping": tiimo.Object{"groupingType": "TimeOfDay", "groupingLabel": "Morning"}})
			return
		}
		body = tiimo.Object{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()
	c := tiimo.New(testProfile, "", false)
	c.BaseURL = ts.URL
	c.Token = func() (string, error) { return "mock", nil }
	cs := session(t, c)
	for _, tt := range []struct {
		name         string
		wantGrouping any
	}{
		{name: "Afternoon", wantGrouping: map[string]any{"groupingType": "TimeOfDay", "groupingLabel": "Day"}},
		{name: "Anytime", wantGrouping: nil},
	} {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "reschedule_activity", Arguments: map[string]any{
			"activityId": testTask, "date": "2026-10-13", "timeOfDay": tt.name,
		}})
		if err != nil || res.IsError {
			t.Fatalf("reschedule %s: %v %#v", tt.name, err, res)
		}
		gotGrouping, hasGrouping := body["grouping"]
		if !hasGrouping {
			t.Fatalf("%s should send grouping explicitly to clear or set time of day", tt.name)
		}
		if !reflect.DeepEqual(gotGrouping, tt.wantGrouping) {
			t.Fatalf("%s grouping = %#v; want %#v", tt.name, gotGrouping, tt.wantGrouping)
		}
	}
}

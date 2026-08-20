package ocisclient

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// activitiesFixture is a real captured response shape (verified live against a running
// oCIS instance: an upload followed by a rename), used to pin this client's parsing against
// what oCIS actually sends rather than an idealized shape.
const activitiesFixture = `{"value":[
	{"id":"c7099e86-e426-4731-8669-fa5a835bfced","template":{"message":"{user} added {resource} to {folder}","variables":{"folder":{"id":"","name":"Admin"},"resource":{"id":"","name":"spike-move-src.txt"},"user":{"id":"7ef1babf-8c0d-43b8-936d-08c18cbe5769","displayName":"Admin"}}},"times":{"recordedTime":"2026-08-20T15:12:49.130904089Z"}},
	{"id":"0af2221b-4d6e-4e47-aa7f-670853178c63","template":{"message":"{user} renamed {oldResource} to {resource}","variables":{"folder":{"id":"","name":"Admin"},"oldResource":{"id":"","name":"spike-move-src.txt"},"resource":{"id":"31887342-c711-4c0f-973a-bf2a23400fd9$7ef1babf-8c0d-43b8-936d-08c18cbe5769!f3432faf-bf88-42d8-821f-7bfeb409e6b2","name":"spike-move-dst.txt"},"user":{"id":"7ef1babf-8c0d-43b8-936d-08c18cbe5769","displayName":"Admin"}}},"times":{"recordedTime":"2026-08-20T15:12:49.17125613Z"}}
]}`

func TestListActivitiesParsesRealResponseShape(t *testing.T) {
	var gotAuth string
	var gotKQL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graph/v1beta1/extensions/org.libregraph/activities" {
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}
		gotKQL = r.URL.Query().Get("kql")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(activitiesFixture))
	}))
	defer srv.Close()

	c := New(srv.URL, false)
	since := time.Date(2026, 8, 20, 15, 0, 0, 0, time.UTC)
	activities, err := c.ListActivities(t.Context(), "Basic dGVzdA==", "drive-1", since)
	if err != nil {
		t.Fatalf("ListActivities: %v", err)
	}

	if gotAuth != "Basic dGVzdA==" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Basic dGVzdA==")
	}
	if !strings.Contains(gotKQL, "itemid:drive-1") {
		t.Errorf("query kql missing itemid clause: %q", gotKQL)
	}
	if !strings.Contains(gotKQL, "depth:-1") {
		t.Errorf("query kql missing depth clause: %q", gotKQL)
	}
	if !strings.Contains(gotKQL, "timestamp>2026-08-20T15:00:00Z") {
		t.Errorf("query kql missing timestamp clause: %q", gotKQL)
	}

	if len(activities) != 2 {
		t.Fatalf("len(activities) = %d, want 2", len(activities))
	}

	if activities[0].Message != "{user} added {resource} to {folder}" {
		t.Errorf("activities[0].Message = %q", activities[0].Message)
	}
	if activities[0].ID != "c7099e86-e426-4731-8669-fa5a835bfced" {
		t.Errorf("activities[0].ID = %q", activities[0].ID)
	}
	wantTime := time.Date(2026, 8, 20, 15, 12, 49, 130904089, time.UTC)
	if !activities[0].RecordedTime.Equal(wantTime) {
		t.Errorf("activities[0].RecordedTime = %v, want %v", activities[0].RecordedTime, wantTime)
	}

	if activities[1].Message != "{user} renamed {oldResource} to {resource}" {
		t.Errorf("activities[1].Message = %q", activities[1].Message)
	}
	wantResourceID := "31887342-c711-4c0f-973a-bf2a23400fd9$7ef1babf-8c0d-43b8-936d-08c18cbe5769!f3432faf-bf88-42d8-821f-7bfeb409e6b2"
	if activities[1].Resource.ID != wantResourceID {
		t.Errorf("activities[1].Resource.ID = %q, want %q", activities[1].Resource.ID, wantResourceID)
	}
	if activities[1].Resource.Name != "spike-move-dst.txt" {
		t.Errorf("activities[1].Resource.Name = %q", activities[1].Resource.Name)
	}

	// The first activity's resource.id is empty in the real fixture (oCIS omits it for a
	// freshly-added file whose parent lookup raced the write) — must not crash or panic.
	if activities[0].Resource.ID != "" {
		t.Errorf("activities[0].Resource.ID = %q, want empty (matches real fixture)", activities[0].Resource.ID)
	}
}

func TestListActivitiesWithCompoundDriveID(t *testing.T) {
	var gotKQL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKQL = r.URL.Query().Get("kql")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, false)
	// Use a realistic compound driveID with $ and ! to verify proper percent-encoding round-trip
	compoundDriveID := "31887342-c711-4c0f-973a-bf2a23400fd9$7ef1babf-8c0d-43b8-936d-08c18cbe5769!f3432faf-bf88-42d8-821f-7bfeb409e6b2"
	since := time.Date(2026, 8, 20, 15, 0, 0, 0, time.UTC)
	_, err := c.ListActivities(t.Context(), "Basic dGVzdA==", compoundDriveID, since)
	if err != nil {
		t.Fatalf("ListActivities: %v", err)
	}

	// After URL decoding, the KQL should contain the compound ID unencoded
	if !strings.Contains(gotKQL, compoundDriveID) {
		t.Errorf("decoded query kql missing compound driveID: got %q, want to contain %q", gotKQL, compoundDriveID)
	}
	if !strings.Contains(gotKQL, "itemid:"+compoundDriveID) {
		t.Errorf("decoded query kql missing itemid clause with compound ID: got %q", gotKQL)
	}
}

func TestListActivitiesNonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := New(srv.URL, false)
	_, err := c.ListActivities(t.Context(), "Basic dGVzdA==", "drive-1", time.Now())
	if err == nil {
		t.Fatal("ListActivities: err = nil, want an error for a non-200 response")
	}
}

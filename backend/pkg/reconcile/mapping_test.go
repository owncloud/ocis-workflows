package reconcile

import "testing"

func TestTriggerType(t *testing.T) {
	cases := []struct {
		message string
		want    string
		wantOK  bool
	}{
		{"{user} added {resource} to {folder}", "upload", true},
		{"{user} moved {resource} to {folder}", "move", true},
		{"{user} renamed {oldResource} to {resource}", "move", true},
		{"{user} shared {resource} with {sharee}", "", false},       // share — not backstopped yet
		{"{user} deleted {resource} from {folder}", "", false},      // trashed — not a trigger type
		{"", "", false},
		{"some unrecognized future message", "", false},
	}
	for _, c := range cases {
		got, ok := TriggerType(c.message)
		if got != c.want || ok != c.wantOK {
			t.Errorf("TriggerType(%q) = (%q, %v), want (%q, %v)", c.message, got, ok, c.want, c.wantOK)
		}
	}
}

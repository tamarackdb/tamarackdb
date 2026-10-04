package dcb

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestSharedQueryCases checks every case in testdata/query-cases.json
// against Query.Matches. internal/store runs the same file against the
// SQL a query turns into, so both agree on what a query selects.
func TestSharedQueryCases(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "query-cases.json"))
	if err != nil {
		t.Fatalf("read cases: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var cases []struct {
		Name    string    `json:"name"`
		Query   Query     `json:"query"`
		Event   EventData `json:"event"`
		Matches bool      `json:"matches"`
	}
	if err := dec.Decode(&cases); err != nil {
		t.Fatalf("decode cases: %v", err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			if got := c.Query.Matches(c.Event); got != c.Matches {
				t.Errorf("Matches() = %v, want %v", got, c.Matches)
			}
		})
	}
}

func TestQueryItemWithNothingSetMatchesEverything(t *testing.T) {
	// Validate rejects QueryItem{}, but the SQL treats it as matching
	// everything, and the matcher must agree.
	if !NewQuery([]QueryItem{{}}).Matches(EventData{Type: "t"}) {
		t.Errorf("Matches() = false, want true")
	}
}

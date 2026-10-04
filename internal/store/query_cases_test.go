package store

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
)

// queryCase is one entry of testdata/query-cases.json: a query, an event,
// and whether the query selects the event. The file uses the HTTP API's
// own JSON shapes.
type queryCase struct {
	Name    string        `json:"name"`
	Query   dcb.Query     `json:"query"`
	Event   dcb.EventData `json:"event"`
	Matches *bool         `json:"matches"`
}

// TestSharedQueryCases checks every case in testdata/query-cases.json
// against the SQL a query turns into. internal/dcb runs the same file
// against Query.Matches, so both agree on what a query selects.
func TestSharedQueryCases(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "query-cases.json"))
	if err != nil {
		t.Fatalf("read cases: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var cases []queryCase
	if err := dec.Decode(&cases); err != nil {
		t.Fatalf("decode cases: %v", err)
	}

	seen := make(map[string]bool, len(cases))
	events := make([]dcb.EventData, len(cases))
	for i, c := range cases {
		switch {
		case c.Name == "":
			t.Fatalf("case %d has no name", i)
		case seen[c.Name]:
			t.Fatalf("case %q appears twice", c.Name)
		case c.Matches == nil:
			t.Fatalf("case %q has no matches field", c.Name)
		}
		seen[c.Name] = true
		// Every case must be input the server accepts.
		if err := c.Query.Validate(); err != nil {
			t.Fatalf("case %q: invalid query: %v", c.Name, err)
		}
		if err := c.Event.Validate(); err != nil {
			t.Fatalf("case %q: invalid event: %v", c.Name, err)
		}
		events[i] = c.Event
	}

	s := openTestStore(t)
	appended := mustAppend(t, s, events, nil)

	for i, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			// Reading right after the sequence before this case's event
			// makes its event the first candidate.
			seq := appended[i].Sequence
			after := seq - 1
			got, _ := mustReadAll(t, s, ReadFilter{Query: c.Query, AfterSequence: &after, Limit: 1})
			matched := len(got) == 1 && got[0].Sequence == seq
			if matched != *c.Matches {
				t.Errorf("matched = %v, want %v", matched, *c.Matches)
			}
		})
	}
}

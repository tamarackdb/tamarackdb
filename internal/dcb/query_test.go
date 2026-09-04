package dcb

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestQueryAllRoundTrip(t *testing.T) {
	q := QueryAll()
	data, err := json.Marshal(q)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if string(data) != `"*"` {
		t.Errorf("Marshal() = %s, want %q", data, `"*"`)
	}
	var q2 Query
	if err := json.Unmarshal(data, &q2); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if !q2.All() {
		t.Errorf("Unmarshal() All() = false, want true")
	}
}

func TestQueryConcreteRoundTrip(t *testing.T) {
	q := NewQuery([]QueryItem{{Types: []string{"user-created"}}})
	data, err := json.Marshal(q)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var q2 Query
	if err := json.Unmarshal(data, &q2); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if q2.All() {
		t.Errorf("Unmarshal() All() = true, want false")
	}
	if len(q2.Items()) != 1 || q2.Items()[0].Types[0] != "user-created" {
		t.Errorf("Unmarshal() Items() = %+v, want one item with type user-created", q2.Items())
	}
}

func TestQueryUnmarshalJSONErrors(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"non-star string", `"not-a-query"`},
		{"number", `5`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var q Query
			if err := json.Unmarshal([]byte(tt.input), &q); err == nil {
				t.Fatalf("Unmarshal(%s) error = nil, want error", tt.input)
			}
		})
	}
}

func TestQueryValidate(t *testing.T) {
	if err := QueryAll().Validate(); err != nil {
		t.Errorf("QueryAll().Validate() = %v, want nil", err)
	}
	if err := NewQuery(nil).Validate(); !errors.Is(err, ErrEmptyQuery) {
		t.Errorf("NewQuery(nil).Validate() = %v, want ErrEmptyQuery", err)
	}
	if err := NewQuery([]QueryItem{}).Validate(); !errors.Is(err, ErrEmptyQuery) {
		t.Errorf("NewQuery([]).Validate() = %v, want ErrEmptyQuery", err)
	}
	invalid := NewQuery([]QueryItem{{Types: []string{}}})
	if err := invalid.Validate(); !errors.Is(err, ErrEmptyQueryItemArray) {
		t.Errorf("Validate() with invalid item = %v, want ErrEmptyQueryItemArray", err)
	}
}

func TestQueryItemValidate(t *testing.T) {
	tests := []struct {
		name    string
		item    QueryItem
		wantErr bool
	}{
		{"empty item", QueryItem{}, false},
		{"all axes nil", QueryItem{Types: nil, Identifiers: nil, Metadata: nil}, false},
		{"types non-nil non-empty", QueryItem{Types: []string{"t"}}, false},
		{"types empty non-nil", QueryItem{Types: []string{}}, true},
		{"identifiers empty non-nil", QueryItem{Identifiers: []Identifier{}}, true},
		{"metadata empty non-nil", QueryItem{Metadata: []Metadata{}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.item.Validate()
			if tt.wantErr && err == nil {
				t.Errorf("Validate() = nil, want error")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
		})
	}
}

func TestAppendConditionValidate(t *testing.T) {
	seqNeg := int64(-1)
	seqOK := int64(5)
	all := QueryAll()
	emptyQuery := NewQuery(nil)

	tests := []struct {
		name    string
		cond    AppendCondition
		wantErr error
	}{
		{"no condition fields", AppendCondition{}, nil},
		{"afterSequence only", AppendCondition{AfterSequence: &seqOK}, nil},
		{"failIfEventsMatch all", AppendCondition{FailIfEventsMatch: &all}, nil},
		{"failIfEventsMatch empty query", AppendCondition{FailIfEventsMatch: &emptyQuery}, ErrEmptyQuery},
		{"negative afterSequence", AppendCondition{AfterSequence: &seqNeg}, ErrNegativeAfterSequence},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cond.Validate()
			if tt.wantErr == nil {
				if err != nil {
					t.Errorf("Validate() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("Validate() = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

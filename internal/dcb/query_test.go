package dcb

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestQueryStringFormsRoundTrip(t *testing.T) {
	tests := []struct {
		q         Query
		want      string
		all, none bool
	}{
		{QueryAll(), `"all"`, true, false},
		{QueryNone(), `"none"`, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			data, err := json.Marshal(tt.q)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			if string(data) != tt.want {
				t.Errorf("Marshal() = %s, want %s", data, tt.want)
			}
			var q2 Query
			if err := json.Unmarshal(data, &q2); err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}
			if q2.All() != tt.all || q2.None() != tt.none || q2.Items() != nil {
				t.Errorf("Unmarshal() All() = %v, None() = %v, Items() = %v, want %v, %v, nil", q2.All(), q2.None(), q2.Items(), tt.all, tt.none)
			}
			if err := q2.Validate(); err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
		})
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
	if q2.All() || q2.None() {
		t.Errorf("Unmarshal() All() = %v, None() = %v, want false, false", q2.All(), q2.None())
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
		{"unknown string", `"not-a-query"`},
		{"star", `"*"`},
		{"all capitalized", `"All"`},
		{"none in capitals", `"NONE"`},
		{"all with a space", `" all"`},
		{"empty string", `""`},
		{"false", `false`},
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

func TestNewQueryDedupesExactDuplicates(t *testing.T) {
	q := NewQuery([]QueryItem{
		{Identifiers: []Identifier{{Name: "userId", Value: "123"}}},
		{Identifiers: []Identifier{{Name: "userId", Value: "123"}}},
	})
	if len(q.Items()) != 1 {
		t.Errorf("Items() = %+v, want 1 item after deduping exact duplicates", q.Items())
	}
}

func TestNewQueryDedupesRegardlessOfOrder(t *testing.T) {
	q := NewQuery([]QueryItem{
		{
			Types:       []string{"a", "b"},
			Identifiers: []Identifier{{Name: "userId", Value: "123"}, {Name: "orgId", Value: "9"}},
		},
		{
			Types:       []string{"b", "a"},
			Identifiers: []Identifier{{Name: "orgId", Value: "9"}, {Name: "userId", Value: "123"}},
		},
	})
	if len(q.Items()) != 1 {
		t.Errorf("Items() = %+v, want 1 item, reordered types/identifiers should still be a duplicate", q.Items())
	}
}

func TestNewQueryKeepsDistinctItems(t *testing.T) {
	q := NewQuery([]QueryItem{
		{Identifiers: []Identifier{{Name: "userId", Value: "123"}}},
		{Identifiers: []Identifier{{Name: "userId", Value: "456"}}},
		{Types: []string{"user-created"}},
	})
	if len(q.Items()) != 3 {
		t.Errorf("Items() = %+v, want 3 distinct items kept", q.Items())
	}
}

func TestQueryUnmarshalJSONDedupes(t *testing.T) {
	var q Query
	input := `[{"identifiers":[{"name":"userId","value":"123"}]},{"identifiers":[{"name":"userId","value":"123"}]}]`
	if err := json.Unmarshal([]byte(input), &q); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(q.Items()) != 1 {
		t.Errorf("Items() = %+v, want 1 item after deduping", q.Items())
	}
}

func TestQueryItemValidate(t *testing.T) {
	tests := []struct {
		name    string
		item    QueryItem
		wantErr bool
	}{
		{"empty item invalid", QueryItem{}, true},
		{"all axes nil", QueryItem{Types: nil, Identifiers: nil, Metadata: nil}, true},
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
	if err := (QueryItem{}).Validate(); !errors.Is(err, ErrEmptyQueryItem) {
		t.Errorf("QueryItem{}.Validate() = %v, want ErrEmptyQueryItem", err)
	}
}

func TestAppendConditionValidate(t *testing.T) {
	seqNeg := int64(-1)
	seqOK := int64(5)
	all := QueryAll()
	none := QueryNone()
	emptyQuery := NewQuery(nil)
	itemQuery := NewQuery([]QueryItem{{}})

	tests := []struct {
		name    string
		cond    AppendCondition
		wantErr error
	}{
		{"no condition fields", AppendCondition{}, nil},
		{"afterSequence only", AppendCondition{AfterSequence: &seqOK}, nil},
		{"failIfEventsMatch all", AppendCondition{FailIfEventsMatch: &all}, nil},
		{"failIfEventsMatch none", AppendCondition{FailIfEventsMatch: &none, AfterSequence: &seqOK}, nil},
		{"failIfEventsMatch empty query", AppendCondition{FailIfEventsMatch: &emptyQuery}, ErrEmptyQuery},
		{"failIfEventsMatch with empty item", AppendCondition{FailIfEventsMatch: &itemQuery}, ErrEmptyQueryItem},
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

func TestAppendConditionValidateStore(t *testing.T) {
	seq := int64(5)
	all := QueryAll()
	const store = "5b0c7e2a-1f4d-4a9b-8c3e-6d2f1a0b9e47"

	tests := []struct {
		name    string
		cond    AppendCondition
		wantErr error
	}{
		{"no condition fields", AppendCondition{}, nil},
		{"failIfEventsMatch only", AppendCondition{FailIfEventsMatch: &all}, nil},
		{"afterSequence with store", AppendCondition{AfterSequence: &seq, Store: store}, nil},
		{"afterSequence without store", AppendCondition{AfterSequence: &seq}, ErrMissingStore},
		{"store without afterSequence", AppendCondition{FailIfEventsMatch: &all, Store: store}, ErrUnexpectedStore},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cond.ValidateStore()
			if tt.wantErr == nil {
				if err != nil {
					t.Errorf("ValidateStore() = %v, want nil", err)
				}
				return
			}
			var ve *ValidationError
			if !errors.Is(err, tt.wantErr) || !errors.As(err, &ve) {
				t.Errorf("ValidateStore() = %v, want a *ValidationError wrapping %v", err, tt.wantErr)
			}
		})
	}
}

func TestAppendConditionStoreJSON(t *testing.T) {
	var c AppendCondition
	if err := json.Unmarshal([]byte(`{"afterSequence":5,"store":"abc"}`), &c); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if c.Store != "abc" {
		t.Errorf("Store = %q, want abc", c.Store)
	}
	data, err := json.Marshal(AppendCondition{})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if string(data) != "{}" {
		t.Errorf("Marshal(AppendCondition{}) = %s, want {}", data)
	}
}

func TestQueryValidateSizeLimits(t *testing.T) {
	item := func(n int) QueryItem {
		types := make([]string, n)
		for i := range types {
			types[i] = fmt.Sprint("t", i)
		}
		return QueryItem{Types: types}
	}
	items := func(n int) []QueryItem {
		out := make([]QueryItem, n)
		for i := range out {
			out[i] = QueryItem{Types: []string{fmt.Sprint("t", i)}}
		}
		return out
	}

	if err := NewQuery(items(MaxQueryItems)).Validate(); err != nil {
		t.Errorf("%d items: Validate() error = %v, want nil", MaxQueryItems, err)
	}
	if err := NewQuery(items(MaxQueryItems + 1)).Validate(); !errors.Is(err, ErrQueryTooLarge) {
		t.Errorf("%d items: Validate() error = %v, want ErrQueryTooLarge", MaxQueryItems+1, err)
	}
	if err := NewQuery([]QueryItem{item(MaxQueryItemValues)}).Validate(); err != nil {
		t.Errorf("%d values: Validate() error = %v, want nil", MaxQueryItemValues, err)
	}

	mixed := item(MaxQueryItemValues)
	mixed.Metadata = []Metadata{{Name: "tenantId", Value: "acme"}}
	if err := NewQuery([]QueryItem{mixed}).Validate(); !errors.Is(err, ErrQueryTooLarge) {
		t.Errorf("%d values across types and metadata: Validate() error = %v, want ErrQueryTooLarge", MaxQueryItemValues+1, err)
	}
}

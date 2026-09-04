package dcb

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestIdentifierSetMarshalJSON(t *testing.T) {
	tests := []struct {
		name string
		set  IdentifierSet
		want string
	}{
		{"empty", nil, `{}`},
		{"single value", IdentifierSet{{Name: "otherId", Value: "baz"}}, `{"otherId":"baz"}`},
		{"multiple values same name", IdentifierSet{
			{Name: "courseId", Value: "foo"},
			{Name: "courseId", Value: "bar"},
		}, `{"courseId":["foo","bar"]}`},
		{"multiple names sorted", IdentifierSet{
			{Name: "otherId", Value: "baz"},
			{Name: "courseId", Value: "foo"},
		}, `{"courseId":"foo","otherId":"baz"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.set)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("Marshal() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestIdentifierSetUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    map[string][]string
		wantErr bool
	}{
		{"string value", `{"otherId":"baz"}`, map[string][]string{"otherId": {"baz"}}, false},
		{"array value", `{"courseId":["foo","bar"]}`, map[string][]string{"courseId": {"foo", "bar"}}, false},
		{"mixed", `{"courseId":["foo","bar"],"otherId":"baz"}`, map[string][]string{"courseId": {"foo", "bar"}, "otherId": {"baz"}}, false},
		{"non-string array element", `{"courseId":[1,2]}`, nil, true},
		{"empty array value", `{"courseId":[]}`, nil, true},
		{"non-object top level", `["a","b"]`, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s IdentifierSet
			err := json.Unmarshal([]byte(tt.input), &s)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Unmarshal() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}
			got := map[string][]string{}
			for _, id := range s {
				got[id.Name] = append(got[id.Name], id.Value)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("Unmarshal() = %+v, want %+v", got, tt.want)
			}
			for name, wantVals := range tt.want {
				gotVals := got[name]
				if len(gotVals) != len(wantVals) {
					t.Errorf("name %q: got %v, want %v", name, gotVals, wantVals)
					continue
				}
				for i, v := range wantVals {
					if gotVals[i] != v {
						t.Errorf("name %q: got %v, want %v", name, gotVals, wantVals)
						break
					}
				}
			}
		})
	}
}

func TestIdentifierSetRoundTrip(t *testing.T) {
	original := IdentifierSet{
		{Name: "courseId", Value: "foo"},
		{Name: "courseId", Value: "bar"},
		{Name: "otherId", Value: "baz"},
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var roundTripped IdentifierSet
	if err := json.Unmarshal(data, &roundTripped); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if !sameSet(original, roundTripped) {
		t.Errorf("round trip mismatch: got %+v, want %+v (order-independent)", roundTripped, original)
	}
}

func sameSet[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	counts := map[T]int{}
	for _, v := range a {
		counts[v]++
	}
	for _, v := range b {
		counts[v]--
	}
	for _, c := range counts {
		if c != 0 {
			return false
		}
	}
	return true
}

func TestEventDataValidate(t *testing.T) {
	makeIdentifiers := func(n int) IdentifierSet {
		out := make(IdentifierSet, n)
		for i := range out {
			out[i] = Identifier{Name: "id", Value: string(rune('a' + i))}
		}
		return out
	}
	makeMetadata := func(n int) MetadataSet {
		out := make(MetadataSet, n)
		for i := range out {
			out[i] = Metadata{Name: "md", Value: string(rune('a' + i))}
		}
		return out
	}

	tests := []struct {
		name    string
		event   EventData
		wantErr error
	}{
		{"valid minimal event", EventData{Type: "user-created"}, nil},
		{"missing type", EventData{}, ErrMissingType},
		{"exactly 20 identifiers", EventData{Type: "t", Identifiers: makeIdentifiers(20)}, nil},
		{"21 identifiers", EventData{Type: "t", Identifiers: makeIdentifiers(21)}, ErrTooManyIdentifiers},
		{"exactly 20 metadata", EventData{Type: "t", Metadata: makeMetadata(20)}, nil},
		{"21 metadata", EventData{Type: "t", Metadata: makeMetadata(21)}, ErrTooManyMetadata},
		{"duplicate identifier", EventData{Type: "t", Identifiers: IdentifierSet{
			{Name: "userId", Value: "123"}, {Name: "userId", Value: "123"},
		}}, ErrDuplicateIdentifier},
		{"duplicate metadata", EventData{Type: "t", Metadata: MetadataSet{
			{Name: "tenantId", Value: "acme"}, {Name: "tenantId", Value: "acme"},
		}}, ErrDuplicateMetadata},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.event.Validate()
			if tt.wantErr == nil {
				if err != nil {
					t.Errorf("Validate() error = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("Validate() error = %v, want %v", err, tt.wantErr)
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Errorf("Validate() error is not a *ValidationError: %v", err)
			}
		})
	}
}

func TestEventDataSize(t *testing.T) {
	e := EventData{
		Type:    "abc",
		Payload: "xyz",
	}
	if got, want := e.Size(), 6; got != want {
		t.Errorf("Size() = %d, want %d", got, want)
	}

	// "é" is 2 bytes in UTF-8; Size must count bytes, not runes.
	e2 := EventData{Type: "é"}
	if got, want := e2.Size(), 2; got != want {
		t.Errorf("Size() = %d, want %d (multi-byte char counted as bytes)", got, want)
	}
}

func TestEventMarshalJSON(t *testing.T) {
	tm := time.Date(2026, 9, 1, 14, 23, 5, 123000000, time.UTC)
	e := Event{
		Sequence: 12346,
		Time:     tm,
		EventData: EventData{
			Type:        "user-created",
			Identifiers: IdentifierSet{{Name: "userId", Value: "123"}},
			Metadata:    MetadataSet{{Name: "tenantId", Value: "acme"}},
			Payload:     "...",
		},
	}
	got, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	want := `{"sequence":12346,"time":"2026-09-01T14:23:05.123000Z","type":"user-created","identifiers":{"userId":"123"},"metadata":{"tenantId":"acme"},"payload":"..."}`
	if string(got) != want {
		t.Errorf("Marshal() = %s, want %s", got, want)
	}
}

func TestEventUnmarshalJSON(t *testing.T) {
	input := `{"sequence":12346,"time":"2026-09-01T14:23:05.123456Z","type":"user-created","identifiers":{"userId":"123"},"metadata":{"tenantId":"acme"},"payload":"..."}`
	var e Event
	if err := json.Unmarshal([]byte(input), &e); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if e.Sequence != 12346 {
		t.Errorf("Sequence = %d, want 12346", e.Sequence)
	}
	wantTime := time.Date(2026, 9, 1, 14, 23, 5, 123456000, time.UTC)
	if !e.Time.Equal(wantTime) {
		t.Errorf("Time = %v, want %v", e.Time, wantTime)
	}
	if e.Type != "user-created" {
		t.Errorf("Type = %q, want %q", e.Type, "user-created")
	}
}

func TestEventUnmarshalJSONInvalidTime(t *testing.T) {
	input := `{"sequence":1,"time":"not-a-time","type":"t","identifiers":{},"metadata":{},"payload":""}`
	var e Event
	if err := json.Unmarshal([]byte(input), &e); err == nil {
		t.Fatalf("Unmarshal() error = nil, want error")
	}
}

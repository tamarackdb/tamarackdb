package projection

import (
	"errors"
	"testing"
)

func strPtr(s string) *string { return &s }

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		v       interface{ Validate() error }
		wantErr error
	}{
		{"create", Create{Type: "user-profile", ID: "123", Payload: strPtr("...")}, nil},
		{"create with empty payload", Create{Type: "user-profile", ID: "123", Payload: strPtr("")}, nil},
		{"create missing type", Create{ID: "123", Payload: strPtr("...")}, ErrMissingType},
		{"create missing id", Create{Type: "user-profile", Payload: strPtr("...")}, ErrMissingID},
		{"create missing payload", Create{Type: "user-profile", ID: "123"}, ErrMissingPayload},
		{"replace", Replace{Type: "user-profile", ID: "123", Version: "v", Payload: strPtr("...")}, nil},
		{"replace missing version", Replace{Type: "user-profile", ID: "123", Payload: strPtr("...")}, ErrMissingVersion},
		{"replace missing payload", Replace{Type: "user-profile", ID: "123", Version: "v"}, ErrMissingPayload},
		{"replace missing type", Replace{ID: "123", Version: "v", Payload: strPtr("...")}, ErrMissingType},
		{"delete", Delete{Type: "user-profile", ID: "123", Version: "v"}, nil},
		{"delete missing version", Delete{Type: "user-profile", ID: "123"}, ErrMissingVersion},
		{"delete missing id", Delete{Type: "user-profile", Version: "v"}, ErrMissingID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.v.Validate()
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

func TestWritesLen(t *testing.T) {
	w := Writes{Create: make([]Create, 2), Replace: make([]Replace, 3), Delete: make([]Delete, 1)}
	if got := w.Len(); got != 6 {
		t.Errorf("Len() = %d, want 6", got)
	}
}

package document

import (
	"errors"
	"testing"
)

func strPtr(s string) *string { return &s }
func verPtr(v int64) *int64   { return &v }

func TestDataValidate(t *testing.T) {
	tests := []struct {
		name    string
		doc     Data
		wantErr error
	}{
		{"create (no version)", Data{Type: "user-profile", ID: "123", Payload: strPtr("...")}, nil},
		{"update (version present)", Data{Type: "user-profile", ID: "123", Payload: strPtr("..."), Version: verPtr(5)}, nil},
		{"delete (version present)", Data{Type: "user-profile", ID: "123", Version: verPtr(3)}, nil},
		{"missing type", Data{ID: "123", Payload: strPtr("...")}, ErrMissingType},
		{"missing id", Data{Type: "user-profile", Payload: strPtr("...")}, ErrMissingID},
		{"version zero", Data{Type: "user-profile", ID: "123", Payload: strPtr("..."), Version: verPtr(0)}, ErrInvalidVersion},
		{"negative version", Data{Type: "user-profile", ID: "123", Payload: strPtr("..."), Version: verPtr(-1)}, ErrInvalidVersion},
		{"delete without version", Data{Type: "user-profile", ID: "123"}, ErrDeleteWithoutVersion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.doc.Validate()
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

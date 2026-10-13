package api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthRejectsEveryRoute(t *testing.T) {
	srv, _, _ := newTestServer(t)

	routes := []struct {
		method, path, body string
	}{
		// Before POST /tx: a valid /pause would wait for the transaction
		// that POST /tx begins.
		{"POST", "/pause", ""},
		{"POST", "/resume", ""},
		{"QUERY", "/events", `{"query":"all"}`},
		{"POST", "/projections", `{"create":[{"type":"p","id":"1","payload":""}]}`},
		{"QUERY", "/projections", `{"type":"user-profile","id":"123"}`},
		{"DELETE", "/projections/user-profile", ""},
		{"DELETE", "/projections", ""},
		{"GET", "/health", ""},
		{"GET", "/stats", ""},
		{"POST", "/tx", ""},
		{"QUERY", "/tx/x/events", `{"query":"none"}`},
		{"POST", "/tx/x/events", `{"events":[]}`},
		{"QUERY", "/tx/x/projections", `{"type":"p","id":"1"}`},
		{"POST", "/tx/x/projections", `{"delete":[{"type":"p","id":"1"}]}`},
		{"POST", "/tx/x/commit", ""},
		{"DELETE", "/tx/x", ""},
	}

	for _, route := range routes {
		t.Run(route.method+" "+route.path+"/no header", func(t *testing.T) {
			req := httptest.NewRequest(route.method, route.path, strings.NewReader(route.body))
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			if rec.Code != 401 {
				t.Errorf("status = %d, want 401", rec.Code)
			}
		})
		t.Run(route.method+" "+route.path+"/wrong token", func(t *testing.T) {
			req := httptest.NewRequest(route.method, route.path, strings.NewReader(route.body))
			req.Header.Set("Authorization", "Bearer wrong-token")
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			if rec.Code != 401 {
				t.Errorf("status = %d, want 401", rec.Code)
			}
		})
		t.Run(route.method+" "+route.path+"/valid token not 401", func(t *testing.T) {
			req := httptest.NewRequest(route.method, route.path, strings.NewReader(route.body))
			req.Header.Set("Authorization", "Bearer "+testToken)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			if rec.Code == 401 {
				t.Errorf("status = 401 with a valid token, want anything else")
			}
		})
	}
}

func TestBearerSchemeIsCaseInsensitive(t *testing.T) {
	srv, _, _ := newTestServer(t)
	for _, scheme := range []string{"Bearer", "bearer", "BEARER"} {
		req := httptest.NewRequest("GET", "/health", nil)
		req.Header.Set("Authorization", scheme+" "+testToken)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Errorf("%s: status = %d, want 200", scheme, rec.Code)
		}
	}
}

func TestTokenThatIsAPrefixIsRejected(t *testing.T) {
	srv, _, _ := newTestServer(t)
	for _, token := range []string{testToken[:len(testToken)-1], testToken + "x", ""} {
		req := httptest.NewRequest("GET", "/health", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != 401 {
			t.Errorf("token %q: status = %d, want 401", token, rec.Code)
		}
	}
}

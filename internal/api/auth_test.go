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
		{"QUERY", "/events", `{"query":"all"}`},
		{"POST", "/projections", `{"create":[{"type":"p","id":"1","payload":""}]}`},
		{"GET", "/projections/user-profile/123", ""},
		{"DELETE", "/projections/user-profile", ""},
		{"DELETE", "/projections", ""},
		{"GET", "/health", ""},
		{"GET", "/stats", ""},
		{"POST", "/tx", ""},
		{"QUERY", "/tx/x/events", `{"query":"none"}`},
		{"POST", "/tx/x/events", `{"events":[]}`},
		{"GET", "/tx/x/projections/p/1", ""},
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

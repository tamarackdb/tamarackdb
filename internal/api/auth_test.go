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
		{"QUERY", "/events", `{"query":"*"}`},
		{"POST", "/events", `{"events":[{"type":"t","identifiers":{},"metadata":{},"payload":""}]}`},
		{"POST", "/commit", ""},
		{"POST", "/rollback", ""},
		{"GET", "/projections/user-profile/123", ""},
		{"POST", "/projections", `{"create":[{"type":"t","id":"1","payload":"x"}]}`},
		{"DELETE", "/projections", ""},
		{"GET", "/health", ""},
		{"GET", "/metrics", ""},
		{"GET", "/debug", ""},
		// /begin comes last: the transaction the valid-token call opens
		// would hold up every later write without a ticket.
		{"POST", "/begin", ""},
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

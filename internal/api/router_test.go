package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAuthFailsClosedWhenTokenMissing(t *testing.T) {
	s := Server{AuthRequired: true, Drivers: func() []string { return []string{"cisco_iosxe"} }, StartedAt: time.Now()}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/drivers", nil)
	rr := httptest.NewRecorder()
	s.Router().ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503", rr.Code)
	}
}

func TestAuthUsesBearerToken(t *testing.T) {
	s := Server{AuthRequired: true, BootstrapToken: "secret-token", Drivers: func() []string { return []string{"cisco_iosxe"} }, StartedAt: time.Now()}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/drivers", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rr := httptest.NewRecorder()
	s.Router().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token status=%d, want 401", rr.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/drivers", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	rr = httptest.NewRecorder()
	s.Router().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("correct token status=%d, want 200", rr.Code)
	}
}

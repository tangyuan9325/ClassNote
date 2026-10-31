package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"classnote/internal/config"
)

func newTestConfig(t *testing.T, token string) *config.Store {
	t.Helper()
	st, err := config.Load(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if _, err := st.Update(func(c *config.Config) error {
		c.Server.Access = "lan"
		c.Server.Token = token
		return nil
	}); err != nil {
		t.Fatalf("update config: %v", err)
	}
	return st
}

func TestAuthMiddleware(t *testing.T) {
	s := &Server{cfg: newTestConfig(t, "cn_testtoken_123456")}
	h := s.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	cases := []struct {
		name    string
		remote  string
		target  string
		header  string
		cookie  string
		want    int
		wantSet bool
	}{
		{name: "loopback allowed", remote: "127.0.0.1:5555", target: "/api/status", want: 200},
		{name: "ipv6 loopback allowed", remote: "[::1]:5555", target: "/api/status", want: 200},
		{name: "lan without token", remote: "192.168.1.20:5000", target: "/api/status", want: 401},
		{name: "lan with header", remote: "192.168.1.20:5000", target: "/api/status", header: "cn_testtoken_123456", want: 200},
		{name: "lan with query sets cookie", remote: "192.168.1.20:5000", target: "/?token=cn_testtoken_123456", want: 200, wantSet: true},
		{name: "lan with cookie", remote: "192.168.1.20:5000", target: "/api/status", cookie: "cn_testtoken_123456", want: 200},
		{name: "lan wrong token", remote: "192.168.1.20:5000", target: "/api/status", header: "wrong", want: 401},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, c.target, nil)
			req.RemoteAddr = c.remote
			if c.header != "" {
				req.Header.Set("X-ClassNote-Token", c.header)
			}
			if c.cookie != "" {
				req.AddCookie(&http.Cookie{Name: "cn_token", Value: c.cookie})
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != c.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, c.want, rec.Body.String())
			}
			if c.wantSet {
				found := false
				for _, ck := range rec.Result().Cookies() {
					if ck.Name == "cn_token" && ck.Value == "cn_testtoken_123456" {
						found = true
					}
				}
				if !found {
					t.Fatal("expected cn_token cookie to be set")
				}
			}
		})
	}
}

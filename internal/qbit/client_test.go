package qbit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cleanarr/internal/config"
)

// server mimics qBittorrent's login. newStyle answers like qBittorrent 5.1+:
// 204 on success and 401 for requests without a session.
func server(newStyle bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_ = r.ParseForm()
			if r.PostForm.Get("password") != "secret" {
				if newStyle {
					w.WriteHeader(http.StatusUnauthorized)
				}
				_, _ = w.Write([]byte("Fails."))
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "s", Path: "/"})
			if newStyle {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/app/version":
			if c, err := r.Cookie("SID"); err != nil || c.Value != "s" {
				if newStyle {
					w.WriteHeader(http.StatusUnauthorized)
				} else {
					w.WriteHeader(http.StatusForbidden)
				}
				return
			}
			_, _ = w.Write([]byte("v5.1.0"))
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestLogin(t *testing.T) {
	for _, newStyle := range []bool{false, true} {
		srv := server(newStyle)
		defer srv.Close()

		c := New(config.QbitInstance{Name: "q", URL: srv.URL, Username: "admin", Password: "secret"})
		v, err := c.Version(context.Background())
		if err != nil || v != "v5.1.0" {
			t.Errorf("newStyle=%v: version %q, err %v", newStyle, v, err)
		}

		c = New(config.QbitInstance{Name: "q", URL: srv.URL, Username: "admin", Password: "wrong"})
		if _, err := c.Version(context.Background()); err == nil || !strings.Contains(err.Error(), "login failed") {
			t.Errorf("newStyle=%v: wrong password gave %v, want login failure", newStyle, err)
		}
	}
}

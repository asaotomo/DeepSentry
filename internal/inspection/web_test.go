package inspection

import (
	"ai-edr/internal/config"
	"bytes"
	"context"
	"fmt"
	"image"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestHeadlessLoginEvidenceIntegration(t *testing.T) {
	if os.Getenv("DEEPSENTRY_INSPECTION_BROWSER_TEST") != "1" {
		t.Skip("explicit opt-in for local Chrome integration")
	}
	if browserPath("") == "" {
		t.Fatal("Chrome missing")
	}
	t.Setenv("INSPECTION_FIXTURE_USER", "test-user")
	t.Setenv("INSPECTION_FIXTURE_PASSWORD", "test-password")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" && r.Method == "POST" {
			if r.FormValue("user") != "test-user" || r.FormValue("password") != "test-password" {
				http.Error(w, "bad login", 403)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "test-session", HttpOnly: true, Path: "/"})
			http.Redirect(w, r, "/dashboard", 303)
			return
		}
		if r.URL.Path == "/dashboard" {
			cookie, e := r.Cookie("session")
			if e != nil || cookie.Value != "test-session" {
				http.Redirect(w, r, "/", 302)
				return
			}
			fmt.Fprint(w, `<html><body><nav id="ready">Fixture device</nav><section id="cpu" style="background:white;color:black;width:500px;height:150px;font:24px sans-serif">CPU: 12<br>Alarm count: 0</section></body></html>`)
			return
		}
		fmt.Fprint(w, `<html><body><form method="post" action="/login"><input id="user" name="user"><input id="password" type="password" name="password"><button id="submit" type="submit">Login</button></form></body></html>`)
	}))
	defer server.Close()
	d := config.InspectionDevice{Name: "fixture", URL: server.URL, UsernameEnv: "INSPECTION_FIXTURE_USER", PasswordEnv: "INSPECTION_FIXTURE_PASSWORD", UsernameSelector: "#user", PasswordSelector: "#password", SubmitSelector: "#submit", ReadySelector: "#ready"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	collect, closeBrowser, err := newWebCollector(ctx, "", d)
	defer closeBrowser()
	if err != nil {
		t.Fatal(err)
	}
	text, png, err := collect(ctx, d, config.InspectionCheck{Name: "cpu", Selector: "#cpu", Screenshot: true})
	if err != nil || text == "" {
		t.Fatalf("%q %v", text, err)
	}
	size, kind, err := image.DecodeConfig(bytes.NewReader(png))
	if err != nil || kind != "png" || size.Width < 100 {
		t.Fatal("screenshot missing")
	}
}

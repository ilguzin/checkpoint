package core

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestAccountLanguageIsolation(t *testing.T) {
	s := setup(t)
	admin(t, s.store)
	if _, e := s.store.CreateLocalUser("second", "another long password", false); e != nil {
		t.Fatal(e)
	}
	_, first, e := s.store.Login("admin", "correct horse battery staple")
	if e != nil {
		t.Fatal(e)
	}
	_, second, e := s.store.Login("second", "another long password")
	if e != nil {
		t.Fatal(e)
	}
	h := s.Handler(http.NotFoundHandler())
	for _, locale := range []string{"ru", "en", "auto"} {
		w := accountRequest(h, "POST", "/api/account/preferences", first, `{"uiLocale":"`+locale+`"}`, s.cfg.PublicOrigin)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		u, ok := s.store.Account(first)
		if !ok || u.UILocale != locale {
			t.Fatal("preference not persisted", u)
		}
		other, _ := s.store.Account(second)
		if other.UILocale != "auto" {
			t.Fatal("another account changed", other)
		}
	}
	for _, body := range []string{`{"uiLocale":"fr"}`, `{"uiLocale":"ru","id":"second"}`, `{}`} {
		if w := accountRequest(h, "POST", "/api/account/preferences", first, body, s.cfg.PublicOrigin); w.Code != 400 {
			t.Fatal(w.Code, body)
		}
	}
	if w := accountRequest(h, "POST", "/api/account/preferences", first, `{"uiLocale":"ru"}`, "https://evil.example"); w.Code != 403 {
		t.Fatal("CSRF allowed")
	}
	w := accountRequest(h, "POST", "/api/account/preferences", "", `{"uiLocale":"ru"}`, s.cfg.PublicOrigin)
	var out map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 401 || out["code"] != "sign_in_required" {
		t.Fatal("guest or unstable code", w.Body.String())
	}
}

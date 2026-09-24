package anchor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestServiceLifecycle(t *testing.T) {
	directory := t.TempDir()
	store, err := OpenStore(filepath.Join(directory, "anchor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	web := filepath.Join(directory, "web")
	if err := os.Mkdir(web, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("admin app"), 0600); err != nil {
		t.Fatal(err)
	}
	handler := NewServer(store, web, true)
	call := func(method, path, body, token string, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			r.Header.Set("X-CSRF-Token", token)
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	assertCode := func(w *httptest.ResponseRecorder, code int) {
		t.Helper()
		if w.Code != code {
			t.Fatalf("want %d, got %d: %s", code, w.Code, w.Body.String())
		}
	}

	root := call("GET", "/", "", "", nil)
	assertCode(root, 200)
	if strings.Contains(root.Body.String(), "admin app") {
		t.Fatal("root displayed the management application")
	}
	assertCode(call("GET", "/admin/settings", "", "", nil), 200)
	assertCode(call("POST", "/api/links", `{}`, "", nil), 401)

	setup := call("POST", "/auth/setup", `{"username":"owner","password":"long-password-123"}`, "", nil)
	assertCode(setup, 200)
	cookie := setup.Result().Cookies()[0]
	var auth struct {
		CSRF string `json:"csrf"`
	}
	if err := json.Unmarshal(setup.Body.Bytes(), &auth); err != nil || auth.CSRF == "" {
		t.Fatal("setup did not issue a CSRF token", err)
	}
	assertCode(call("POST", "/auth/setup", `{"username":"other","password":"long-password-123"}`, "", nil), 409)
	assertCode(call("POST", "/api/links", `{}`, "", cookie), 403)
	assertCode(call("POST", "/api/links", `{"code":"API","destination":"https://example.org"}`, auth.CSRF, cookie), 400)
	assertCode(call("POST", "/api/links", `{"code":"bad","destination":"javascript:alert(1)"}`, auth.CSRF, cookie), 400)

	assertCode(call("POST", "/api/links", `{"destination":"https://example.org","note":"`+strings.Repeat("a", 501)+`"}`, auth.CSRF, cookie), 400)
	created := call("POST", "/api/links", `{"code":"hello1","destination":"https://example.org/page","note":"  项目文档  "}`, auth.CSRF, cookie)
	assertCode(created, 201)
	var first Link
	if err := json.Unmarshal(created.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if first.Note != "项目文档" {
		t.Fatalf("created note was not saved: %q", first.Note)
	}
	assertCode(call("PATCH", "/api/links/"+first.ID, `{"note":"更新后的用途"}`, "", cookie), 403)
	assertCode(call("PATCH", "/api/links/"+first.ID, `{"note":"更新后的用途"}`, auth.CSRF, cookie), 204)
	listed := call("GET", "/api/links", "", "", cookie)
	assertCode(listed, 200)
	var saved []Link
	if err := json.Unmarshal(listed.Body.Bytes(), &saved); err != nil || len(saved) != 1 || saved[0].Note != "更新后的用途" {
		t.Fatalf("updated note not returned in list: %v, %s", err, listed.Body.String())
	}
	assertCode(call("PATCH", "/api/links/"+first.ID, `{"note":""}`, auth.CSRF, cookie), 204)
	assertCode(call("PATCH", "/api/links/"+first.ID, `{"note":"x","expiresAt":null}`, auth.CSRF, cookie), 400)
	redirect := call("GET", "/hello1", "", "", nil)
	assertCode(redirect, 302)
	if redirect.Header().Get("Location") != "https://example.org/page" {
		t.Fatal("wrong redirect destination")
	}
	if redirect.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("mutable redirect was cacheable")
	}

	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	scheduled := call("POST", "/api/links", `{"code":"later1","destination":"https://example.org","startsAt":"`+future+`"}`, auth.CSRF, cookie)
	assertCode(scheduled, 201)
	assertCode(call("GET", "/later1", "", "", nil), 404)

	start := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	end := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	expired := call("POST", "/api/links", `{"code":"old1","destination":"https://example.org/old","startsAt":"`+start+`","expiresAt":"`+end+`"}`, auth.CSRF, cookie)
	assertCode(expired, 201)
	assertCode(call("GET", "/old1", "", "", nil), 404)
	reused := call("POST", "/api/links", `{"code":"old1","destination":"https://example.org/new"}`, auth.CSRF, cookie)
	assertCode(reused, 201)
	if call("GET", "/old1", "", "", nil).Header().Get("Location") != "https://example.org/new" {
		t.Fatal("reused code did not resolve to the new destination")
	}

	settings := `{"minLength":6,"maxLength":32,"excludeSimilar":true,"reuseCodes":false}`
	assertCode(call("PUT", "/api/settings", settings, auth.CSRF, cookie), 200)
	assertCode(call("DELETE", "/api/links/"+first.ID, "", auth.CSRF, cookie), 204)
	assertCode(call("GET", "/hello1", "", "", nil), 404)
	assertCode(call("POST", "/api/links", `{"code":"hello1","destination":"https://example.org"}`, auth.CSRF, cookie), 409)

	assertCode(call("POST", "/auth/logout", "", auth.CSRF, cookie), 204)
	assertCode(call("GET", "/api/links", "", "", cookie), 401)
	login := call("POST", "/auth/login", `{"username":"owner","password":"long-password-123"}`, "", nil)
	assertCode(login, 200)
}

func TestBatchIsAtomic(t *testing.T) {
	directory := t.TempDir()
	store, err := OpenStore(filepath.Join(directory, "anchor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	first, err := store.CreateLink(Link{Destination: "https://example.org", CreatedAt: time.Now().UTC(), StartsAt: time.Now().UTC()}, "first1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteLinks([]string{first.ID, strings.Repeat("0", 32)}); err != ErrNotFound {
		t.Fatalf("expected missing-link error, got %v", err)
	}
	if _, err := store.ActiveLink("first1"); err != nil {
		t.Fatal("first link was deleted despite batch failure", err)
	}
}

func TestGeneratedCodeAndSettings(t *testing.T) {
	directory := t.TempDir()
	store, err := OpenStore(filepath.Join(directory, "anchor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.UpdateSettings(Settings{MinLength: 1, MaxLength: 4, ExcludeSimilar: true, ReuseCodes: true}); err != nil {
		t.Fatal(err)
	}
	link, err := store.CreateLink(Link{Destination: "https://example.org", CreatedAt: time.Now().UTC(), StartsAt: time.Now().UTC()}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(link.Code) != 1 || strings.ContainsAny(link.Code, "0O1il") {
		t.Fatalf("unexpected generated code %q", link.Code)
	}
}

func TestDataPersistsAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "anchor.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAccount("owner", "stored-hash"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateSettings(Settings{MinLength: 4, MaxLength: 12, ReuseCodes: false}); err != nil {
		t.Fatal(err)
	}
	_, err = store.CreateLink(Link{Destination: "https://example.org", Note: "重启后保留", CreatedAt: time.Now().UTC(), StartsAt: time.Now().UTC()}, "persist1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	account, found, err := store.Account()
	if err != nil || !found || account.Username != "owner" {
		t.Fatalf("account did not persist: %v", err)
	}
	settings, err := store.Settings()
	if err != nil || settings.MaxLength != 12 || settings.ReuseCodes {
		t.Fatalf("settings did not persist: %v", err)
	}
	link, err := store.ActiveLink("persist1")
	if err != nil {
		t.Fatalf("short link did not persist: %v", err)
	}
	if link.Note != "重启后保留" {
		t.Fatalf("note did not persist: %q", link.Note)
	}
}

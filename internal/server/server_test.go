package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"buku-journal/internal/journal"
)

const testToken = "a-very-long-private-access-key-for-tests-123456789"
const entryName = "2026-10-23-산책.md"

func testServer(t *testing.T) (*Server, *journal.Repository) {
	t.Helper()
	jj, err := exec.LookPath("jj")
	if err != nil {
		t.Skip("jj not installed")
	}
	repo, err := journal.Open(filepath.Join(t.TempDir(), "journal"), jj)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.Apply(map[string][]byte{}, map[string][]byte{entryName: []byte("개인적인 기억\n"), "카페.txt": []byte("카페\n서울\nhttps://example.com\n")}, "가져오기"); err != nil {
		t.Fatal(err)
	}
	return New(repo, nil, testToken), repo
}
func request(s *Server, method, path string, body any, auth bool, headers map[string]string) *httptest.ResponseRecorder {
	var encoded []byte
	if body != nil {
		encoded, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, "https://journal.example"+path, bytes.NewReader(encoded))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth {
		req.Header.Set("Authorization", "Bearer "+testToken)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	out := httptest.NewRecorder()
	s.ServeHTTP(out, req)
	return out
}
func TestPrivateEndpointsRequireAuthentication(t *testing.T) {
	s, _ := testServer(t)
	for _, path := range []string{"/api/entries", "/api/history", "/api/places/" + url.PathEscape("카페.txt"), "/api/jobs"} {
		r := request(s, "GET", path, nil, false, nil)
		if r.Code != 401 || strings.Contains(r.Body.String(), "개인적인 기억") {
			t.Fatalf("private endpoint %s returned %d %s", path, r.Code, r.Body.String())
		}
		if r.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("private response is cacheable")
		}
	}
	r := request(s, "GET", "/api/entries", nil, true, nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), "개인적인 기억") {
		t.Fatalf("bearer auth failed: %d %s", r.Code, r.Body.String())
	}
}
func TestLoginCookieAndForgery(t *testing.T) {
	s, _ := testServer(t)
	bad := request(s, "POST", "/api/session", map[string]string{"token": "wrong"}, false, nil)
	if bad.Code != 401 {
		t.Fatalf("invalid token status %d", bad.Code)
	}
	good := request(s, "POST", "/api/session", map[string]string{"token": testToken}, false, map[string]string{"Origin": "https://journal.example"})
	if good.Code != 200 {
		t.Fatalf("login: %d %s", good.Code, good.Body.String())
	}
	cookies := good.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies %#v", cookies)
	}
	cookie := cookies[0]
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("unsafe cookie: %#v", cookie)
	}
	req := httptest.NewRequest("GET", "https://journal.example/api/entries", nil)
	req.AddCookie(cookie)
	out := httptest.NewRecorder()
	s.ServeHTTP(out, req)
	if out.Code != 200 {
		t.Fatal("valid cookie rejected")
	}
	cookie.Value += "0"
	req = httptest.NewRequest("GET", "https://journal.example/api/entries", nil)
	req.AddCookie(cookie)
	out = httptest.NewRecorder()
	s.ServeHTTP(out, req)
	if out.Code != 401 {
		t.Fatal("forged cookie accepted")
	}
	logout := request(s, "DELETE", "/api/session", nil, true, map[string]string{"Origin": "https://journal.example"})
	if logout.Code != 200 || logout.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("logout failed")
	}
}
func TestOriginAndMalformedRequestsRejected(t *testing.T) {
	s, _ := testServer(t)
	for _, headers := range []map[string]string{{"Origin": "https://evil.example"}, {"Origin": "null"}, {"Sec-Fetch-Site": "cross-site"}} {
		r := request(s, "POST", "/api/session", map[string]string{"token": testToken}, false, headers)
		if r.Code != 403 {
			t.Fatalf("origin accepted %#v: %d", headers, r.Code)
		}
	}
	req := httptest.NewRequest("POST", "https://journal.example/api/session", strings.NewReader(`{"token":"`+testToken+`"} {}`))
	req.Header.Set("Content-Type", "application/json")
	out := httptest.NewRecorder()
	s.ServeHTTP(out, req)
	if out.Code != 400 {
		t.Fatal("accepted trailing JSON")
	}
	out = request(s, "POST", "/api/session", map[string]string{"token": testToken, "unknown": "value"}, false, nil)
	if out.Code != 400 {
		t.Fatal("accepted unknown fields")
	}
	req = httptest.NewRequest("POST", "https://journal.example/api/session", strings.NewReader(testToken))
	out = httptest.NewRecorder()
	s.ServeHTTP(out, req)
	if out.Code != 415 {
		t.Fatal("accepted non-JSON request")
	}
}
func TestEditingChecksSourceHashAndPreservesNewerContent(t *testing.T) {
	s, repo := testServer(t)
	path := "/api/entries/" + url.PathEscape(entryName)
	stale := request(s, "POST", path, map[string]string{"body": "덮어쓰기", "base_hash": "old"}, true, nil)
	if stale.Code != 409 {
		t.Fatalf("stale status %d %s", stale.Code, stale.Body.String())
	}
	sum := sha256.Sum256([]byte("개인적인 기억\n"))
	updated := request(s, "POST", path, map[string]string{"body": "수정한 기억\n", "base_hash": hex.EncodeToString(sum[:])}, true, nil)
	if updated.Code != 200 {
		t.Fatalf("edit status %d %s", updated.Code, updated.Body.String())
	}
	stale = request(s, "POST", path, map[string]string{"body": "오래된 탭에서 수정", "base_hash": hex.EncodeToString(sum[:])}, true, nil)
	if stale.Code != 409 {
		t.Fatal("accepted stale editor")
	}
	current, err := repo.Snapshot()
	if err != nil || string(current[entryName]) != "수정한 기억\n" {
		t.Fatalf("content lost: %#v %v", current, err)
	}
}
func TestOnlyEmbeddedPublicAssetsAreServed(t *testing.T) {
	s, _ := testServer(t)
	for path, contentType := range map[string]string{"/": "text/html", "/app.js": "text/javascript", "/style.css": "text/css"} {
		r := request(s, "GET", path, nil, false, nil)
		if r.Code != 200 || !strings.HasPrefix(r.Header().Get("Content-Type"), contentType) {
			t.Fatalf("asset %s: %d %s", path, r.Code, r.Header())
		}
		if r.Header().Get("Content-Security-Policy") == "" || r.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("missing security headers")
		}
		if strings.Contains(r.Body.String(), "개인적인 기억") {
			t.Fatal("private content embedded in public asset")
		}
	}
	for _, path := range []string{"/.jj/config.toml", "/config.toml", "/token", "/" + url.PathEscape(entryName), "/web/embed.go"} {
		r := request(s, "GET", path, nil, false, nil)
		if r.Code != 404 {
			t.Fatalf("unexpected asset exposed %s: %d", path, r.Code)
		}
	}
}
func TestTokenCreationIsPrivateAndPersistent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "token")
	one, err := Token(path)
	if err != nil {
		t.Fatal(err)
	}
	two, err := Token(path)
	if err != nil || one != two || len(one) < 32 {
		t.Fatalf("token persistence %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("token mode %v %v", info, err)
	}
	if err = os.WriteFile(path, []byte("short\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Token(path); err == nil {
		t.Fatal("accepted weak key")
	}
}

package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"buku-journal/internal/journal"
	"buku-journal/internal/worker"
	"buku-journal/web"
)

type Server struct {
	repo     *journal.Repository
	jobs     *worker.Manager
	token    string
	mux      *http.ServeMux
	authMu   sync.Mutex
	attempts map[string]attempt
}
type attempt struct {
	Count int
	Since time.Time
}

func Token(path string) (string, error) {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return "", e
	}
	b, e := os.ReadFile(path)
	if errors.Is(e, os.ErrNotExist) {
		raw := make([]byte, 32)
		if _, e = rand.Read(raw); e != nil {
			return "", e
		}
		token := base64.RawURLEncoding.EncodeToString(raw)
		f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return "", e
		}
		_, e = f.WriteString(token + "\n")
		if e == nil {
			e = f.Sync()
		}
		ce := f.Close()
		if e == nil {
			e = ce
		}
		return token, e
	}
	if e != nil {
		return "", e
	}
	token := strings.TrimSpace(string(b))
	if len(token) < 32 {
		return "", errors.New("접속 키는 32자 이상이어야 합니다")
	}
	return token, nil
}
func New(repo *journal.Repository, jobs *worker.Manager, token string) *Server {
	s := &Server{repo: repo, jobs: jobs, token: token, mux: http.NewServeMux(), attempts: map[string]attempt{}}
	s.routes()
	return s
}
func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { write(w, 200, map[string]string{"status": "ok"}) })
	s.mux.HandleFunc("GET /api/session", func(w http.ResponseWriter, r *http.Request) {
		write(w, 200, map[string]bool{"authenticated": s.authenticated(r)})
	})
	s.mux.HandleFunc("POST /api/session", s.login)
	s.mux.HandleFunc("DELETE /api/session", s.logout)
	s.mux.HandleFunc("GET /api/entries", s.entries)
	s.mux.HandleFunc("POST /api/entries/{id}", s.edit)
	s.mux.HandleFunc("GET /api/places/{id}", s.place)
	s.mux.HandleFunc("GET /api/history", s.history)
	s.mux.HandleFunc("GET /api/history/{id}", s.diff)
	s.mux.HandleFunc("POST /api/history/{id}/revert", s.revert)
	s.mux.HandleFunc("GET /api/jobs", func(w http.ResponseWriter, r *http.Request) { write(w, 200, map[string]any{"jobs": s.jobs.List()}) })
	s.mux.HandleFunc("POST /api/jobs", s.submit)
	s.mux.HandleFunc("POST /api/jobs/{id}/answers", s.answer)
	s.mux.HandleFunc("POST /api/jobs/{id}/cancel", s.cancelJob)
	s.mux.HandleFunc("POST /api/jobs/{id}/retry", s.retryJob)
	s.mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		types := map[string]string{
			"index.html": "text/html; charset=utf-8", "app.js": "text/javascript; charset=utf-8", "style.css": "text/css; charset=utf-8",
			"manifest.webmanifest": "application/manifest+json", "icon.svg": "image/svg+xml",
			"pwa-192.png": "image/png", "pwa-512.png": "image/png", "apple-touch-icon.png": "image/png",
		}
		contentType, allowed := types[name]
		if !allowed {
			http.NotFound(w, r)
			return
		}
		b, e := fs.ReadFile(web.FS, name)
		if e != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(b)
	})
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path != "/api/session" && !s.authenticated(r) {
			problem(w, 401, "접속 키로 로그인해 주세요.")
			return
		}
	}
	if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" {
		if origin := r.Header.Get("Origin"); origin != "" {
			u, e := url.Parse(origin)
			if e != nil || u.Host != r.Host || (u.Scheme != "https" && u.Scheme != "http") {
				problem(w, 403, "허용되지 않은 요청 출처입니다.")
				return
			}
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" {
			problem(w, 403, "허용되지 않은 요청 출처입니다.")
			return
		}
	}
	s.mux.ServeHTTP(w, r)
}
func (s *Server) authenticated(r *http.Request) bool {
	if t := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "); t != "" && subtle.ConstantTimeCompare([]byte(t), []byte(s.token)) == 1 {
		return true
	}
	c, e := r.Cookie("buku_session")
	if e != nil {
		return false
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 2 {
		return false
	}
	expiry, e := strconv.ParseInt(parts[0], 10, 64)
	if e != nil || time.Now().Unix() > expiry || expiry > time.Now().Add(31*24*time.Hour).Unix() {
		return false
	}
	signature, e := hex.DecodeString(parts[1])
	if e != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(s.token))
	_, _ = mac.Write([]byte(parts[0]))
	return hmac.Equal(signature, mac.Sum(nil))
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	s.authMu.Lock()
	a := s.attempts[host]
	if time.Since(a.Since) > time.Minute {
		a = attempt{Since: time.Now()}
	}
	if a.Count >= 10 {
		s.authMu.Unlock()
		w.Header().Set("Retry-After", "60")
		problem(w, 429, "접속 시도가 많아요. 1분 후 다시 시도해 주세요.")
		return
	}
	a.Count++
	if len(s.attempts) > 1024 {
		clear(s.attempts)
	}
	s.attempts[host] = a
	s.authMu.Unlock()
	var body struct {
		Token string `json:"token"`
	}
	if !decode(w, r, &body) {
		return
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(body.Token)), []byte(s.token)) != 1 {
		problem(w, 401, "접속 키가 맞지 않아요.")
		return
	}
	s.authMu.Lock()
	delete(s.attempts, host)
	s.authMu.Unlock()
	exp := time.Now().Add(30 * 24 * time.Hour)
	payload := strconv.FormatInt(exp.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(s.token))
	_, _ = mac.Write([]byte(payload))
	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	http.SetCookie(w, &http.Cookie{Name: "buku_session", Value: payload + "." + hex.EncodeToString(mac.Sum(nil)), Path: "/", Expires: exp, MaxAge: 30 * 24 * 60 * 60, HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode})
	write(w, 200, map[string]bool{"authenticated": true})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "buku_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https", SameSite: http.SameSiteStrictMode})
	write(w, 200, map[string]bool{"authenticated": false})
}
func (s *Server) entries(w http.ResponseWriter, r *http.Request) {
	entries, rev, e := s.repo.List()
	if e != nil {
		problem(w, 500, e.Error())
		return
	}
	if entries == nil {
		entries = []journal.Entry{}
	}
	write(w, 200, map[string]any{"entries": entries, "revision": rev})
}
func (s *Server) place(w http.ResponseWriter, r *http.Request) {
	p, e := s.repo.Place(r.PathValue("id"))
	if e != nil {
		problem(w, 404, "장소 정보를 찾을 수 없어요.")
		return
	}
	write(w, 200, p)
}
func (s *Server) edit(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Body     string `json:"body"`
		BaseHash string `json:"base_hash"`
	}
	if !decode(w, r, &b) {
		return
	}
	id := r.PathValue("id")
	if !strings.HasSuffix(id, ".md") || !journal.ValidFilename(id) {
		problem(w, 400, "잘못된 기록 경로입니다.")
		return
	}
	base, e := s.repo.Snapshot()
	if e != nil {
		problem(w, 500, e.Error())
		return
	}
	old, ok := base[id]
	if !ok {
		problem(w, 404, "기록을 찾을 수 없어요.")
		return
	}
	hash := sha256.Sum256(old)
	if b.BaseHash != hex.EncodeToString(hash[:]) {
		problem(w, 409, "다른 곳에서 수정된 기록이에요. 새로고침 후 다시 편집해 주세요.")
		return
	}
	next := make(map[string][]byte, len(base))
	for n, v := range base {
		next[n] = v
	}
	next[id] = []byte(b.Body)
	change, e := s.repo.Apply(base, next, "원문 수정: "+id)
	if e != nil {
		problem(w, 409, e.Error())
		return
	}
	write(w, 200, map[string]string{"change_id": change})
}
func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	changes, e := s.repo.History()
	if e != nil {
		problem(w, 500, e.Error())
		return
	}
	if changes == nil {
		changes = []journal.Change{}
	}
	write(w, 200, map[string]any{"changes": changes})
}
func (s *Server) diff(w http.ResponseWriter, r *http.Request) {
	diff, e := s.repo.Diff(r.PathValue("id"))
	if e != nil {
		problem(w, 400, e.Error())
		return
	}
	write(w, 200, map[string]string{"diff": diff})
}
func (s *Server) revert(w http.ResponseWriter, r *http.Request) {
	id, e := s.repo.Revert(r.PathValue("id"))
	if e != nil {
		problem(w, 409, e.Error())
		return
	}
	write(w, 200, map[string]string{"change_id": id})
}
func (s *Server) submit(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Message   string `json:"message"`
		EntryID   string `json:"entry_id"`
		RequestID string `json:"request_id"`
	}
	if !decode(w, r, &b) {
		return
	}
	j, e := s.jobs.Submit(b.Message, b.EntryID, b.RequestID)
	if e != nil {
		problem(w, 400, e.Error())
		return
	}
	write(w, 202, map[string]any{"job": j})
}
func (s *Server) answer(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Message string `json:"message"`
	}
	if !decode(w, r, &b) {
		return
	}
	j, e := s.jobs.Answer(r.PathValue("id"), b.Message)
	if e != nil {
		problem(w, 400, e.Error())
		return
	}
	write(w, 202, map[string]any{"job": j})
}
func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	j, e := s.jobs.Cancel(r.PathValue("id"))
	if e != nil {
		problem(w, 404, e.Error())
		return
	}
	write(w, 200, map[string]any{"job": j})
}
func (s *Server) retryJob(w http.ResponseWriter, r *http.Request) {
	j, e := s.jobs.Retry(r.PathValue("id"))
	if e != nil {
		problem(w, 409, e.Error())
		return
	}
	write(w, 202, map[string]any{"job": j})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		problem(w, 415, "JSON 요청이 필요합니다.")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		problem(w, 400, "요청 형식이 올바르지 않아요.")
		return false
	}
	if e := d.Decode(&struct{}{}); e != io.EOF {
		problem(w, 400, "요청 형식이 올바르지 않아요.")
		return false
	}
	return true
}
func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func problem(w http.ResponseWriter, status int, message string) {
	write(w, status, map[string]string{"error": message})
}

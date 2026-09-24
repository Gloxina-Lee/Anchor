package anchor

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const cookieName = "anchor_session"
const sessionLifetime = 7 * 24 * time.Hour

type Server struct {
	store           *Store
	webDir          string
	insecureCookies bool
}

func NewServer(store *Store, webDir string, insecureCookies bool) http.Handler {
	s := &Server{store: store, webDir: webDir, insecureCookies: insecureCookies}
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/status", s.status)
	mux.HandleFunc("/auth/setup", s.setup)
	mux.HandleFunc("/auth/login", s.login)
	mux.HandleFunc("/auth/logout", s.logout)
	mux.HandleFunc("/api/links/batch", s.batchLinks)
	mux.HandleFunc("/api/links/", s.oneLink)
	mux.HandleFunc("/api/links", s.links)
	mux.HandleFunc("/api/settings", s.settings)
	mux.HandleFunc("/admin/", s.admin)
	mux.HandleFunc("/admin", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusPermanentRedirect)
	})
	mux.HandleFunc("/", s.public)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) {
		return
	}
	account, initialized, err := s.store.Account()
	if err != nil {
		serverError(w, err)
		return
	}
	_, sess, authenticated := s.currentSession(r)
	response := map[string]any{"initialized": initialized, "authenticated": authenticated}
	if authenticated {
		response["username"] = account.Username
		response["csrf"] = sess.CSRF
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) || !sameOrigin(w, r) {
		return
	}
	_, initialized, err := s.store.Account()
	if err != nil {
		serverError(w, err)
		return
	}
	if initialized {
		apiError(w, http.StatusConflict, "服务已完成初始化")
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	input.Username = strings.TrimSpace(input.Username)
	if len(input.Username) < 3 || len(input.Username) > 64 || len(input.Password) < 8 || len(input.Password) > 1024 {
		apiError(w, http.StatusBadRequest, "用户名需为 3–64 个字符，密码需为 8–1024 个字符")
		return
	}
	hash, err := hashPassword(input.Password)
	if err != nil {
		serverError(w, err)
		return
	}
	if err := s.store.CreateAccount(input.Username, hash); err != nil {
		respondStoreError(w, err)
		return
	}
	s.issueSession(w)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) || !sameOrigin(w, r) {
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	account, found, err := s.store.Account()
	if err != nil {
		serverError(w, err)
		return
	}
	if !found || input.Username != account.Username || !verifyPassword(account.Hash, input.Password) {
		apiError(w, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	s.issueSession(w)
}

func (s *Server) issueSession(w http.ResponseWriter) {
	token, sess, err := newSession()
	if err != nil {
		serverError(w, err)
		return
	}
	sess.ExpiresAt = time.Now().Add(sessionLifetime).UTC()
	if err := s.store.PutSession(token, sess); err != nil {
		serverError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/", Expires: sess.ExpiresAt,
		HttpOnly: true, Secure: !s.insecureCookies, SameSite: http.SameSiteLaxMode})
	writeJSON(w, http.StatusOK, map[string]any{"csrf": sess.CSRF})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) || !sameOrigin(w, r) {
		return
	}
	token, _, ok := s.authorize(w, r, true)
	if !ok {
		return
	}
	if err := s.store.DeleteSession(token); err != nil {
		serverError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: !s.insecureCookies, SameSite: http.SameSiteLaxMode})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) currentSession(r *http.Request) (string, session, bool) {
	cookie, err := r.Cookie(cookieName)
	if err != nil || len(cookie.Value) != 43 {
		return "", session{}, false
	}
	sess, found, err := s.store.Session(cookie.Value)
	if err != nil || !found {
		return "", session{}, false
	}
	return cookie.Value, sess, true
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request, write bool) (string, session, bool) {
	token, sess, found := s.currentSession(r)
	if !found {
		apiError(w, http.StatusUnauthorized, "请先登录")
		return "", session{}, false
	}
	if write && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(sess.CSRF)) != 1 {
		apiError(w, http.StatusForbidden, "请求验证失败，请刷新页面重试")
		return "", session{}, false
	}
	return token, sess, true
}

func (s *Server) links(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w, "GET, POST")
		return
	}
	if r.Method == http.MethodPost && !sameOrigin(w, r) {
		return
	}
	if _, _, ok := s.authorize(w, r, r.Method != http.MethodGet); !ok {
		return
	}
	if r.Method == http.MethodGet {
		links, err := s.store.ListLinks()
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, links)
		return
	}
	var input struct {
		Code        string     `json:"code"`
		Destination string     `json:"destination"`
		Note        string     `json:"note"`
		StartsAt    *time.Time `json:"startsAt"`
		ExpiresAt   *time.Time `json:"expiresAt"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	settings, err := s.store.Settings()
	if err != nil {
		serverError(w, err)
		return
	}
	if !validDestination(input.Destination) || input.Code != "" && !validCode(input.Code, settings.MaxLength) {
		apiError(w, http.StatusBadRequest, "目标网址或短码无效")
		return
	}
	note, valid := normalizeNote(input.Note)
	if !valid {
		apiError(w, http.StatusBadRequest, "备注不能超过 500 个字符，也不能包含控制字符")
		return
	}
	start := time.Now().UTC()
	if input.StartsAt != nil {
		start = input.StartsAt.UTC()
	}
	if input.ExpiresAt != nil && !input.ExpiresAt.After(start) {
		apiError(w, http.StatusBadRequest, "到期时间必须晚于生效时间")
		return
	}
	link := Link{Destination: input.Destination, Note: note, CreatedAt: time.Now().UTC(), StartsAt: start, ExpiresAt: input.ExpiresAt}
	link, err = s.store.CreateLink(link, input.Code)
	if err != nil {
		respondStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, link)
}

func (s *Server) oneLink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch && r.Method != http.MethodDelete {
		methodNotAllowed(w, "PATCH, DELETE")
		return
	}
	if !sameOrigin(w, r) {
		return
	}
	if _, _, ok := s.authorize(w, r, true); !ok {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/links/")
	if len(id) != 32 || strings.Contains(id, "/") {
		apiError(w, http.StatusNotFound, "短链接不存在")
		return
	}
	if r.Method == http.MethodDelete {
		if err := s.store.DeleteLinks([]string{id}); err != nil {
			respondStoreError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var input struct {
		ExpiresAt json.RawMessage `json:"expiresAt"`
		Note      *string         `json:"note"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	if (len(input.ExpiresAt) > 0) == (input.Note != nil) {
		apiError(w, http.StatusBadRequest, "请只提交备注或到期时间")
		return
	}
	var err error
	if input.Note != nil {
		var note string
		var valid bool
		note, valid = normalizeNote(*input.Note)
		if !valid {
			apiError(w, http.StatusBadRequest, "备注不能超过 500 个字符，也不能包含控制字符")
			return
		}
		err = s.store.UpdateNote(id, note)
	} else {
		var expiry *time.Time
		if err := json.Unmarshal(input.ExpiresAt, &expiry); err != nil {
			apiError(w, http.StatusBadRequest, "到期时间格式错误")
			return
		}
		err = s.store.UpdateExpiry([]string{id}, expiry)
	}
	if err != nil {
		respondStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func normalizeNote(raw string) (string, bool) {
	note := strings.TrimSpace(raw)
	if utf8.RuneCountInString(note) > 500 {
		return "", false
	}
	for _, char := range note {
		if char < 0x20 && char != '\n' && char != '\t' || char == 0x7f {
			return "", false
		}
	}
	return note, true
}

func (s *Server) batchLinks(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) || !sameOrigin(w, r) {
		return
	}
	if _, _, ok := s.authorize(w, r, true); !ok {
		return
	}
	var input struct {
		IDs       []string   `json:"ids"`
		Action    string     `json:"action"`
		ExpiresAt *time.Time `json:"expiresAt"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	if len(input.IDs) == 0 || len(input.IDs) > 500 || (input.Action != "delete" && input.Action != "expiry") {
		apiError(w, http.StatusBadRequest, "批量操作无效")
		return
	}
	seen := make(map[string]bool, len(input.IDs))
	ids := make([]string, 0, len(input.IDs))
	for _, id := range input.IDs {
		if len(id) != 32 {
			apiError(w, http.StatusBadRequest, "短链接 ID 无效")
			return
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	var err error
	if input.Action == "delete" {
		err = s.store.DeleteLinks(ids)
	} else {
		err = s.store.UpdateExpiry(ids, input.ExpiresAt)
	}
	if err != nil {
		respondStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPut {
		methodNotAllowed(w, "GET, PUT")
		return
	}
	if r.Method == http.MethodPut && !sameOrigin(w, r) {
		return
	}
	if _, _, ok := s.authorize(w, r, r.Method != http.MethodGet); !ok {
		return
	}
	if r.Method == http.MethodGet {
		settings, err := s.store.Settings()
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, settings)
		return
	}
	var settings Settings
	if !readJSON(w, r, &settings) {
		return
	}
	if settings.MinLength < 1 || settings.MaxLength > 128 || settings.MinLength > settings.MaxLength {
		apiError(w, http.StatusBadRequest, "随机短码长度须在 1–128 之间，最小值不能大于最大值")
		return
	}
	if err := s.store.UpdateSettings(settings); err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (s *Server) public(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, "GET, HEAD")
		return
	}
	if r.URL.Path == "/" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, "<!doctype html><html lang=\"zh-CN\"><meta charset=\"utf-8\"><title></title><body></body></html>")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if strings.Count(r.URL.Path, "/") != 1 {
		http.NotFound(w, r)
		return
	}
	code := strings.TrimPrefix(r.URL.Path, "/")
	if !validCode(code, 128) {
		http.NotFound(w, r)
		return
	}
	link, err := s.store.ActiveLink(code)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			http.NotFound(w, r)
		} else {
			serverError(w, err)
		}
		return
	}
	http.Redirect(w, r, link.Destination, http.StatusFound)
}

func (s *Server) admin(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) {
		return
	}
	relative := strings.TrimPrefix(r.URL.Path, "/admin/")
	if strings.Contains(relative, "..") || strings.Contains(relative, "\\") {
		http.NotFound(w, r)
		return
	}
	if relative != "" {
		file := filepath.Join(s.webDir, filepath.FromSlash(relative))
		if info, err := os.Stat(file); err == nil && info.Mode().IsRegular() {
			http.ServeFile(w, r, file)
			return
		}
		if strings.Contains(filepath.Base(relative), ".") {
			http.NotFound(w, r)
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, filepath.Join(s.webDir, "index.html"))
}

func method(w http.ResponseWriter, r *http.Request, allowed string) bool {
	if r.Method == allowed {
		return true
	}
	methodNotAllowed(w, allowed)
	return false
}

func methodNotAllowed(w http.ResponseWriter, allowed string) {
	w.Header().Set("Allow", allowed)
	apiError(w, http.StatusMethodNotAllowed, "不支持此请求方法")
}

func sameOrigin(w http.ResponseWriter, r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" {
		apiError(w, http.StatusForbidden, "跨站请求已拒绝")
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || !strings.EqualFold(parsed.Host, r.Host) || (parsed.Scheme != "https" && parsed.Scheme != "http") {
			apiError(w, http.StatusForbidden, "跨站请求已拒绝")
			return false
		}
	}
	return true
}

func readJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		apiError(w, http.StatusUnsupportedMediaType, "请使用 JSON 请求")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		apiError(w, http.StatusBadRequest, "请求数据格式错误")
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		apiError(w, http.StatusBadRequest, "请求数据格式错误")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func apiError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func serverError(w http.ResponseWriter, err error) {
	fmt.Fprintf(os.Stderr, "anchor: %v\n", err)
	apiError(w, http.StatusInternalServerError, "服务器暂时无法处理请求")
}

func respondStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrConflict):
		apiError(w, http.StatusConflict, "短码已被使用，或时间设置与现有数据冲突")
	case errors.Is(err, ErrNotFound):
		apiError(w, http.StatusNotFound, "短链接不存在")
	default:
		serverError(w, err)
	}
}

// Package server exposes the app service over HTTP for the iOS shortcut.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ledger-snap/internal/app"
	"ledger-snap/internal/store"
)

type Server struct {
	App *app.Service
	Log *slog.Logger
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("POST /v1/login", s.login)
	mux.HandleFunc("POST /v1/recognize", s.auth(s.recognize))
	mux.HandleFunc("POST /v1/records/{id}/confirm", s.auth(s.confirm))
	mux.HandleFunc("POST /v1/records/{id}/correct", s.auth(s.correct))
	mux.HandleFunc("POST /v1/records/{id}/update", s.auth(s.update))
	mux.HandleFunc("GET /v1/options", s.auth(s.options))
	mux.HandleFunc("POST /v1/catalog/refresh", s.auth(s.refreshCatalog))
	mux.HandleFunc("DELETE /v1/records/{id}", s.auth(s.remove))
	// The shortcut's "Get Contents of URL" handles POST more reliably than DELETE.
	mux.HandleFunc("POST /v1/records/{id}/delete", s.auth(s.remove))
	return s.logRequests(mux)
}

type authed func(w http.ResponseWriter, r *http.Request, u *store.User)

func (s *Server) auth(next authed) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		u, err := s.App.Authenticate(r.Context(), strings.TrimSpace(token))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		next(w, r, u)
	}
}

// field reads a value from a JSON body or a form, whichever the client sent.
func field(r *http.Request, body map[string]string, name string) string {
	if v, ok := body[name]; ok {
		return v
	}
	return r.FormValue(name)
}

func readBody(r *http.Request) map[string]string {
	body := map[string]string{}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		data, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		_ = json.Unmarshal(data, &body)
	}
	return body
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	body := readBody(r)
	token, err := s.App.Login(r.Context(), field(r, body, "account"), field(r, body, "password_md5"), field(r, body, "invite_code"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]string{"token": token, "message": "登录成功"})
}

func (s *Server) recognize(w http.ResponseWriter, r *http.Request, u *store.User) {
	limit := s.App.Config.Server.MaxUploadMB << 20
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	if err := r.ParseMultipartForm(limit); err != nil {
		s.fail(w, r, &app.UserError{Status: 400, Message: "需要以 multipart 表单上传 image 字段"})
		return
	}
	file, _, err := r.FormFile("image")
	if err != nil {
		s.fail(w, r, &app.UserError{Status: 400, Message: "缺少 image 字段"})
		return
	}
	defer file.Close()
	image, err := io.ReadAll(file)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	upload := time.Now()
	if v := r.FormValue("client_time"); v != "" {
		if sec, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && sec > 0 {
			upload = time.Unix(sec, 0)
		}
	}
	res, err := s.App.Recognize(r.Context(), u, image, upload)
	s.reply(w, r, res, err)
}

func (s *Server) confirm(w http.ResponseWriter, r *http.Request, u *store.User) {
	res, err := s.App.Confirm(r.Context(), u, r.PathValue("id"))
	s.reply(w, r, res, err)
}

func (s *Server) correct(w http.ResponseWriter, r *http.Request, u *store.User) {
	body := readBody(r)
	res, err := s.App.Correct(r.Context(), u, r.PathValue("id"), field(r, body, "text"))
	s.reply(w, r, res, err)
}

func (s *Server) update(w http.ResponseWriter, r *http.Request, u *store.User) {
	body := readBody(r)
	p := app.Patch{}
	for name, dst := range map[string]*string{"kind": &p.Kind, "category": &p.Category, "asset": &p.Asset,
		"target_asset": &p.TargetAsset, "money": &p.Money, "time": &p.Time, "remark": &p.Remark} {
		*dst = strings.TrimSpace(field(r, body, name))
	}
	res, err := s.App.Update(r.Context(), u, r.PathValue("id"), p)
	s.reply(w, r, res, err)
}

func (s *Server) options(w http.ResponseWriter, r *http.Request, u *store.User) {
	o, err := s.App.Options(r.Context(), u)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, o)
}

func (s *Server) refreshCatalog(w http.ResponseWriter, r *http.Request, u *store.User) {
	info, err := s.App.RefreshCatalog(r.Context(), u)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, info)
}

func (s *Server) remove(w http.ResponseWriter, r *http.Request, u *store.User) {
	res, err := s.App.Delete(r.Context(), u, r.PathValue("id"))
	s.reply(w, r, res, err)
}

func (s *Server) reply(w http.ResponseWriter, r *http.Request, res *app.Result, err error) {
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, res)
}

// fail always answers with a "message" the shortcut can show directly.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var ue *app.UserError
	if errors.As(err, &ue) {
		body := map[string]string{"status": "error", "message": ue.Message}
		if ue.Status == http.StatusUnauthorized {
			// The shortcut deletes its saved token when it sees this code.
			body["code"] = "auth_required"
		}
		writeJSON(w, ue.Status, body)
		return
	}
	if errors.Is(err, context.Canceled) {
		return
	}
	s.Log.Error("请求失败", "path", r.URL.Path, "err", err)
	writeJSON(w, 500, map[string]string{"status": "error", "message": "服务出错：" + err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (sw *statusWriter) WriteHeader(code int) {
	sw.status = code
	sw.ResponseWriter.WriteHeader(code)
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		s.Log.Info("请求", "method", r.Method, "path", r.URL.Path, "status", sw.status, "ms", time.Since(start).Milliseconds())
	})
}

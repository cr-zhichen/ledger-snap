// Package qianji is a minimal client for the unofficial Qianji app API: login,
// catalogs, and bill reads and writes through the sync channel.
package qianji

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Options struct {
	BaseURL     string
	Timeout     time.Duration
	VersionCode int
	VersionName string
	Market      string
	OSVersion   string
	DeviceBrand string
	DeviceName  string
}

type Session struct {
	UID    string
	Token  string
	Device string
}

type Client struct {
	opt  Options
	http *http.Client

	mu     sync.Mutex
	lastMS map[string]int64
}

func New(opt Options) *Client {
	return &Client{
		opt: opt,
		http: &http.Client{
			Timeout: opt.Timeout,
			// Never forward credentials to another host.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		lastMS: map[string]int64{},
	}
}

type ErrorKind int

const (
	ErrNetwork ErrorKind = iota + 1
	ErrAuth
	ErrLoginRejected
	ErrSignature
	ErrBusiness
	ErrInvalidResponse
)

type Error struct {
	Kind    ErrorKind
	Path    string
	Code    int
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("钱迹 %s: %s (code=%d)", e.Path, e.Message, e.Code)
}

func IsKind(err error, kind ErrorKind) bool {
	var e *Error
	return errors.As(err, &e) && e.Kind == kind
}

// Routes on the sync channel carry htoken: 1.
var syncRoutes = map[string]bool{"/bill/syncall": true, "/syncv2/pull": true}

func PasswordMD5(password string) string {
	sum := md5.Sum([]byte(password))
	return hex.EncodeToString(sum[:])
}

func md5hex(s string) string { return PasswordMD5(s) }

// nextMS keeps the signature timestamp strictly increasing per route.
func (c *Client) nextMS(path string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := max(time.Now().UnixMilli(), c.lastMS[path]+1)
	c.lastMS[path] = now
	return now
}

func (c *Client) sign(ctrl, act string, ms int64) (reqid, tok string) {
	shifted := strconv.FormatInt(ms+int64(c.opt.VersionCode)+9081127, 10)
	reqid = md5hex("com.mutangtech.qianji" + shifted + ctrl + act + "free20170908&x_*1127")
	inner := md5hex(reqid + "michaeljackson")
	return reqid, md5hex(reqid + "1172020" + ctrl + inner + act)
}

func (c *Client) post(ctx context.Context, path string, s Session, form url.Values) (json.RawMessage, error) {
	if path != "/account/login" {
		if s.UID == "" || s.Token == "" {
			return nil, &Error{Kind: ErrAuth, Path: path, Message: "未登录"}
		}
		form.Set("uid", s.UID)
		form.Set("fr", s.UID)
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	ctrl, act := parts[0], parts[1]
	reqid, tok := c.sign(ctrl, act, c.nextMS(path))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.opt.BaseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	headers := map[string]string{
		"content-type": "application/x-www-form-urlencoded",
		"ctrl":         ctrl, "act": act,
		"os": "1", "osvs": c.opt.OSVersion, "pkg": "com.mutangtech.qianji",
		"vs": strconv.Itoa(c.opt.VersionCode), "vsn": c.opt.VersionName, "mk": c.opt.Market,
		"devbrand": c.opt.DeviceBrand, "devname": c.opt.DeviceName, "devid": s.Device,
		"clang": "zh", "cregion": "CN", "timezoneoffset": "28800",
		"reqidv2": reqid, "tok": tok,
	}
	if s.Token != "" {
		headers["utoken"] = s.Token
	}
	if syncRoutes[path] {
		headers["htoken"] = "1"
	}
	for k, v := range headers {
		// The server only speaks HTTP/1.1 and rejects canonicalized names
		// such as "Vs", so bypass Header.Set.
		req.Header[k] = []string{v}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &Error{Kind: ErrNetwork, Path: path, Message: err.Error()}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, &Error{Kind: ErrNetwork, Path: path, Message: err.Error()}
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, authError(path, resp.StatusCode, "登录状态失效")
	}
	// A bare 403 can be a signature or gateway rejection, not token expiry.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &Error{Kind: ErrNetwork, Path: path, Code: resp.StatusCode, Message: "HTTP " + strconv.Itoa(resp.StatusCode)}
	}
	var env struct {
		Code    *int            `json:"ec"`
		Message json.RawMessage `json:"em"`
		Data    json.RawMessage `json:"data"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if dec.Decode(&env) != nil || env.Code == nil {
		return nil, &Error{Kind: ErrInvalidResponse, Path: path, Message: "响应不是有效 JSON"}
	}
	if *env.Code != 200 {
		return nil, businessError(path, *env.Code, env.Message)
	}
	if len(env.Data) == 0 || string(bytes.TrimSpace(env.Data)) == "null" {
		return nil, &Error{Kind: ErrInvalidResponse, Path: path, Message: "响应缺少 data"}
	}
	return env.Data, nil
}

func authError(path string, code int, msg string) error {
	if path == "/account/login" {
		return &Error{Kind: ErrLoginRejected, Path: path, Code: code, Message: msg}
	}
	return &Error{Kind: ErrAuth, Path: path, Code: code, Message: msg}
}

func businessError(path string, code int, raw json.RawMessage) error {
	var msg string
	_ = json.Unmarshal(raw, &msg)
	var nested struct {
		Msg string `json:"msg"`
	}
	if json.Unmarshal([]byte(msg), &nested) == nil && nested.Msg != "" {
		msg = nested.Msg
	}
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "签名") || strings.Contains(lower, "signature") || strings.Contains(lower, "sign error") || strings.HasPrefix(lower, "tok") {
		return &Error{Kind: ErrSignature, Path: path, Code: code, Message: msg}
	}
	if path == "/account/login" {
		return authError(path, code, msg)
	}
	for _, marker := range []string{"token", "登录", "login"} {
		if strings.Contains(lower, marker) {
			return authError(path, code, msg)
		}
	}
	return &Error{Kind: ErrBusiness, Path: path, Code: code, Message: msg}
}

func (c *Client) Login(ctx context.Context, account, passwordMD5, device string) (Session, error) {
	data, err := c.post(ctx, "/account/login", Session{Device: device}, url.Values{"v": {account}, "pwd": {strings.ToLower(passwordMD5)}})
	if err != nil {
		return Session{}, err
	}
	var result struct {
		User struct {
			ID json.RawMessage `json:"id"`
		} `json:"user"`
		Token string `json:"token"`
	}
	if json.Unmarshal(data, &result) != nil || result.Token == "" {
		return Session{}, &Error{Kind: ErrInvalidResponse, Path: "/account/login", Message: "登录响应缺少 token"}
	}
	uid := strings.Trim(strings.TrimSpace(string(result.User.ID)), `"`)
	if uid == "" {
		return Session{}, &Error{Kind: ErrInvalidResponse, Path: "/account/login", Message: "登录响应缺少用户 ID"}
	}
	return Session{UID: uid, Token: result.Token, Device: device}, nil
}

func (c *Client) list(ctx context.Context, path string, s Session, form url.Values) ([]json.RawMessage, error) {
	data, err := c.post(ctx, path, s, form)
	if err != nil {
		return nil, err
	}
	var object struct {
		List []json.RawMessage `json:"list"`
	}
	if json.Unmarshal(data, &object) != nil || object.List == nil {
		return nil, &Error{Kind: ErrInvalidResponse, Path: path, Message: "响应缺少 list"}
	}
	return object.List, nil
}

// Categories returns the category tree of a book; kind -1 means both types.
func (c *Client) Categories(ctx context.Context, s Session, book string, kind int) ([]json.RawMessage, error) {
	return c.list(ctx, "/category/listv2", s, url.Values{"bookid": {book}, "t": {strconv.Itoa(kind)}})
}

// Assets returns visible (status 0) or hidden (status 2) asset accounts.
func (c *Client) Assets(ctx context.Context, s Session, status int) ([]json.RawMessage, error) {
	return c.list(ctx, "/asset/list", s, url.Values{"status": {strconv.Itoa(status)}})
}

func (c *Client) Books(ctx context.Context, s Session) ([]json.RawMessage, error) {
	return c.list(ctx, "/book/list", s, url.Values{"t": {"1"}})
}

// Currencies lists the currencies with their prices in CNY.
func (c *Client) Currencies(ctx context.Context, s Session) ([]json.RawMessage, error) {
	return c.list(ctx, "/currency/listv2", s, url.Values{})
}

// BaseCurrency reads the user's base currency from the settings client/init
// returns; users who never changed it get DefaultCurrency.
func (c *Client) BaseCurrency(ctx context.Context, s Session) (string, error) {
	data, err := c.post(ctx, "/client/init", s, url.Values{"v": {"0"}, "vvmark": {"0"}, "newinstall": {"0"}, "upgradealert": {"0"}})
	if err != nil {
		return "", err
	}
	var result struct {
		Configs struct {
			Base string `json:"basecur"`
		} `json:"userconfigs"`
	}
	if json.Unmarshal(data, &result) != nil || result.Configs.Base == "" {
		return DefaultCurrency, nil
	}
	return result.Configs.Base, nil
}

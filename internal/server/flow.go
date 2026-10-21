package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"ledger-snap/internal/app"
	"ledger-snap/internal/recognize"
	"ledger-snap/internal/store"
)

// The generated shortcut (see internal/shortcut) is one generic loop: it shows
// the Step the server returns and posts the answer to Step.Next. Every menu,
// prompt and branch lives here, so changing the flow never means reinstalling
// the shortcut. These handlers always answer 200, because the shortcut cannot
// branch on HTTP status codes.

// Step is one dialog of the shortcut.
type Step struct {
	// Action is choose (pick one of Options), text or number (type an answer),
	// done (show Message as a notification and stop) or relogin (forget the
	// saved token, show Message and stop).
	Action  string   `json:"action"`
	Prompt  string   `json:"prompt,omitempty"`
	Options []string `json:"options,omitempty"`
	Default string   `json:"default,omitempty"`
	// Next is the path to POST {"value": answer} to.
	Next     string `json:"next,omitempty"`
	Message  string `json:"message,omitempty"`
	RecordID string `json:"record_id,omitempty"`
}

// Menu entries; the server matches answers against these.
const (
	optConfirm  = "确认"
	optEdit     = "修改"
	optDelete   = "删除"
	optAsset    = "账户"
	optKind     = "收支类型"
	optCategory = "分类"
	optMoney    = "金额"
	optTime     = "时间"
	optText     = "用一句话改"
	optRefresh  = "刷新账户和分类"
	optBack     = "返回"
)

// keepKind in a step path means the edit keeps the bill's kind.
const keepKind = "keep"

func (s *Server) flowLogin(w http.ResponseWriter, r *http.Request) {
	body := readBody(r)
	token, err := s.App.Login(r.Context(), field(r, body, "account"), field(r, body, "password_md5"), field(r, body, "invite_code"))
	if err != nil {
		writeJSON(w, 200, map[string]string{"message": s.message(r, err)})
		return
	}
	writeJSON(w, 200, map[string]string{"token": token, "message": "登录成功"})
}

func (s *Server) flowAuth(next authed) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, err := s.App.Authenticate(r.Context(), bearer(r))
		if err != nil {
			s.failStep(w, r, err)
			return
		}
		next(w, r, u)
	}
}

func (s *Server) flowRecognize(w http.ResponseWriter, r *http.Request, u *store.User) {
	image, upload, err := s.readImage(w, r)
	if err != nil {
		s.failStep(w, r, err)
		return
	}
	res, err := s.App.Recognize(r.Context(), u, image, upload)
	if err != nil {
		s.failStep(w, r, err)
		return
	}
	writeJSON(w, 200, mainStep(res, ""))
}

func (s *Server) flowStep(w http.ResponseWriter, r *http.Request, u *store.User) {
	ctx, id := r.Context(), r.PathValue("id")
	step, err := s.advance(ctx, u, id, strings.SplitN(r.PathValue("step"), "/", 3), flowValue(r))
	if err == nil {
		writeJSON(w, 200, step)
		return
	}
	var ue *app.UserError
	if errors.As(err, &ue) && (ue.Status == http.StatusUnauthorized || ue.Status == http.StatusNotFound) {
		s.failStep(w, r, err)
		return
	}
	// A failed step leaves the record as it was: show it again with the
	// reason, so the user can try something else.
	cur, cerr := s.App.Current(ctx, u, id)
	if cerr != nil {
		s.failStep(w, r, err)
		return
	}
	writeJSON(w, 200, mainStep(cur, "没改成："+s.message(r, err)))
}

// failStep ends the run with an error message; an auth failure also makes the
// shortcut forget its token so the next run logs in again.
func (s *Server) failStep(w http.ResponseWriter, r *http.Request, err error) {
	var ue *app.UserError
	if errors.As(err, &ue) && ue.Status == http.StatusUnauthorized {
		writeJSON(w, 200, Step{Action: "relogin", Message: ue.Message + "（再运行一次快捷指令即可登录）"})
		return
	}
	writeJSON(w, 200, done(s.message(r, err)))
}

// message is the text shown to the user for an error; unexpected errors are
// logged.
func (s *Server) message(r *http.Request, err error) string {
	var ue *app.UserError
	if errors.As(err, &ue) {
		return ue.Message
	}
	if !errors.Is(err, context.Canceled) {
		s.Log.Error("请求失败", "path", r.URL.Path, "err", err)
	}
	return "服务出错：" + err.Error()
}

// flowValue reads the answer posted as {"value": ...}; number inputs may
// arrive as JSON numbers.
func flowValue(r *http.Request) string {
	var body struct {
		Value any `json:"value"`
	}
	data, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if json.Unmarshal(data, &body) != nil {
		return ""
	}
	switch v := body.Value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return fmt.Sprint(body.Value)
}

// advance handles the answer to one step and returns the next one. path is
// the step name followed by the state earlier steps carried along.
func (s *Server) advance(ctx context.Context, u *store.User, id string, path []string, value string) (*Step, error) {
	arg := func(i int) string {
		if i < len(path) {
			return path[i]
		}
		return ""
	}
	switch path[0] {
	case "main":
		return s.mainChoice(ctx, u, id, value)
	case "edit":
		return s.editChoice(ctx, u, id, value)
	case "kind":
		kind := kindOf(value)
		if kind == "" {
			return nil, &app.UserError{Status: 400, Message: "不认识的类型：" + value}
		}
		if kind.IsTransfer() {
			return s.pickSource(ctx, u, id, string(kind))
		}
		return s.pickCategory(ctx, u, id, kind)
	case "category":
		return s.patch(ctx, u, id, app.Patch{Kind: arg(1), Category: value})
	case "asset":
		return s.patch(ctx, u, id, app.Patch{Asset: value})
	case "source":
		return s.pickTarget(ctx, u, id, arg(1), value)
	case "target":
		source, err := base64.RawURLEncoding.DecodeString(arg(2))
		if err != nil {
			return nil, &app.UserError{Status: 400, Message: "转出账户读不出来，请重新选择"}
		}
		p := app.Patch{Asset: string(source), TargetAsset: value}
		if arg(1) != keepKind {
			p.Kind = arg(1)
		}
		return s.patch(ctx, u, id, p)
	case "money":
		return s.patch(ctx, u, id, app.Patch{Money: strings.ReplaceAll(value, ",", "")})
	case "time":
		return s.patch(ctx, u, id, app.Patch{Time: value})
	case "text":
		res, err := s.App.Correct(ctx, u, id, value)
		if err != nil {
			return nil, err
		}
		return mainStep(res, ""), nil
	}
	return nil, &app.UserError{Status: http.StatusNotFound, Message: "快捷指令和服务端版本不匹配，请重新安装快捷指令"}
}

func (s *Server) mainChoice(ctx context.Context, u *store.User, id, value string) (*Step, error) {
	cur, err := s.App.Current(ctx, u, id)
	if err != nil {
		return nil, err
	}
	switch value {
	case optConfirm:
		if cur.Status == "needs_confirm" {
			if cur, err = s.App.Confirm(ctx, u, id); err != nil {
				return nil, err
			}
		}
		if cur.Status == "written" {
			return done("已记账：" + cur.Summary), nil
		}
		// Still held, e.g. a transfer without both accounts: show why.
	case optEdit:
		return editStep(cur, ""), nil
	case optDelete:
		if _, err := s.App.Delete(ctx, u, id); err != nil {
			return nil, err
		}
		if cur.Status == "written" {
			return done("已从钱迹删除这笔账"), nil
		}
		return done("已放弃，这笔没有记账"), nil
	}
	return mainStep(cur, ""), nil
}

func (s *Server) editChoice(ctx context.Context, u *store.User, id, value string) (*Step, error) {
	cur, err := s.App.Current(ctx, u, id)
	if err != nil {
		return nil, err
	}
	kind := kindOf(cur.Kind)
	switch value {
	case optAsset:
		if kind.IsTransfer() {
			return s.pickSource(ctx, u, id, keepKind)
		}
		o, err := s.App.Options(ctx, u)
		if err != nil {
			return nil, err
		}
		return choose("用哪个账户？", o.AssetsOrNone, id, "asset"), nil
	case optKind:
		var labels []string
		for _, k := range app.Kinds {
			labels = append(labels, k.Label())
		}
		return choose("改成哪种类型？", labels, id, "kind"), nil
	case optCategory:
		if kind.IsTransfer() {
			return editStep(cur, "转账和还款没有分类"), nil
		}
		return s.pickCategory(ctx, u, id, kind)
	case optMoney:
		return &Step{Action: "number", Prompt: "金额（现在是 " + cur.Money + "）", Next: flowPath(id, "money")}, nil
	case optTime:
		return &Step{Action: "text", Prompt: "时间，格式如 " + cur.Time, Default: cur.Time, Next: flowPath(id, "time")}, nil
	case optText:
		return &Step{Action: "text", Prompt: "怎么改？例如：改成打车，用支付宝，昨天的", Next: flowPath(id, "text")}, nil
	case optRefresh:
		info, err := s.App.RefreshCatalog(ctx, u)
		if err != nil {
			return nil, err
		}
		return editStep(cur, info.Message), nil
	case optBack:
		return mainStep(cur, ""), nil
	}
	return editStep(cur, ""), nil
}

func (s *Server) pickCategory(ctx context.Context, u *store.User, id string, kind recognize.Kind) (*Step, error) {
	o, err := s.App.Options(ctx, u)
	if err != nil {
		return nil, err
	}
	list := o.ExpenseCategories
	if kind == recognize.KindIncome {
		list = o.IncomeCategories
	}
	if len(list) == 0 {
		return nil, &app.UserError{Status: 400, Message: "钱迹里还没有" + kind.Label() + "分类"}
	}
	return choose("记到哪个分类？", list, id, "category", string(kind)), nil
}

// pickSource starts choosing both accounts of a transfer; kind is the new
// kind's code, or keepKind.
func (s *Server) pickSource(ctx context.Context, u *store.User, id, kind string) (*Step, error) {
	o, err := s.App.Options(ctx, u)
	if err != nil {
		return nil, err
	}
	if len(o.Assets) < 2 {
		return nil, &app.UserError{Status: 400, Message: "转账需要至少两个账户，请先在钱迹里添加"}
	}
	return choose("从哪个账户转出？", o.Assets, id, "source", kind), nil
}

// pickTarget carries the chosen source account in the path, base64-encoded so
// the URL stays plain ASCII.
func (s *Server) pickTarget(ctx context.Context, u *store.User, id, kind, source string) (*Step, error) {
	o, err := s.App.Options(ctx, u)
	if err != nil {
		return nil, err
	}
	targets := slices.DeleteFunc(o.Assets, func(a string) bool { return a == source })
	prompt := "转入哪个账户？"
	if kind == string(recognize.KindCredit) {
		prompt = "还的是哪个信用账户？"
	}
	return choose(prompt, targets, id, "target", kind, base64.RawURLEncoding.EncodeToString([]byte(source))), nil
}

func (s *Server) patch(ctx context.Context, u *store.User, id string, p app.Patch) (*Step, error) {
	res, err := s.App.Update(ctx, u, id, p)
	if err != nil {
		return nil, err
	}
	return mainStep(res, ""), nil
}

// mainStep shows a record with 确认 / 修改 / 删除, or ends the run when there
// is nothing left to do.
func mainStep(res *app.Result, note string) *Step {
	switch res.Status {
	case "rejected":
		return done("没有记账：" + res.Reason)
	case "deleted", "discarded":
		return done("这笔已经删除了")
	}
	prompt := "已记账：" + res.Summary
	if res.Status == "needs_confirm" {
		prompt = res.Reason + "\n" + res.Summary
	}
	step := choose(withNote(note, prompt), []string{optConfirm, optEdit, optDelete}, res.RecordID, "main")
	step.RecordID = res.RecordID
	return step
}

func editStep(cur *app.Result, note string) *Step {
	opts := []string{optAsset, optKind}
	if !kindOf(cur.Kind).IsTransfer() {
		opts = append(opts, optCategory)
	}
	opts = append(opts, optMoney, optTime, optText, optRefresh, optBack)
	step := choose(withNote(note, "改哪一项？\n"+cur.Summary), opts, cur.RecordID, "edit")
	step.RecordID = cur.RecordID
	return step
}

func choose(prompt string, options []string, id string, path ...string) *Step {
	return &Step{Action: "choose", Prompt: prompt, Options: options, Next: flowPath(id, path...)}
}

func done(message string) *Step { return &Step{Action: "done", Message: message} }

func withNote(note, text string) string {
	if note == "" {
		return text
	}
	return note + "\n" + text
}

func flowPath(id string, parts ...string) string {
	path := "/v1/flow/" + url.PathEscape(id)
	for _, p := range parts {
		path += "/" + url.PathEscape(p)
	}
	return path
}

// kindOf maps a kind's label back to the kind; unknown labels give "".
func kindOf(label string) recognize.Kind {
	for _, k := range app.Kinds {
		if k.Label() == label {
			return k
		}
	}
	return ""
}

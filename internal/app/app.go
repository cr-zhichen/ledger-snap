// Package app ties recognition, the Qianji client and the store together into
// the operations the HTTP API exposes.
package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"ledger-snap/internal/config"
	"ledger-snap/internal/qianji"
	"ledger-snap/internal/recognize"
	"ledger-snap/internal/store"
)

// UserError is shown to the shortcut user as is.
type UserError struct {
	Status  int
	Message string
}

func (e *UserError) Error() string { return e.Message }

func userErr(status int, format string, args ...any) error {
	return &UserError{Status: status, Message: fmt.Sprintf(format, args...)}
}

type Service struct {
	Config     *config.Config
	Store      *store.Store
	Qianji     *qianji.Client
	Recognizer *recognize.Recognizer
	Invites    []string
	Log        *slog.Logger // nil means slog.Default()

	locks sync.Map // uid -> *sync.Mutex
	syncs sync.Map // uid -> *billSync
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Service) lock(uid string) func() {
	m, _ := s.locks.LoadOrStore(uid, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// Login checks the invite code, logs into Qianji, loads the catalog, starts
// syncing the bills in the background and returns a new service token.
func (s *Service) Login(ctx context.Context, account, passwordMD5, invite string) (string, error) {
	account, passwordMD5 = strings.TrimSpace(account), strings.ToLower(strings.TrimSpace(passwordMD5))
	if account == "" || len(passwordMD5) != 32 {
		return "", userErr(400, "需要钱迹账号和 32 位密码 MD5")
	}
	if !slices.Contains(s.Invites, strings.TrimSpace(invite)) {
		return "", userErr(403, "邀请码不对")
	}
	device := randomHex(16)
	if u, err := s.Store.UserByAccount(ctx, account); err == nil {
		device = u.Device
	} else if !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	session, err := s.Qianji.Login(ctx, account, passwordMD5, device)
	if qianji.IsKind(err, qianji.ErrLoginRejected) {
		return "", userErr(401, "钱迹拒绝登录，请检查账号和密码")
	}
	if err != nil {
		return "", err
	}
	u := &store.User{UID: session.UID, Account: account, PasswordMD5: passwordMD5, QJToken: session.Token,
		Device: device, Invite: invite, CreatedAt: time.Now().Unix()}
	defer s.lock(u.UID)()
	if err := s.Store.SaveUser(ctx, u); err != nil {
		return "", err
	}
	if _, err := s.refreshCatalog(ctx, u); err != nil {
		return "", err
	}
	token := randomHex(32)
	if err := s.Store.AddToken(ctx, hashToken(token), u.UID, time.Now().Unix()); err != nil {
		return "", err
	}
	s.syncSoon(u.UID)
	return token, nil
}

func (s *Service) Authenticate(ctx context.Context, token string) (*store.User, error) {
	if token == "" {
		return nil, userErr(401, "缺少登录凭据")
	}
	u, err := s.Store.UserByToken(ctx, hashToken(token))
	if errors.Is(err, store.ErrNotFound) {
		return nil, userErr(401, "登录已失效，请重新登录")
	}
	return u, err
}

func session(u *store.User) qianji.Session {
	return qianji.Session{UID: u.UID, Token: u.QJToken, Device: u.Device}
}

// withSession runs fn and, if the Qianji token expired, logs in again with the
// stored password MD5 and retries once. An auth failure means the request was
// not applied, so retrying writes is safe too.
func (s *Service) withSession(ctx context.Context, u *store.User, fn func(qianji.Session) error) error {
	err := fn(session(u))
	if !qianji.IsKind(err, qianji.ErrAuth) {
		return err
	}
	fresh, err := s.Qianji.Login(ctx, u.Account, u.PasswordMD5, u.Device)
	if qianji.IsKind(err, qianji.ErrLoginRejected) {
		return userErr(401, "钱迹登录失效且无法自动重新登录，请重新登录")
	}
	if err != nil {
		return err
	}
	u.QJToken = fresh.Token
	if err := s.Store.SaveQianjiToken(ctx, u.UID, fresh.Token); err != nil {
		return err
	}
	return fn(fresh)
}

func (s *Service) refreshCatalog(ctx context.Context, u *store.User) (qianji.Catalog, error) {
	var catalog qianji.Catalog
	err := s.withSession(ctx, u, func(qs qianji.Session) error {
		cats, err := s.Qianji.Categories(ctx, qs, qianji.NoID, -1)
		if err != nil {
			return err
		}
		assets, err := s.Qianji.Assets(ctx, qs, 0)
		if err != nil {
			return err
		}
		if catalog, err = qianji.ParseCatalog(cats, assets); err != nil {
			return err
		}
		currencies, err := s.Qianji.Currencies(ctx, qs)
		if err != nil {
			return err
		}
		if catalog.Prices, err = qianji.ParsePrices(currencies); err != nil {
			return err
		}
		catalog.BaseCurrency, err = s.Qianji.BaseCurrency(ctx, qs)
		return err
	})
	if err != nil {
		return catalog, err
	}
	data, _ := json.Marshal(catalog)
	u.Catalog, u.CatalogAt = string(data), time.Now().Unix()
	return catalog, s.Store.SaveCatalog(ctx, u.UID, u.Catalog, u.CatalogAt)
}

func (s *Service) catalog(ctx context.Context, u *store.User) (qianji.Catalog, error) {
	if u.Catalog != "" && time.Since(time.Unix(u.CatalogAt, 0)) < s.Config.Qianji.CatalogTTL.Duration {
		var c qianji.Catalog
		// Catalogs cached before currency support have no prices.
		if err := json.Unmarshal([]byte(u.Catalog), &c); err == nil && len(c.Prices) > 0 {
			return c, nil
		}
	}
	return s.refreshCatalog(ctx, u)
}

// Result is what the shortcut receives.
type Result struct {
	RecordID  string   `json:"record_id"`
	Status    string   `json:"status"`
	Kind      string   `json:"kind,omitempty"` // 支出 | 收入 | 转账 | 信用卡还款
	Summary   string   `json:"summary"`
	Reason    string   `json:"reason,omitempty"`
	Uncertain []string `json:"uncertain,omitempty"`
	Money     string   `json:"money,omitempty"`
	Currency  string   `json:"currency,omitempty"` // of Money; "" is the account's
	// Other is the other side of a two-currency bill: what a transfer's
	// target received, or what a foreign purchase cost in the account's
	// currency.
	Other         string `json:"other,omitempty"`
	OtherCurrency string `json:"other_currency,omitempty"`
	Time          string `json:"time,omitempty"` // TimeLayout
}

// TimeLayout is how times are shown to and typed by the user.
const TimeLayout = "2006-01-02 15:04"

func (s *Service) result(rec *store.Record, d *recognize.Draft, c qianji.Catalog) *Result {
	res := &Result{RecordID: rec.ID, Status: rec.Status, Kind: d.Kind.Label(), Summary: rec.Summary, Uncertain: d.Uncertain}
	if rec.Summary != "" {
		res.Money, res.Currency, res.Time = d.Money, d.Currency, d.Time.In(s.Config.Location()).Format(TimeLayout)
		m, _ := moneyOf(d, c)
		res.Other, res.OtherCurrency = m.Other, m.OtherCurrency
	}
	return res
}

func (s *Service) Recognize(ctx context.Context, u *store.User, image []byte, upload time.Time) (*Result, error) {
	defer s.lock(u.UID)()
	// For the next recognition and edit; this one uses the cache as it is.
	s.syncSoon(u.UID)
	catalog, err := s.catalog(ctx, u)
	if err != nil {
		return nil, err
	}
	mem, err := s.Store.Memory(ctx, u.UID)
	if err != nil {
		return nil, err
	}
	input := recognize.Input{
		Image: image, UploadTime: upload, Catalog: catalog,
		Memory: recognize.Memory{AssetAliases: mem.AssetAliases, MerchantRules: mem.MerchantRules},
	}
	draft, trace, err := s.Recognizer.Recognize(ctx, input)
	if err != nil {
		return nil, err
	}
	if unmatched(draft) && time.Since(time.Unix(u.CatalogAt, 0)) >= recheckAfter {
		draft, trace, catalog = s.recheck(ctx, u, input, draft, trace)
	}
	rec := &store.Record{ID: "r_" + randomHex(10), UID: u.UID, CreatedAt: upload.Unix()}
	rec.ImagePath, err = s.saveImage(u.UID, rec.ID, image)
	if err != nil {
		return nil, err
	}
	rec.Trace = toJSON(trace)
	return s.settle(ctx, u, rec, nil, draft, catalog, modeNew)
}

// recheckAfter is how old the catalog must be before an unmatched draft
// refreshes it; a fresher one already has what the app has.
const recheckAfter = time.Minute

// unmatched reports a draft whose account or category may be missing from
// the cached catalog: none was picked, or one was picked without confidence.
func unmatched(d *recognize.Draft) bool {
	if d.Status != "ok" || d.Kind == recognize.KindRefund {
		return false
	}
	for _, field := range []string{"asset", "target_asset", "category"} {
		if slices.Contains(d.Uncertain, field) {
			return true
		}
	}
	if d.Kind.IsTransfer() {
		return d.AssetID == "" || d.TargetID == ""
	}
	return d.AssetID == "" || d.CategoryID == ""
}

// recheck refreshes the catalog once for an unmatched draft, in case the
// account or category was added in the app since it was cached, and
// recognizes again with the same OCR result when the catalog changed.
func (s *Service) recheck(ctx context.Context, u *store.User, in recognize.Input, d *recognize.Draft, tr *recognize.Trace) (*recognize.Draft, *recognize.Trace, qianji.Catalog) {
	fresh, err := s.refreshCatalog(ctx, u)
	if err != nil {
		tr.CatalogRefresh = "刷新账户和分类失败：" + err.Error()
		return d, tr, in.Catalog
	}
	if slices.Equal(in.Catalog.Assets, fresh.Assets) && slices.Equal(in.Catalog.Categories, fresh.Categories) {
		tr.CatalogRefresh = "账户和分类没有变化"
		return d, tr, fresh
	}
	in.Catalog, in.Lines = fresh, tr.OCRLines
	d2, tr2, err := s.Recognizer.Recognize(ctx, in)
	if err != nil {
		tr.CatalogRefresh = "账户和分类有更新，重新识别失败：" + err.Error()
		return d, tr, fresh
	}
	tr2.OCRMs, tr2.Cost = tr.OCRMs, tr2.Cost+tr.Cost
	tr2.CatalogRefresh = "账户和分类有更新，已重新识别"
	return d2, tr2, fresh
}

// settleMode says why a draft is being settled.
type settleMode int

const (
	// modeNew is a fresh recognition: hold back possible duplicates and, when
	// configured, drafts with uncertain fields.
	modeNew settleMode = iota
	// modeConfirm is the user confirming a held-back record: write it.
	modeConfirm
	// modeEdit is an edit: rewrite a written bill, keep a held-back record
	// waiting for confirmation.
	modeEdit
)

var fieldNames = map[string]string{"category": "分类", "asset": "账户", "target_asset": "转入账户", "time": "时间"}

// settle writes a draft (or records why not) and stores the record. prev is
// the draft before an edit; see write.
func (s *Service) settle(ctx context.Context, u *store.User, rec *store.Record, prev, draft *recognize.Draft, catalog qianji.Catalog, mode settleMode) (*Result, error) {
	rec.Draft = toJSON(draft)
	reason := ""
	hold := func(why string) { rec.Status, reason = "needs_confirm", why }
	switch {
	case draft.Status != "ok":
		rec.Status, reason = "rejected", draft.Reason
	case draft.Kind == recognize.KindRefund:
		rec.Status, reason = "rejected", "这是一笔退款，暂不支持自动记录，请在钱迹的原账单上添加退款"
	default:
		rec.Summary = Summary(draft, catalog, s.Config.Location())
		if why := transferProblem(draft, catalog); why != "" {
			hold(why)
			break
		}
		m, err := moneyOf(draft, catalog)
		if err != nil {
			hold(err.Error())
			break
		}
		if mode == modeEdit && rec.Status == "needs_confirm" {
			hold("")
			break
		}
		if mode == modeNew {
			dup, err := s.duplicate(ctx, u, draft, m, catalog)
			if err != nil {
				return nil, err
			}
			if dup != "" {
				hold("可能重复：" + dup)
				break
			}
			if m.Estimated {
				hold(fmt.Sprintf("截图里没有%s，按钱迹汇率估算为 %s %s，不对就修改%s", OtherLabel(draft.Kind), m.Other, m.OtherCurrency, OtherLabel(draft.Kind)))
				break
			}
			if s.Config.Recognize.ConfirmUncertain && len(draft.Uncertain) > 0 {
				var names []string
				for _, f := range draft.Uncertain {
					names = append(names, fieldNames[f])
				}
				hold("有几项拿不准：" + strings.Join(names, "、"))
				break
			}
		}
		if err := s.write(ctx, u, rec, prev, draft, catalog); err != nil {
			return nil, err
		}
		rec.Status = "written"
	}
	if err := s.Store.SaveRecord(ctx, rec); err != nil {
		return nil, err
	}
	res := s.result(rec, draft, catalog)
	res.Reason = reason
	if res.Summary == "" {
		res.Summary = reason
	}
	return res, nil
}

// duplicate describes an earlier bill with the draft's amount and accounts
// within the dedup window: one this service wrote, or one in the bill cache,
// such as a bill recorded in the app. m is the draft's bill amount.
func (s *Service) duplicate(ctx context.Context, u *store.User, d *recognize.Draft, m billMoney, c qianji.Catalog) (string, error) {
	window := s.Config.Recognize.DedupWindow.Duration
	loc := s.Config.Location()
	recent, err := s.Store.WrittenSince(ctx, u.UID, time.Now().Add(-24*time.Hour).Unix())
	if err != nil {
		return "", err
	}
	for _, r := range recent {
		var prev recognize.Draft
		if json.Unmarshal([]byte(r.Draft), &prev) != nil {
			continue
		}
		gap := prev.Time.Sub(d.Time)
		if prev.Money == d.Money && prev.AssetID == d.AssetID && prev.TargetID == d.TargetID && prev.Kind == d.Kind && gap <= window && gap >= -window {
			return time.Unix(r.CreatedAt, 0).In(loc).Format("15:04") + " 已记过「" + r.Summary + "」", nil
		}
	}
	bills, err := s.Store.BillsBetween(ctx, u.UID, d.Time.Add(-window).Unix(), d.Time.Add(window).Unix())
	if err != nil {
		return "", err
	}
	for _, b := range bills {
		qb, err := parseBill(b.Raw)
		if err != nil {
			continue
		}
		prev, ok := qb.draft()
		if !ok || prev.Kind != d.Kind || !sameAmount(qb.Money, json.Number(m.Value)) ||
			normID(prev.AssetID) != normID(d.AssetID) || normID(prev.TargetID) != normID(d.TargetID) {
			continue
		}
		return "钱迹里已有「" + Summary(prev, c, loc) + "」", nil
	}
	return "", nil
}

// normID treats NoID as no ID, the way bills read from Qianji do.
func normID(id string) string {
	if id == qianji.NoID {
		return ""
	}
	return id
}

// transferProblem explains why a transfer or repayment cannot be written yet.
func transferProblem(d *recognize.Draft, c qianji.Catalog) string {
	if !d.Kind.IsTransfer() {
		return ""
	}
	from, okFrom := c.Asset(d.AssetID)
	target, okTarget := c.Asset(d.TargetID)
	switch {
	case !okFrom || !okTarget:
		return d.Kind.Label() + "需要转出和转入两个账户，请点修改选择"
	case from.ID == target.ID:
		return "转出和转入账户不能相同，请点修改选择"
	case d.Kind == recognize.KindCredit && target.Type != qianji.AssetTypeCredit:
		return "还款的转入账户应该是信用账户，请点修改选择"
	}
	return ""
}

func billType(k recognize.Kind) int {
	switch k {
	case recognize.KindIncome:
		return qianji.TypeIncome
	case recognize.KindTransfer:
		return qianji.TypeTransfer
	case recognize.KindCredit:
		return qianji.TypeCreditPayment
	}
	return qianji.TypeExpense
}

func billInput(d *recognize.Draft, c qianji.Catalog, m billMoney) qianji.BillInput {
	in := qianji.BillInput{Type: billType(d.Kind), Money: m.Value, Currency: m.Currency, Time: d.Time, Remark: d.Merchant}
	if d.Kind.IsTransfer() {
		from, _ := c.Asset(d.AssetID)
		target, _ := c.Asset(d.TargetID)
		in.FromID, in.TargetID, in.FromName, in.TargetName = from.ID, target.ID, from.Name, target.Name
		return in
	}
	in.CategoryID, in.AssetID = d.CategoryID, d.AssetID
	return in
}

func isTransferType(t int) bool { return t == qianji.TypeTransfer || t == qianji.TypeCreditPayment }

// write creates or replaces the record's bill. The bill ID is fixed per
// record, so resending after a network error cannot create a duplicate.
// Switching between an ordinary bill and a transfer replaces the bill with a
// new ID instead of changing its type in place.
//
// For an edit of a written bill, rec.Bill is the bill as it is in Qianji and
// prev the draft before the edit: only the fields the edit changes are
// replaced, so what was changed in the app stays.
func (s *Service) write(ctx context.Context, u *store.User, rec *store.Record, prev, d *recognize.Draft, catalog qianji.Catalog) error {
	m, err := moneyOf(d, catalog)
	if err != nil {
		return userErr(400, "%v", err)
	}
	input := billInput(d, catalog, m)
	var deletes []string
	if rec.BillID != "" && rec.Bill != "" {
		var old struct {
			Type int `json:"type"`
		}
		if json.Unmarshal([]byte(rec.Bill), &old) == nil && isTransferType(old.Type) != isTransferType(input.Type) {
			deletes = []string{rec.BillID}
			rec.BillID = ""
		}
	}
	if rec.BillID == "" {
		rec.BillID = qianji.NewBillID()
	}
	bill := qianji.NewBill(u.UID, rec.BillID, input)
	if prev != nil && rec.Bill != "" && deletes == nil {
		// If the old draft no longer converts, every field counts as changed.
		before := map[string]any{}
		if pm, err := moneyOf(prev, catalog); err == nil {
			before = qianji.NewBill(u.UID, rec.BillID, billInput(prev, catalog, pm))
		}
		if bill, err = mergeBill(rec.Bill, before, bill); err != nil {
			return err
		}
	}
	var receipt qianji.SyncReceipt
	send := func(qs qianji.Session) (err error) {
		receipt, err = s.Qianji.SyncBills(ctx, qs, []map[string]any{bill}, deletes)
		return err
	}
	err = s.withSession(ctx, u, send)
	if qianji.IsKind(err, qianji.ErrNetwork) {
		err = s.withSession(ctx, u, send)
	}
	if err != nil {
		return userErr(502, "写入钱迹失败，可能已经记上，请在钱迹里确认：%v", err)
	}
	if !receipt.Accepted([]string{rec.BillID}, nil, deletes) {
		return userErr(502, "钱迹没有确认这笔账单（%+v）", receipt)
	}
	rec.Bill = toJSON(bill)
	s.cacheWrite(ctx, u.UID, bill, deletes)
	return nil
}

func (s *Service) loadRecord(ctx context.Context, u *store.User, id string) (*store.Record, *recognize.Draft, error) {
	rec, err := s.Store.Record(ctx, u.UID, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, userErr(404, "找不到这条记录")
	}
	if err != nil {
		return nil, nil, err
	}
	var d recognize.Draft
	if err := json.Unmarshal([]byte(rec.Draft), &d); err != nil {
		return nil, nil, err
	}
	return rec, &d, nil
}

// Current reports a record as it stands, to show it again.
func (s *Service) Current(ctx context.Context, u *store.User, id string) (*Result, error) {
	rec, d, err := s.loadRecord(ctx, u, id)
	if err != nil {
		return nil, err
	}
	catalog, err := s.catalog(ctx, u)
	if err != nil {
		return nil, err
	}
	return s.result(rec, d, catalog), nil
}

// Confirm writes a record that was held back, e.g. as a possible duplicate.
func (s *Service) Confirm(ctx context.Context, u *store.User, id string) (*Result, error) {
	defer s.lock(u.UID)()
	rec, d, err := s.loadRecord(ctx, u, id)
	if err != nil {
		return nil, err
	}
	if rec.Status != "needs_confirm" {
		return nil, userErr(409, "这条记录不需要确认")
	}
	catalog, err := s.catalog(ctx, u)
	if err != nil {
		return nil, err
	}
	return s.settle(ctx, u, rec, nil, d, catalog, modeConfirm)
}

// latest waits for a sync and brings a written record up to date with its
// bill in Qianji: rec.Bill becomes that bill, and the returned draft carries
// what was changed in the app. An edit then builds on both.
func (s *Service) latest(ctx context.Context, u *store.User, rec *store.Record, d *recognize.Draft) (*recognize.Draft, error) {
	if err := s.waitSync(ctx, u.UID); err != nil {
		return nil, err
	}
	b, err := s.Store.Bill(ctx, u.UID, rec.BillID)
	if errors.Is(err, store.ErrNotFound) {
		// The sync succeeded, so the cache holds every bill Qianji has.
		rec.Status = "deleted"
		if err := s.Store.SaveRecord(ctx, rec); err != nil {
			return nil, err
		}
		return nil, userErr(409, "这笔账已经在钱迹里删掉了")
	}
	if err != nil {
		return nil, err
	}
	next, err := reconcile(d, rec.Bill, b.Raw)
	if err != nil {
		return nil, err
	}
	rec.Bill = b.Raw
	return next, nil
}

// Correct applies a free-text correction, rewrites the bill and learns from it.
func (s *Service) Correct(ctx context.Context, u *store.User, id, text string) (*Result, error) {
	defer s.lock(u.UID)()
	if strings.TrimSpace(text) == "" {
		return nil, userErr(400, "修改内容不能为空")
	}
	rec, d, err := s.loadRecord(ctx, u, id)
	if err != nil {
		return nil, err
	}
	if rec.Status != "written" && rec.Status != "needs_confirm" {
		return nil, userErr(409, "这条记录已经不能修改")
	}
	catalog, err := s.catalog(ctx, u)
	if err != nil {
		return nil, err
	}
	if rec.Status == "written" {
		if d, err = s.latest(ctx, u, rec, d); err != nil {
			return nil, err
		}
	}
	next, _, err := s.Recognizer.Correct(ctx, d, catalog, text, time.Now())
	if err != nil {
		return nil, err
	}
	return s.commitEdit(ctx, u, rec, d, next, catalog)
}

// learn stores merchant and payment-method rules from a correction.
func (s *Service) learn(ctx context.Context, u *store.User, rec *store.Record, before, after *recognize.Draft) {
	now := time.Now().Unix()
	if after.CategoryID != before.CategoryID && after.Merchant != "" && after.CategoryID != "" {
		_ = s.Store.PutMerchantRule(ctx, u.UID, after.Merchant, after.CategoryID, now)
	}
	if after.AssetID != before.AssetID && after.AssetID != "" && after.AssetID != qianji.NoID {
		var trace recognize.Trace
		if json.Unmarshal([]byte(rec.Trace), &trace) == nil {
			if p := recognize.PaymentText(trace.OCRLines); p != "" {
				_ = s.Store.PutAssetAlias(ctx, u.UID, p, after.AssetID, now)
			}
		}
	}
}

// Delete removes the record's bill from Qianji, or discards an unwritten record.
func (s *Service) Delete(ctx context.Context, u *store.User, id string) (*Result, error) {
	defer s.lock(u.UID)()
	rec, _, err := s.loadRecord(ctx, u, id)
	if err != nil {
		return nil, err
	}
	switch rec.Status {
	case "written":
		var receipt qianji.SyncReceipt
		err := s.withSession(ctx, u, func(qs qianji.Session) (err error) {
			receipt, err = s.Qianji.SyncBills(ctx, qs, nil, []string{rec.BillID})
			return err
		})
		if err != nil {
			return nil, userErr(502, "从钱迹删除失败：%v", err)
		}
		if !receipt.Accepted(nil, nil, []string{rec.BillID}) && !s.goneFromQianji(ctx, u.UID, rec.BillID) {
			return nil, userErr(502, "钱迹没有确认删除（%+v）", receipt)
		}
		s.cacheWrite(ctx, u.UID, nil, []string{rec.BillID})
		s.syncSoon(u.UID)
		rec.Status = "deleted"
	case "deleted", "discarded":
	default:
		rec.Status = "discarded"
	}
	if err := s.Store.SaveRecord(ctx, rec); err != nil {
		return nil, err
	}
	return &Result{RecordID: rec.ID, Status: rec.Status, Summary: "已撤销"}, nil
}

// goneFromQianji reports a bill that a sync found deleted, e.g. in the app.
// Every bill this service writes goes into the cache, so once the first sync
// is done, a bill missing from it is not in Qianji either.
func (s *Service) goneFromQianji(ctx context.Context, uid, id string) bool {
	st, err := s.Store.SyncState(ctx, uid)
	if err != nil || !st.Initialized() {
		return false
	}
	_, err = s.Store.Bill(ctx, uid, id)
	return errors.Is(err, store.ErrNotFound)
}

func (s *Service) saveImage(uid, id string, image []byte) (string, error) {
	dir := filepath.Join(s.Config.Server.DataDir, "images", uid)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, id+".jpg")
	return path, os.WriteFile(path, image, 0o600)
}

// CleanImages deletes screenshots older than the retention period.
func (s *Service) CleanImages(ctx context.Context) error {
	before := time.Now().AddDate(0, 0, -s.Config.Server.ImageRetentionDays).Unix()
	paths, err := s.Store.ClearImagesBefore(ctx, before)
	for _, p := range paths {
		_ = os.Remove(p)
	}
	return err
}

func toJSON(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}

// Summary renders a draft the way the shortcut menu shows it; uncertain
// fields get a question mark.
func Summary(d *recognize.Draft, c qianji.Catalog, loc *time.Location) string {
	mark := func(field, text string) string {
		if slices.Contains(d.Uncertain, field) {
			return text + "?"
		}
		return text
	}
	name := func(id, empty string) string {
		if a, ok := c.Asset(id); ok {
			return a.Name
		}
		return empty
	}
	amount := d.Money
	if m, err := moneyOf(d, c); err == nil {
		amount = m.Text
	}
	parts := []string{d.Kind.Label() + " " + amount}
	if d.Kind.IsTransfer() {
		parts = append(parts, mark("asset", name(d.AssetID, "转出账户?"))+" → "+mark("target_asset", name(d.TargetID, "转入账户?")))
	} else {
		category := "未分类"
		if cat, ok := c.Category(d.CategoryID); ok {
			category = cat.Path
		}
		parts = append(parts, mark("category", category), mark("asset", name(d.AssetID, "无账户")))
	}
	parts = append(parts, mark("time", d.Time.In(loc).Format("01-02 15:04")))
	if d.Merchant != "" {
		parts = append(parts, d.Merchant)
	}
	return strings.Join(parts, " · ")
}

// Options lists the names the shortcut offers in its pickers.
type Options struct {
	Kinds             []string `json:"kinds"`
	ExpenseCategories []string `json:"expense_categories"`
	IncomeCategories  []string `json:"income_categories"`
	Assets            []string `json:"assets"`
	// AssetsOrNone adds the "no account" entry for expenses and income.
	AssetsOrNone []string `json:"assets_or_none"`
}

// Kinds are the bill kinds a user can pick, in menu order.
var Kinds = []recognize.Kind{recognize.KindExpense, recognize.KindIncome, recognize.KindTransfer, recognize.KindCredit}

// assetLabels gives every asset a unique display name.
func assetLabels(c qianji.Catalog) map[string]string {
	labels, seen := map[string]string{}, map[string]int{}
	for _, a := range c.Assets {
		seen[recognize.AssetLabel(a)]++
	}
	for _, a := range c.Assets {
		label := recognize.AssetLabel(a)
		if seen[label] > 1 {
			label += " #" + a.ID[max(0, len(a.ID)-4):]
		}
		labels[a.ID] = label
	}
	return labels
}

func (s *Service) Options(ctx context.Context, u *store.User) (*Options, error) {
	defer s.lock(u.UID)()
	c, err := s.catalog(ctx, u)
	if err != nil {
		return nil, err
	}
	o := &Options{}
	for _, k := range Kinds {
		o.Kinds = append(o.Kinds, k.Label())
	}
	for _, cat := range c.Leaves(qianji.TypeExpense) {
		o.ExpenseCategories = append(o.ExpenseCategories, cat.Path)
	}
	for _, cat := range c.Leaves(qianji.TypeIncome) {
		o.IncomeCategories = append(o.IncomeCategories, cat.Path)
	}
	labels := assetLabels(c)
	for _, a := range c.Assets {
		o.Assets = append(o.Assets, labels[a.ID])
	}
	o.AssetsOrNone = append(slices.Clone(o.Assets), NoAsset)
	return o, nil
}

// Patch is a structured edit; fields are display names as listed by Options.
// Empty fields stay unchanged.
type Patch struct {
	Kind        string `json:"kind"`
	Category    string `json:"category"`
	Asset       string `json:"asset"`
	TargetAsset string `json:"target_asset"`
	Money       string `json:"money"`
	// Other sets the other side of a two-currency bill (see Result.Other).
	Other  string `json:"other"`
	Time   string `json:"time"` // TimeLayout
	Remark string `json:"remark"`
}

// NoAsset is the picker entry for "no account".
const NoAsset = "不关联账户"

func (s *Service) applyPatch(d *recognize.Draft, p Patch, c qianji.Catalog) (*recognize.Draft, error) {
	out := *d
	out.Uncertain = nil
	if p.Kind != "" {
		found := false
		for _, k := range Kinds {
			if p.Kind == k.Label() || p.Kind == string(k) {
				out.Kind, found = k, true
			}
		}
		if !found {
			return nil, userErr(400, "不认识的类型：%s", p.Kind)
		}
	}
	labels := assetLabels(c)
	asset := func(name string) (string, error) {
		if name == NoAsset {
			return "", nil
		}
		for id, label := range labels {
			if label == name {
				return id, nil
			}
		}
		return "", userErr(http.StatusNotFound, "找不到账户：%s", name)
	}
	var err error
	if p.Asset != "" {
		if out.AssetID, err = asset(p.Asset); err != nil {
			return nil, err
		}
	}
	if p.TargetAsset != "" {
		if out.TargetID, err = asset(p.TargetAsset); err != nil {
			return nil, err
		}
	}
	if out.Kind == recognize.KindExpense || out.Kind == recognize.KindIncome {
		out.TargetID = ""
		t := billType(out.Kind)
		if p.Category != "" {
			out.CategoryID = ""
			for _, cat := range c.Leaves(t) {
				if cat.Path == p.Category {
					out.CategoryID = cat.ID
				}
			}
			if out.CategoryID == "" {
				return nil, userErr(http.StatusNotFound, "%s里没有分类：%s", out.Kind.Label(), p.Category)
			}
		} else if cat, ok := c.Category(out.CategoryID); !ok || cat.Type != t {
			// The type changed and the old category does not fit; default to
			// "其它" and let the user pick.
			out.CategoryID = ""
			for _, cat := range c.Leaves(t) {
				if out.CategoryID == "" || cat.Name == "其它" {
					out.CategoryID = cat.ID
				}
			}
			out.Uncertain = append(out.Uncertain, "category")
		}
	} else {
		out.CategoryID = ""
	}
	parseMoney := func(s string) (string, error) {
		v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil || v <= 0 || v > 9999999999.99 {
			return "", userErr(400, "金额不对：%s", s)
		}
		return strconv.FormatFloat(v, 'f', 2, 64), nil
	}
	if p.Money != "" {
		if out.Money, err = parseMoney(p.Money); err != nil {
			return nil, err
		}
	}
	if p.Other != "" {
		v, err := parseMoney(p.Other)
		if err != nil {
			return nil, err
		}
		m, _ := moneyOf(&out, c)
		switch {
		case m.OtherCurrency == "":
			return nil, userErr(400, "这笔账只涉及一种币种，改金额就行")
		case out.Currency == m.OtherCurrency:
			// The recognized amount is already the other side.
			out.Money = v
		default:
			out.Converted, out.ConvertedCurrency = v, m.OtherCurrency
		}
	}
	if p.Time != "" {
		t, err := time.ParseInLocation(TimeLayout, strings.TrimSpace(p.Time), s.Config.Location())
		if err != nil {
			return nil, userErr(400, "时间格式不对，应该像 2026-10-21 12:30 这样")
		}
		out.Time, out.TimeSource = t, "manual"
	}
	if p.Remark != "" {
		out.Merchant = strings.TrimSpace(p.Remark)
	}
	return &out, nil
}

// Update applies a structured edit chosen in the shortcut's pickers.
func (s *Service) Update(ctx context.Context, u *store.User, id string, p Patch) (*Result, error) {
	defer s.lock(u.UID)()
	rec, d, err := s.loadRecord(ctx, u, id)
	if err != nil {
		return nil, err
	}
	if rec.Status != "written" && rec.Status != "needs_confirm" {
		return nil, userErr(409, "这条记录已经不能修改")
	}
	catalog, err := s.catalog(ctx, u)
	if err != nil {
		return nil, err
	}
	if rec.Status == "written" {
		if d, err = s.latest(ctx, u, rec, d); err != nil {
			return nil, err
		}
	}
	next, err := s.applyPatch(d, p, catalog)
	var ue *UserError
	if errors.As(err, &ue) && ue.Status == http.StatusNotFound {
		// The account or category may have been added in the app since the
		// catalog was cached; refresh once and retry.
		if catalog, err = s.refreshCatalog(ctx, u); err != nil {
			return nil, err
		}
		next, err = s.applyPatch(d, p, catalog)
	}
	if errors.As(err, &ue) && ue.Status == http.StatusNotFound {
		return nil, userErr(400, "%s", ue.Message)
	}
	if err != nil {
		return nil, err
	}
	return s.commitEdit(ctx, u, rec, d, next, catalog)
}

// commitEdit writes an edited draft. A written bill is never replaced by one
// that cannot be written, such as an incomplete transfer, so the bill in
// Qianji always matches the record and a written record stays written.
func (s *Service) commitEdit(ctx context.Context, u *store.User, rec *store.Record, before, after *recognize.Draft, catalog qianji.Catalog) (*Result, error) {
	if rec.Status == "written" {
		if why := transferProblem(after, catalog); why != "" {
			return nil, userErr(400, "%s", strings.Replace(why, "请点修改选择", "请一起选好", 1))
		}
		if _, err := moneyOf(after, catalog); err != nil {
			return nil, userErr(400, "%v", err)
		}
	}
	s.learn(ctx, u, rec, before, after)
	return s.settle(ctx, u, rec, before, after, catalog, modeEdit)
}

// CatalogInfo reports a refreshed catalog.
type CatalogInfo struct {
	Message           string `json:"message"`
	Assets            int    `json:"assets"`
	ExpenseCategories int    `json:"expense_categories"`
	IncomeCategories  int    `json:"income_categories"`
}

// RefreshCatalog pulls categories and assets from Qianji now, regardless of
// the cache age.
func (s *Service) RefreshCatalog(ctx context.Context, u *store.User) (*CatalogInfo, error) {
	defer s.lock(u.UID)()
	c, err := s.refreshCatalog(ctx, u)
	if err != nil {
		return nil, err
	}
	info := &CatalogInfo{Assets: len(c.Assets), ExpenseCategories: len(c.Leaves(qianji.TypeExpense)), IncomeCategories: len(c.Leaves(qianji.TypeIncome))}
	info.Message = fmt.Sprintf("已刷新：%d 个账户，%d 个支出分类，%d 个收入分类", info.Assets, info.ExpenseCategories, info.IncomeCategories)
	return info, nil
}

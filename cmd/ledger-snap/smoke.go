package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"slices"
	"time"

	"ledger-snap/internal/config"
	"ledger-snap/internal/qianji"
)

func newQianji(cfg *config.Config) *qianji.Client {
	q := cfg.Qianji
	return qianji.New(qianji.Options{
		BaseURL: q.BaseURL, Timeout: q.Timeout.Duration,
		VersionCode: q.VersionCode, VersionName: q.VersionName, Market: q.Market,
		OSVersion: q.OSVersion, DeviceBrand: q.DeviceBrand, DeviceName: q.DeviceName,
	})
}

func randomDevice() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// runQianjiSmoke exercises login, catalogs, bill pulls and a
// create/update/delete cycle.
func runQianjiSmoke(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("qianji-smoke", flag.ExitOnError)
	cfgPath := fs.String("config", "config.toml", "配置文件")
	dump := fs.String("dump", "", "把目录原始 JSON 写到这个目录")
	write := fs.Bool("write", true, "新增、修改并删除一笔测试账单")
	_ = fs.Parse(args)
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	account, err := config.Secret("QIANJI_TEST_ACCOUNT")
	if err != nil {
		return err
	}
	digest, err := config.Secret("QIANJI_TEST_PASSWORD_MD5")
	if err != nil {
		return err
	}
	c := newQianji(cfg)
	start := time.Now()
	s, err := c.Login(ctx, account, digest, randomDevice())
	if err != nil {
		return err
	}
	fmt.Printf("登录成功 uid=%s (%v)\n", s.UID, time.Since(start).Round(time.Millisecond))

	cats, err := c.Categories(ctx, s, qianji.NoID, -1)
	if err != nil {
		return err
	}
	assets, err := c.Assets(ctx, s, 0)
	if err != nil {
		return err
	}
	books, err := c.Books(ctx, s)
	if err != nil {
		return err
	}
	fmt.Printf("分类 %d 项，资产 %d 项，账本 %d 项\n", len(cats), len(assets), len(books))
	start = time.Now()
	pulled, err := pullAll(ctx, c, s, nil)
	if err != nil {
		return err
	}
	fmt.Printf("全量拉取账单 %d 页 %d 笔 (%v)\n", pulled.pages, len(pulled.changes), time.Since(start).Round(time.Millisecond))
	if *dump != "" {
		if err := os.MkdirAll(*dump, 0o700); err != nil {
			return err
		}
		for name, list := range map[string]any{"categories": cats, "assets": assets, "books": books} {
			if err := writeJSON(fmt.Sprintf("%s/%s.json", *dump, name), list); err != nil {
				return err
			}
		}
	}
	if !*write {
		return nil
	}
	catalog, err := qianji.ParseCatalog(cats, assets)
	if err != nil {
		return err
	}
	var category qianji.Category
	for _, cat := range catalog.Categories {
		if cat.Type == qianji.TypeExpense && cat.Leaf {
			category = cat
			break
		}
	}
	if category.ID == "" {
		return fmt.Errorf("测试账号没有支出分类")
	}
	id := qianji.NewBillID()
	bill := qianji.NewBill(s.UID, id, qianji.BillInput{
		Type: qianji.TypeExpense, Money: "0.01", CategoryID: category.ID,
		Time: time.Now(), Remark: "ledger-snap 冒烟测试",
	})
	start = time.Now()
	r, err := c.SyncBills(ctx, s, []map[string]any{bill}, nil)
	if err != nil {
		return err
	}
	fmt.Printf("新增 %s 分类=%s 结果=%+v (%v)\n", id, category.Path, r, time.Since(start).Round(time.Millisecond))
	if !r.Accepted([]string{id}, nil, nil) {
		return fmt.Errorf("新增未被确认")
	}
	// Resending the same create must not produce a second bill.
	r, err = c.SyncBills(ctx, s, []map[string]any{bill}, nil)
	if err != nil {
		return err
	}
	fmt.Printf("同一 ID 重发结果=%+v\n", r)
	bill["remark"] = "ledger-snap 冒烟测试（已修改）"
	bill["money"] = "0.02"
	qianji.Touch(bill)
	r, err = c.SyncBills(ctx, s, []map[string]any{bill}, nil)
	if err != nil {
		return err
	}
	fmt.Printf("修改结果=%+v\n", r)
	if !r.Accepted(nil, []string{id}, nil) {
		return fmt.Errorf("修改未被确认")
	}
	start = time.Now()
	if pulled, err = pullAll(ctx, c, s, pulled.lastTimes); err != nil {
		return err
	}
	fmt.Printf("增量拉取 %d 页 %d 笔 (%v)\n", pulled.pages, len(pulled.changes), time.Since(start).Round(time.Millisecond))
	var got struct {
		Money float64 `json:"money"`
	}
	if raw, ok := pulled.changes[id]; !ok || json.Unmarshal(raw, &got) != nil || fmt.Sprintf("%.2f", got.Money) != "0.02" {
		return fmt.Errorf("增量拉取没有拿到修改后的测试账单：%s", pulled.changes[id])
	}
	r, err = c.SyncBills(ctx, s, nil, []string{id})
	if err != nil {
		return err
	}
	fmt.Printf("删除结果=%+v\n", r)
	if !r.Accepted(nil, nil, []string{id}) {
		return fmt.Errorf("删除未被确认")
	}
	if pulled, err = pullAll(ctx, c, s, pulled.lastTimes); err != nil {
		return err
	}
	if !slices.Contains(pulled.deletes, id) {
		return fmt.Errorf("增量拉取没有报告测试账单已删除（删除 %v）", pulled.deletes)
	}
	fmt.Println("增量拉取报告了删除")
	fmt.Println("冒烟测试通过")
	return nil
}

type pullResult struct {
	pages     int
	changes   map[string]json.RawMessage
	deletes   []string
	lastTimes json.RawMessage
}

// pullAll runs one /syncv2/pull round the way the bill cache does: a full
// sync without lastTimes, otherwise the changes since then.
func pullAll(ctx context.Context, c *qianji.Client, s qianji.Session, lastTimes json.RawMessage) (pullResult, error) {
	out := pullResult{changes: map[string]json.RawMessage{}}
	cur := qianji.Cursor{BookID: qianji.NoID, LastTimes: lastTimes}
	for {
		page, err := c.PullBills(ctx, s, cur)
		if err != nil {
			return out, err
		}
		out.pages++
		for _, b := range page.Changes {
			out.changes[b.ID] = b.Raw
		}
		out.deletes = append(out.deletes, page.Deletes...)
		out.lastTimes = page.LastTimes
		if !page.HasMore {
			return out, nil
		}
		if out.pages >= 1000 {
			return out, fmt.Errorf("拉取超过 1000 页")
		}
		cur = page.Next
	}
}

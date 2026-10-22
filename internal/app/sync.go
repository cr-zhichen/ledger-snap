package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"ledger-snap/internal/qianji"
	"ledger-snap/internal/store"
)

// The bill cache holds the user's Qianji bills, including those recorded or
// changed in the app (docs/design.md 6.4). Recognition never waits for a
// sync and uses the cache as it is; an edit waits for one, so it starts from
// the bill as it is in Qianji. Syncs run outside the user's lock, so the next
// request never queues behind one.

// billSync coalesces one user's syncs: one runs at a time, and requests for a
// fresh one while it runs share the run after it.
type billSync struct {
	mu      sync.Mutex
	running *syncRun
	next    *syncRun
}

type syncRun struct {
	done chan struct{}
	err  error
}

const (
	// syncTimeout bounds a sync, which runs on after the request that
	// started it; a first sync of a long history takes many pages.
	syncTimeout = 10 * time.Minute
	// maxSyncPages and maxSyncBytes stop a sync whose paging never ends.
	maxSyncPages = 1000
	maxSyncBytes = 256 << 20
)

// syncSoon starts a sync in the background, unless one is running.
func (s *Service) syncSoon(uid string) { s.startSync(uid, false) }

// startSync returns a sync run; with fresh, one that starts after the call.
func (s *Service) startSync(uid string, fresh bool) *syncRun {
	v, _ := s.syncs.LoadOrStore(uid, &billSync{})
	b := v.(*billSync)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.running == nil {
		b.running = &syncRun{done: make(chan struct{})}
		go s.runSyncs(uid, b)
		return b.running
	}
	if !fresh {
		return b.running
	}
	if b.next == nil {
		b.next = &syncRun{done: make(chan struct{})}
	}
	return b.next
}

func (s *Service) runSyncs(uid string, b *billSync) {
	b.mu.Lock()
	run := b.running
	b.mu.Unlock()
	for run != nil {
		ctx, cancel := context.WithTimeout(context.Background(), syncTimeout)
		_, run.err = s.SyncBills(ctx, uid, false)
		cancel()
		if run.err != nil {
			s.log().Warn("同步钱迹账单失败", "uid", uid, "err", run.err)
		}
		close(run.done)
		b.mu.Lock()
		run, b.next = b.next, nil
		b.running = run
		b.mu.Unlock()
	}
}

// waitSync waits, up to qianji.sync_wait, for a sync that starts now, so the
// cache has what was changed in the app until now.
func (s *Service) waitSync(ctx context.Context, uid string) error {
	run := s.startSync(uid, true)
	timer := time.NewTimer(s.Config.Qianji.SyncWait.Duration)
	defer timer.Stop()
	select {
	case <-run.done:
	case <-timer.C:
		return userErr(504, "钱迹的账单还没同步完，这次没有修改，请稍后再试")
	case <-ctx.Done():
		return ctx.Err()
	}
	var ue *UserError
	if run.err == nil || errors.As(run.err, &ue) {
		return run.err
	}
	return userErr(502, "从钱迹同步账单失败，这次没有修改，请稍后再试：%v", run.err)
}

// SyncReport describes one sync.
type SyncReport struct {
	Full    bool          `json:"full"`
	Pages   int           `json:"pages"`
	Changes int           `json:"changes"`
	Deletes int           `json:"deletes"`
	Took    time.Duration `json:"took"`
}

// SyncBills syncs the user's bill cache now: what changed since the last
// sync, or everything for a first or full sync, stored once every page has
// arrived. A sync that raced with this service's own write starts over.
func (s *Service) SyncBills(ctx context.Context, uid string, full bool) (*SyncReport, error) {
	for attempt := 1; ; attempt++ {
		r, err := s.pull(ctx, uid, full)
		if !errors.Is(err, store.ErrSyncConflict) || attempt == 3 {
			return r, err
		}
	}
}

func (s *Service) pull(ctx context.Context, uid string, full bool) (*SyncReport, error) {
	start := time.Now()
	u, err := s.Store.User(ctx, uid)
	if err != nil {
		return nil, err
	}
	before, err := s.Store.SyncState(ctx, uid)
	if err != nil {
		return nil, err
	}
	r := &SyncReport{Full: full || !before.Initialized()}
	cur := qianji.Cursor{BookID: qianji.NoID}
	if !r.Full {
		cur.LastTimes = json.RawMessage(before.LastTimes)
	}
	// LastTimes stays the same on every page, so it is not part of the key.
	position := func(c qianji.Cursor) string { return fmt.Sprintf("%s:%d:%s", c.BookID, c.PageOffset, c.PageSign) }
	seen := map[string]bool{position(cur): true}
	var changes []store.Bill
	var deletes []string
	var last json.RawMessage
	size := 0
	for {
		var page qianji.PullPage
		err := s.withSession(ctx, u, func(qs qianji.Session) (err error) {
			page, err = s.Qianji.PullBills(ctx, qs, cur)
			return err
		})
		if err != nil {
			return nil, err
		}
		r.Pages++
		for _, b := range page.Changes {
			changes = append(changes, store.Bill{ID: b.ID, Time: b.Time, Raw: string(b.Raw)})
			size += len(b.Raw)
		}
		deletes = append(deletes, page.Deletes...)
		last = page.LastTimes
		if !page.HasMore {
			break
		}
		if r.Pages >= maxSyncPages || size > maxSyncBytes {
			return nil, fmt.Errorf("钱迹账单超过 %d 页或 %d MiB，没有同步", maxSyncPages, maxSyncBytes>>20)
		}
		if seen[position(page.Next)] {
			return nil, errors.New("钱迹账单的分页回到了之前的位置，没有同步")
		}
		seen[position(page.Next)] = true
		cur = page.Next
	}
	if !json.Valid(last) || string(last) == "null" {
		return nil, errors.New("钱迹没有返回同步点，没有同步")
	}
	if err := s.Store.ApplySync(ctx, uid, before, string(last), changes, deletes, r.Full, time.Now().Unix()); err != nil {
		return nil, err
	}
	r.Changes, r.Deletes, r.Took = len(changes), len(deletes), time.Since(start)
	if r.Full {
		s.log().Info("已全量同步钱迹账单", "uid", uid, "pages", r.Pages, "bills", r.Changes, "ms", r.Took.Milliseconds())
	}
	return r, nil
}

// cacheWrite puts a bill this service wrote into the cache, and drops the
// ones it replaced or deleted. A failure only delays them to the next sync.
func (s *Service) cacheWrite(ctx context.Context, uid string, bill map[string]any, drop []string) {
	var put []store.Bill
	if bill != nil {
		raw := toJSON(bill)
		b, _ := parseBill(raw)
		put = append(put, store.Bill{ID: fmt.Sprint(bill["id"]), Time: b.unix(), Raw: raw})
	}
	if err := s.Store.PutBills(ctx, uid, put, drop); err != nil {
		s.log().Warn("更新账单缓存失败", "uid", uid, "err", err)
	}
}

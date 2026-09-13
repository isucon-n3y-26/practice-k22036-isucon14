package main

import (
	"sort"
	"sync"
	"time"

	"github.com/isucon/isucon14/webapp/go/repository"
)

// statusLogEntry は未送信追跡用の1行。DBの ride_statuses 行と1:1に対応する。
type statusLogEntry struct {
	ID        string
	RideID    string
	Status    string
	CreatedAt time.Time
	ChairSent bool
	AppSent   bool
}

type statusLogRide struct {
	UserID  string
	ChairID string // 割当前は空。SetChair で事後設定する
	Entries []*statusLogEntry
}

// StatusLog は ride_statuses の未送信集合のインメモリ索引。
// 全遷移が本アプリ経由で発生するため、commit 直後に Append すれば
// 未送信ポーリングと等価になり、接続数×1秒の重いCTEを排除できる。
// DB側のINSERT・送信済みUPDATEは durability と backfill 用に残す。
type StatusLog struct {
	mu    sync.Mutex
	rides map[string]*statusLogRide
	byID  map[string]*statusLogEntry
}

func NewStatusLog() *StatusLog {
	return &StatusLog{
		rides: make(map[string]*statusLogRide),
		byID:  make(map[string]*statusLogEntry),
	}
}

// Append は遷移の追跡を開始する。commit 後・wake 前に呼ぶこと。
func (l *StatusLog) Append(id, rideID, status, userID, chairID string, createdAt time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	rl, ok := l.rides[rideID]
	if !ok {
		rl = &statusLogRide{}
		l.rides[rideID] = rl
	}
	if userID != "" {
		rl.UserID = userID
	}
	if chairID != "" {
		rl.ChairID = chairID
	}
	e := &statusLogEntry{ID: id, RideID: rideID, Status: status, CreatedAt: createdAt}
	rl.Entries = append(rl.Entries, e)
	l.byID[id] = e
}

// SetChair は割当確定を反映する（MATCHING行の配送先設定）。
// commit 後・wake 前に呼ぶこと。
func (l *StatusLog) SetChair(rideID, chairID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if rl, ok := l.rides[rideID]; ok {
		rl.ChairID = chairID
	}
}

// ListUnsentForChair は椅子向け未送信を返す。順序はSQL版と等価:
// COMPLETED未送信を含むライド優先→ライド内は時系列昇順→上限limit。
func (l *StatusLog) ListUnsentForChair(chairID string, limit int) []RideStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	type candidate struct {
		rl       *statusLogRide
		priority int
		oldest   time.Time
	}
	var cands []candidate
	// 1. 当該椅子のライドから、未送信を持つものだけを集める。
	// priority は COMPLETED 未送信あり=0・なし=1（SQLのMIN(CASE...)と等価）、
	// oldest は最古の未送信時刻（SQLのMIN(created_at)と等価）。
	for _, rl := range l.rides {
		if rl.ChairID != chairID {
			continue
		}
		has := false
		priority := 1
		var oldest time.Time
		for _, e := range rl.Entries {
			if e.ChairSent {
				continue
			}
			if !has {
				oldest = e.CreatedAt
				has = true
			}
			if e.Status == "COMPLETED" {
				priority = 0
			}
		}
		if !has {
			continue
		}
		cands = append(cands, candidate{rl: rl, priority: priority, oldest: oldest})
	}
	// 2. ライド間を priority→oldest の順に並べる（SQLのORDER BYと等価）。
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].priority != cands[j].priority {
			return cands[i].priority < cands[j].priority
		}
		return cands[i].oldest.Before(cands[j].oldest)
	})
	// 3. 並べた順にライド内の未送信を時系列順（追記順）で取り出し、上限で切る。
	var out []RideStatus
	for _, c := range cands {
		for _, e := range c.rl.Entries {
			if e.ChairSent {
				continue
			}
			out = append(out, RideStatus{ID: e.ID, RideID: e.RideID, Status: e.Status, CreatedAt: e.CreatedAt})
			if len(out) >= limit {
				return out
			}
		}
	}
	return out
}

// ListUnsentForUser はユーザー向け未送信を時系列昇順で返す（SQL版と等価）。
func (l *StatusLog) ListUnsentForUser(userID string, limit int) []RideStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	// タイの決定性を保つためライドID順に集めて安定ソートする。
	// （SQL版も同刻の順序は不定のため、決定性の範囲で等価）
	var rideIDs []string
	for rideID, rl := range l.rides {
		if rl.UserID == userID {
			rideIDs = append(rideIDs, rideID)
		}
	}
	sort.Strings(rideIDs)
	var all []*statusLogEntry
	for _, rideID := range rideIDs {
		for _, e := range l.rides[rideID].Entries {
			if !e.AppSent {
				all = append(all, e)
			}
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].CreatedAt.Before(all[j].CreatedAt) })
	var out []RideStatus
	for _, e := range all {
		out = append(out, RideStatus{ID: e.ID, RideID: e.RideID, Status: e.Status, CreatedAt: e.CreatedAt})
		if len(out) >= limit {
			break
		}
	}
	return out
}

func (l *StatusLog) markSent(id string, chair bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.byID[id]
	if !ok {
		return
	}
	if chair {
		e.ChairSent = true
	} else {
		e.AppSent = true
	}
	// 両送達済みは追跡不要のため破棄する
	if e.ChairSent && e.AppSent {
		delete(l.byID, id)
		if rl, ok := l.rides[e.RideID]; ok {
			for i, x := range rl.Entries {
				if x == e {
					rl.Entries = append(rl.Entries[:i], rl.Entries[i+1:]...)
					break
				}
			}
			if len(rl.Entries) == 0 {
				delete(l.rides, e.RideID)
			}
		}
	}
}

// MarkChairSent は椅子向け送達を記録する。DB側のUPDATEと併用すること。
func (l *StatusLog) MarkChairSent(id string) { l.markSent(id, true) }

// MarkAppSent はユーザー向け送達を記録する。DB側のUPDATEと併用すること。
func (l *StatusLog) MarkAppSent(id string) { l.markSent(id, false) }

// Clear は全追跡を破棄する。postInitialize（DB全初期化）用。
func (l *StatusLog) Clear() {
	l.mu.Lock()
	l.rides = make(map[string]*statusLogRide)
	l.byID = make(map[string]*statusLogEntry)
	l.mu.Unlock()
}

// Backfill は未送信行（椅子・ユーザーのどちらかが未送信）を投入する。
// 起動時・初期化時の復元用。事前に Clear すること。created_at 昇順で渡すこと。
func (l *StatusLog) Backfill(rows []repository.UnsentStatusRow) {
	for _, row := range rows {
		l.mu.Lock()
		rl, ok := l.rides[row.RideID]
		if !ok {
			rl = &statusLogRide{UserID: row.UserID}
			if row.ChairID.Valid {
				rl.ChairID = row.ChairID.String
			}
			l.rides[row.RideID] = rl
		}
		e := &statusLogEntry{
			ID:        row.ID,
			RideID:    row.RideID,
			Status:    row.Status,
			CreatedAt: row.CreatedAt,
			ChairSent: row.ChairSentAt != nil,
			AppSent:   row.AppSentAt != nil,
		}
		rl.Entries = append(rl.Entries, e)
		l.byID[row.ID] = e
		l.mu.Unlock()
	}
}

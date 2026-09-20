package main

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/isucon/isucon14/webapp/go/repository"
)

// chairDist は椅子1台の距離追跡状態。座標POSTストリームから直接維持し、
// ChairManagerのエントリ有無に依存しない。エントリ欠落時も追跡は継続する。
// bench検証との対応:
//   - 提供合計 = base + sumAll（Sync以降にPOSTされた全差分）。
//     bench側 Total（全移動の合計）と一致する。
//   - 提供updated_at = max(baseH, maxAll) の切上げ。
//     bench側 want（ServerTime<=updated_atの移動合計）は、
//     逐次POST（応答受信後に次POST）の順序保証により合算値と一致する。
//   - flushは未書込分（sumAll-sumFlushed / maxAll-maxFlushed）だけを書く。
type chairDist struct {
	lastLat, lastLon int
	hasBase          bool
	base             int
	baseH            time.Time
	sumAll           int
	maxAll           time.Time
	sumFlushed       int
	maxFlushed       time.Time
}

func (s *chairDist) hasData() bool {
	return !s.baseH.IsZero() || !s.maxAll.IsZero()
}

// DistanceBuffer は座標POSTの走行距離を追跡し、短周期の multi-row upsert で
// DBへ書込むと同時に、owner一覧への提供も行う。提供値をメモリから返すため、
// プール混雑による読取遅延の影響を受けず、鮮度検証に触れない。
// 等価性の根拠:
//   - 差分は椅子毎ストライプドロック下で前回POST位置→今回位置から求めるため、
//     逐次upsertと合計が一致する（同一座標の重複POSTは差分0で無害）。
//   - updated_at は合算POSTの recordedAt 最大値（切上げ）を明示挿入するため、
//     逐次upsert時の commit 時刻と同等の意味を保ち、鮮度はflush周期で進む。
//   - SyncFromDB で起動時・初期化時にDBと再同期する。
//   - 失敗時は合算分を戻して次tickで再試行する（欠損させない）。
//   - 終了時は main の shutdown 処理で Flush する。
type DistanceBuffer struct {
	db       *sqlx.DB
	mu       sync.Mutex
	states   map[string]*chairDist
	interval time.Duration
	// exec は flush の書込み処理。テストで差し替え可能にする。
	exec func(ctx context.Context, deltas []repository.DistanceDelta) error
	// listFunc は SyncFromDB の距離一覧取得。テストで差し替え可能にする。
	listFunc func(ctx context.Context) ([]repository.ChairDistance, error)
	// flushMu は ticker による周期 flush と MaybeRepair による
	// inline flush の直列化用。バッチの順序（＝updated_at の単調性）を保つ。
	flushMu sync.Mutex
	// lastOK は最後に flush が成功した時刻（unixnano）。
	// MaybeRepair の停滞検出用。
	lastOK atomic.Int64
	// repairing は inline flush の多重起動防止用。
	repairing atomic.Bool
	// lastTry は repair 試行の最終時刻（unixnano）。不調時の連打抑止用。
	lastTry atomic.Int64
}

// 診断カウンタ。total_distance 鮮度検証割れの切り分け用。
// いずれも atomic 加算のみでホットパスへの影響はnsオーダー。
var (
	coordPosts     atomic.Int64
	coordNoPrev    atomic.Int64
	flushRuns      atomic.Int64
	flushRowsTotal atomic.Int64
	repairCount    atomic.Int64
	maxGapNs       atomic.Int64
	maxExecNs      atomic.Int64
)

func noteCoordPost(hasPrev bool) {
	coordPosts.Add(1)
	if !hasPrev {
		coordNoPrev.Add(1)
	}
}

func noteFlush(rows int) {
	flushRuns.Add(1)
	flushRowsTotal.Add(int64(rows))
}

func noteGap(gap time.Duration) {
	for {
		cur := maxGapNs.Load()
		if int64(gap) <= cur {
			return
		}
		if maxGapNs.CompareAndSwap(cur, int64(gap)) {
			return
		}
	}
}

// repairThreshold は inline repair を発動する停滞閾値。
// 正常時の乖離は flush 周期（500ms）＋書込時間に収まるため、
// これを超えた停滞は ticker 側の異常とみなして POST パスで回収する。
// bench の鮮度検証（3秒）に対するマージンを残す。
const repairThreshold = time.Second

// repairRetryInterval は repair 試行の最小間隔。不調継続時の連打抑止用。
const repairRetryInterval = 200 * time.Millisecond

func NewDistanceBuffer(db *sqlx.DB) *DistanceBuffer {
	b := &DistanceBuffer{
		db:     db,
		states: make(map[string]*chairDist),
		interval: 500 * time.Millisecond,
	}
	b.exec = func(ctx context.Context, deltas []repository.DistanceDelta) error {
		return chairRepository.AddTotalDistances(ctx, b.db, deltas)
	}
	b.listFunc = func(ctx context.Context) ([]repository.ChairDistance, error) {
		return chairRepository.ListAllDistances(ctx, b.db)
	}
	b.lastOK.Store(time.Now().UnixNano())
	return b
}

// Add はPOST座標を追跡する。h はそのPOSTの recordedAt（応答に載せた時刻と同一）。
// 初回POSTは基準点の確立のみ（差分なし）。差分0でも maxH は進める。
func (b *DistanceBuffer) Add(chairID string, lat, lon int, h time.Time) {
	b.mu.Lock()
	st := b.states[chairID]
	if st == nil {
		st = &chairDist{}
		b.states[chairID] = st
	}
	if !st.hasBase {
		st.lastLat, st.lastLon = lat, lon
		st.hasBase = true
	} else {
		st.sumAll += calculateDistance(st.lastLat, st.lastLon, lat, lon)
		st.lastLat, st.lastLon = lat, lon
	}
	if h.After(st.maxAll) {
		st.maxAll = h
	}
	b.mu.Unlock()
}

// Lookup は提供用の合計と updated_at を返す。追跡もDB行も無ければ ok=false。
func (b *DistanceBuffer) Lookup(chairID string) (total int, updatedAt time.Time, ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.states[chairID]
	if st == nil || !st.hasData() {
		return 0, time.Time{}, false
	}
	uh := st.baseH
	if st.maxAll.After(uh) {
		uh = st.maxAll
	}
	return st.base + st.sumAll, ceilMicro(uh), true
}

// flushMark は失敗時の巻き戻し用。
type flushMark struct {
	st         *chairDist
	sumFlushed int
	maxFlushed time.Time
}

// Discard は追跡分を破棄する。postInitialize（DB全初期化）用。
// 直後の Reload＋SyncFromDB で再構築される。
func (b *DistanceBuffer) Discard() {
	b.mu.Lock()
	b.states = make(map[string]*chairDist)
	b.mu.Unlock()
}

// SyncFromDB はDBの距離行と ChairManager の位置で追跡状態を再構築する。
// 起動時・初期化時の Reload 直後に呼ぶこと。
func (b *DistanceBuffer) SyncFromDB(ctx context.Context) error {
	rows, err := b.listFunc(ctx)
	if err != nil {
		return err
	}
	locs := globalChairManager.SnapshotLocations()
	b.mu.Lock()
	defer b.mu.Unlock()
	states := make(map[string]*chairDist, len(rows)+len(locs))
	for _, r := range rows {
		states[r.ChairID] = &chairDist{
			base:       r.Total,
			baseH:      r.UpdatedAt,
			maxAll:     r.UpdatedAt,
			maxFlushed: r.UpdatedAt,
		}
	}
	for id, c := range locs {
		st := states[id]
		if st == nil {
			st = &chairDist{}
			states[id] = st
		}
		st.lastLat, st.lastLon = c[0], c[1]
		st.hasBase = true
	}
	b.states = states
	b.lastOK.Store(time.Now().UnixNano())
	return nil
}

func (b *DistanceBuffer) Start(ctx context.Context) {
	ticker := time.NewTicker(b.interval)
	defer ticker.Stop()
	ticks := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.Flush(context.Background())
			ticks++
			// 30秒ごとに診断カウンタを出す（1走行2〜3行）。
			if ticks%60 == 0 {
				maxGap := time.Duration(maxGapNs.Swap(0))
				maxExec := time.Duration(maxExecNs.Swap(0))
				slog.Info("distance buffer stats",
					"posts", coordPosts.Load(),
					"noPrev", coordNoPrev.Load(),
					"flushes", flushRuns.Load(),
					"rows", flushRowsTotal.Load(),
					"repairs", repairCount.Load(),
					"maxGap", maxGap.String(),
					"maxExec", maxExec.String(),
				)
			}
		}
	}
}

// ceilMicro は DATETIME(6) への丸めで切り捨てられないよう切上げる。
func ceilMicro(t time.Time) time.Time {
	tr := t.Truncate(time.Microsecond)
	if tr.Before(t) {
		return tr.Add(time.Microsecond)
	}
	return tr
}

// MaybeRepair は POST パスからの停滞検出・回収用。
// 最終成功から repairThreshold を超えていたら inline flush を1回だけ行う。
// 正常時は atomic load＋分岐のみ（nsオーダー）で何もしない。
func (b *DistanceBuffer) MaybeRepair(ctx context.Context, now time.Time) {
	last := time.Unix(0, b.lastOK.Load())
	gap := now.Sub(last)
	noteGap(gap)
	if gap < repairThreshold {
		return
	}
	try := time.Unix(0, b.lastTry.Load())
	if now.Sub(try) < repairRetryInterval {
		return
	}
	if !b.repairing.CompareAndSwap(false, true) {
		return
	}
	defer b.repairing.Store(false)
	b.lastTry.Store(now.UnixNano())
	repairCount.Add(1)
	slog.Info("distance buffer stall detected, inline flush", "stale", now.Sub(last).String())
	b.Flush(ctx)
}

// Flush は滞留分を1本の multi-row upsert で書込む。
// 失敗時は書込済みマークを戻して次tickで再試行する（欠損させない）。
func (b *DistanceBuffer) Flush(ctx context.Context) {
	b.flushMu.Lock()
	defer b.flushMu.Unlock()

	b.mu.Lock()
	deltas := make([]repository.DistanceDelta, 0)
	marks := make([]flushMark, 0)
	for id, st := range b.states {
		if st.sumAll == st.sumFlushed && !st.maxAll.After(st.maxFlushed) {
			continue
		}
		deltas = append(deltas, repository.DistanceDelta{
			ChairID:   id,
			Delta:     st.sumAll - st.sumFlushed,
			UpdatedAt: ceilMicro(st.maxAll),
		})
		marks = append(marks, flushMark{st: st, sumFlushed: st.sumFlushed, maxFlushed: st.maxFlushed})
		st.sumFlushed = st.sumAll
		st.maxFlushed = st.maxAll
	}
	b.mu.Unlock()
	if len(deltas) == 0 {
		// 書込むものが無くても生存確認として成功時刻を進める。
		b.lastOK.Store(time.Now().UnixNano())
		return
	}
	noteFlush(len(deltas))
	execStart := time.Now()
	err := b.exec(ctx, deltas)
	execDur := time.Since(execStart)
	for {
		cur := maxExecNs.Load()
		if int64(execDur) <= cur {
			break
		}
		if maxExecNs.CompareAndSwap(cur, int64(execDur)) {
			break
		}
	}
	if err != nil {
		slog.Error("failed to flush total distances", "error", err, "chairs", len(deltas))
		b.mu.Lock()
		for _, m := range marks {
			m.st.sumFlushed = m.sumFlushed
			m.st.maxFlushed = m.maxFlushed
		}
		b.mu.Unlock()
		return
	}
	b.lastOK.Store(time.Now().UnixNano())
}

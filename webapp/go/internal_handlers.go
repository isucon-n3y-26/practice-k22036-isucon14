package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/go-sql-driver/mysql"
)

var matchSignal = make(chan struct{}, 1)

// maxMatchingDistanceBase は割当対象とする迎車地点までの距離の基本上限。
// これを超える椅子は候補から外し、近傍の椅子が空くのを待つ。
// 実測のマッチ距離分布を見て調整すること（GET /api/internal/match-dist）。
const maxMatchingDistanceBase = 200

// maxMatchingDistanceRelaxPerSec は待ち時間1秒あたりの上限緩和量。
// 待てば上限が開くため、椅子が来ない地域のライドが永久に飢餓死しない。
// 例: 10秒待ちで +300。bench側タイムアウト（30秒）より十分前に全開放される。
const maxMatchingDistanceRelaxPerSec = 30

// matchDistBuckets は割当成立時の迎車距離の度数分布。
// 境界: [0]=50以下 [1]=100以下 [2]=200以下 [3]=400以下 [4]=800以下 [5]=超過。
var matchDistBuckets [6]atomic.Uint64
var matchDistTotal atomic.Uint64

// matchSkippedByDistance は割当できずキューに残留した延べ件数
// （空き椅子なし・距離上限による見送りの合計）。
var matchSkippedByDistance atomic.Uint64

func noteMatchDist(dist int) {
	matchDistTotal.Add(1)
	switch {
	case dist <= 50:
		matchDistBuckets[0].Add(1)
	case dist <= 100:
		matchDistBuckets[1].Add(1)
	case dist <= 200:
		matchDistBuckets[2].Add(1)
	case dist <= 400:
		matchDistBuckets[3].Add(1)
	case dist <= 800:
		matchDistBuckets[4].Add(1)
	default:
		matchDistBuckets[5].Add(1)
	}
}

// matchingMaxDist はライドの待ち時間に応じた実効上限を返す。
func matchingMaxDist(waited time.Duration) int {
	relax := int(waited / time.Second)
	if relax < 0 {
		relax = 0
	}
	return maxMatchingDistanceBase + relax*maxMatchingDistanceRelaxPerSec
}

// isRetryableDBError は deadlock (1213) / lock wait timeout (1205) を
// 検出し、トランザクション再試行の可否を返す。
func isRetryableDBError(err error) bool {
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		return mysqlErr.Number == 1213 || mysqlErr.Number == 1205
	}
	return false
}

// isDupEntryError は重複キーエラー (1062) かどうかを返す。
// ユーザー登録の username 衝突検出用。
func isDupEntryError(err error) bool {
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		return mysqlErr.Number == 1062
	}
	return false
}

func triggerMatching() {
	select {
	case matchSignal <- struct{}{}:
	default:
	}
}

func startMatcher(ctx context.Context) {
	slog.Info("matcher started")
	// ティッカーは取りこぼし時の安全網専用。定常のマッチングは
	// triggerMatching（配車要求・椅子有効化・評価完了）で即時起動するため、
	// ここは低頻度でよい。全表JOINの走査を10Hzで回さないための設定。
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-matchSignal:
			if _, _, err := doMatching(ctx); err != nil {
				slog.Error("error during matching (signal)", "error", err)
			}
		case <-ticker.C:
			slog.Debug("matcher ticker fired")
			if _, _, err := doMatching(ctx); err != nil {
				slog.Error("error during matching (ticker)", "error", err)
			}
		}
	}
}

func doMatching(ctx context.Context) (int, int, error) {
	start := time.Now()
	ridesCount := 0
	matchedCount := 0
	defer func() {
		if ridesCount > 0 || matchedCount > 0 {
			slog.Info("matching executed",
				"elapsed_ms", time.Since(start).Milliseconds(),
				"elapsed", time.Since(start).String(),
				"unassigned_rides", ridesCount,
				"matched", matchedCount,
			)
		}
	}()

	tx, err := db.Beginx()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	// 1. マッチング待ちキューのライド一覧を取得（古い順）
	// 全表JOIN走査の代わりにキューを見るため、待ち行列の規模にのみ比例する
	rides, err := matchingQueueRepository.ListPendingRides(ctx, tx)
	if err != nil {
		return 0, 0, err
	}
	ridesCount = len(rides)
	if len(rides) == 0 {
		return 0, 0, nil
	}

	var assignedChairIDs []string
	var assignedRideIDs []string
	now := time.Now()
	for _, ride := range rides {
		// 2. メモリ上から最適な空き椅子を探索。距離上限を超える場合は
		// 見送り、キューに残して近傍の椅子が空くのを待つ。上限は
		// 待ち時間に応じて緩むため、飢餓死はしない。
		maxDist := matchingMaxDist(now.Sub(ride.CreatedAt))
		matched, ok := globalChairManager.FindBestAvailableChair(ride.PickupLatitude, ride.PickupLongitude, ride.ID, maxDist)
		if !ok {
			// 空き椅子がない場合・上限内に候補がない場合はキューに残して後続のライドを試す
			matchSkippedByDistance.Add(1)
			continue
		}
		noteMatchDist(calculateDistance(ride.PickupLatitude, ride.PickupLongitude, matched.Latitude, matched.Longitude))
		assignedChairIDs = append(assignedChairIDs, matched.ID)
		assignedRideIDs = append(assignedRideIDs, ride.ID)

		// 3. ライドに椅子を割り当て、キューから取り除く
		if err := rideRepository.UpdateChairID(ctx, tx, ride.ID, matched.ID); err != nil {
			for _, cid := range assignedChairIDs {
				globalChairManager.UnassignRide(cid)
			}
			return ridesCount, matchedCount, err
		}
		if err := matchingQueueRepository.Dequeue(ctx, tx, ride.ID); err != nil {
			for _, cid := range assignedChairIDs {
				globalChairManager.UnassignRide(cid)
			}
			return ridesCount, matchedCount, err
		}
		matchedCount++
	}

	if matchedCount == 0 {
		return ridesCount, 0, nil
	}

	if err := tx.Commit(); err != nil {
		for _, cid := range assignedChairIDs {
			globalChairManager.UnassignRide(cid)
		}
		return ridesCount, matchedCount, err
	}

	// chair_id が変化したためライドキャッシュを破棄する。
	// 次回通知取得で最新行を読み直すため stale は起きない。
	for _, rid := range assignedRideIDs {
		rideRepository.InvalidateCache(rid)
	}

	// 割当により椅子向けSSEで可視になったMATCHINGを通知する
	// 未送信ログの配送先設定は wake より先に行う
	for i, cid := range assignedChairIDs {
		globalStatusLog.SetChair(assignedRideIDs[i], cid)
		WakeChair(cid)
	}

	return ridesCount, matchedCount, nil
}

// internalGetMatchDist は割当成立時の迎車距離の度数分布を返す。
// 上限値の調整用。benchからの呼出しはなく診断専用。
func internalGetMatchDist(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"total":   matchDistTotal.Load(),
		"skipped": matchSkippedByDistance.Load(),
		"le_50":   matchDistBuckets[0].Load(),
		"le_100":  matchDistBuckets[1].Load(),
		"le_200":  matchDistBuckets[2].Load(),
		"le_400":  matchDistBuckets[3].Load(),
		"le_800":  matchDistBuckets[4].Load(),
		"gt_800":  matchDistBuckets[5].Load(),
	})
}

// internalGetChairStates は ChairManager の全椅子状態をダンプする。
// nearby不足（CODE=31）発生時の切分け用。読取り専用でbenchに影響なし。
func internalGetChairStates(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UnixMilli()
	type chairStateView struct {
		ID         string `json:"id"`
		Active     bool   `json:"active"`
		HasLoc     bool   `json:"has_location"`
		RideID     string `json:"ride_id"`
		FreedAgoMs int64  `json:"freed_ago_ms"`
		Lat        int    `json:"lat"`
		Lon        int    `json:"lon"`
	}
	views := make([]chairStateView, 0, 2048)
	for _, p := range globalChairManager.snapshot() {
		st := p.Load()
		if st == nil {
			continue
		}
		views = append(views, chairStateView{
			ID:         st.ID,
			Active:     st.IsActive,
			HasLoc:     st.HasLocation,
			RideID:     st.CurrentRideID,
			FreedAgoMs: now - st.FreedAt,
			Lat:        st.Latitude,
			Lon:        st.Longitude,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"chairs": views})
}

// このAPIをインスタンス内から一定間隔で叩かせることで、椅子とライドをマッチングさせる
func internalGetMatching(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ridesCount, matchedCount, err := doMatching(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if ridesCount == 0 || matchedCount == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

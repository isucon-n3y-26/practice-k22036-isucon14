package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-sql-driver/mysql"
)

var matchSignal = make(chan struct{}, 1)

// isRetryableDBError は deadlock (1213) / lock wait timeout (1205) を
// 検出し、トランザクション再試行の可否を返す。
func isRetryableDBError(err error) bool {
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		return mysqlErr.Number == 1213 || mysqlErr.Number == 1205
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
	for _, ride := range rides {
		// 2. メモリ上から最適な空き椅子を探索
		matched, ok := globalChairManager.FindBestAvailableChair(ride.PickupLatitude, ride.PickupLongitude, ride.ID)
		if !ok {
			// 空き椅子がない場合はキューに残して後続のライドを試す
			continue
		}
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

	// 割当により椅子向けSSEで可視になったMATCHINGを通知する
	// 未送信ログの配送先設定は wake より先に行う
	for i, cid := range assignedChairIDs {
		globalStatusLog.SetChair(assignedRideIDs[i], cid)
		WakeChair(cid)
	}

	return ridesCount, matchedCount, nil
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

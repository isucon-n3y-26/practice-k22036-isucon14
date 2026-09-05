package main

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

var matchSignal = make(chan struct{}, 1)

func triggerMatching() {
	select {
	case matchSignal <- struct{}{}:
	default:
	}
}

func startMatcher(ctx context.Context) {
	slog.Info("matcher started")
	ticker := time.NewTicker(100 * time.Millisecond)
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

	// 1. MATCHING 状態で chair_id が未割当のライド一覧を取得（古い順）
	rides, err := rideRepository.GetUnassignedMatchingRides(ctx, tx)
	if err != nil {
		return 0, 0, err
	}
	ridesCount = len(rides)
	if len(rides) == 0 {
		return 0, 0, nil
	}

	var assignedChairIDs []string
	for _, ride := range rides {
		// 2. メモリ上から最適な空き椅子を探索
		matched, ok := globalChairManager.FindBestAvailableChair(ride.PickupLatitude, ride.PickupLongitude, ride.ID)
		if !ok {
			// 空き椅子がない場合はスキップして後続のライドを試す
			continue
		}
		assignedChairIDs = append(assignedChairIDs, matched.ID)

		// 3. ライドに椅子を割り当て
		if err := rideRepository.UpdateChairID(ctx, tx, ride.ID, matched.ID); err != nil {
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
	for _, cid := range assignedChairIDs {
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

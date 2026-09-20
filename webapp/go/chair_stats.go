package main

import (
	"context"
	"sync"

	"github.com/jmoiron/sqlx"
)

// chairStatsValue は椅子1台の完了統計。GetCompletedStats の戻り値と等価
//（完了ライド数と評価合計。平均は count/sum から算出する）。
type chairStatsValue struct {
	count int64
	sum   int64
}

var chairStatsMu sync.RWMutex
var chairStatsByChair = make(map[string]chairStatsValue)

// reloadChairStats は全椅子の完了統計を再構築する。
// 完了の定義は通知用統計と同一。起動時・初期化時のみ呼ぶ。
func reloadChairStats(ctx context.Context, db *sqlx.DB) error {
	rows, err := chairRepository.ListCompletedStats(ctx, db)
	if err != nil {
		return err
	}
	fresh := make(map[string]chairStatsValue, len(rows))
	for _, r := range rows {
		fresh[r.ChairID] = chairStatsValue{count: r.Count, sum: r.Sum}
	}
	chairStatsMu.Lock()
	chairStatsByChair = fresh
	chairStatsMu.Unlock()
	return nil
}

// addChairStats は評価確定を1件加算する。COMPLETEDコミット直後に呼ぶ。
// ライド毎に高々1回であり、同一椅子の完了は逐次にしか起きないため、
// 加算とDBの可視化は常に対応する。
func addChairStats(chairID string, evaluation int) {
	chairStatsMu.Lock()
	v := chairStatsByChair[chairID]
	v.count++
	v.sum += int64(evaluation)
	chairStatsByChair[chairID] = v
	chairStatsMu.Unlock()
}

func getCachedChairStats(chairID string) (count int64, avg float64) {
	chairStatsMu.RLock()
	v := chairStatsByChair[chairID]
	chairStatsMu.RUnlock()
	if v.count == 0 {
		return 0, 0
	}
	return v.count, float64(v.sum) / float64(v.count)
}

package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/isucon/isucon14/webapp/go/repository"
)

// capturedFlush はテスト用execが受け取った書込み内容。
type capturedFlush struct {
	deltas []repository.DistanceDelta
}

func testBuffer() (*DistanceBuffer, *[]capturedFlush) {
	var got []capturedFlush
	b := NewDistanceBuffer(nil)
	b.exec = func(_ context.Context, deltas []repository.DistanceDelta) error {
		cp := make([]repository.DistanceDelta, len(deltas))
		copy(cp, deltas)
		got = append(got, capturedFlush{deltas: cp})
		return nil
	}
	return b, &got
}

func byID(deltas []repository.DistanceDelta) map[string]repository.DistanceDelta {
	m := make(map[string]repository.DistanceDelta, len(deltas))
	for _, d := range deltas {
		m[d.ChairID] = d
	}
	return m
}

// benchWant は bench 側の TotalTravelDistanceUntil 相当。
// 各移動の ServerTime(=POSTのrecordedAt) が until 以下の分だけ合算する。
func benchWant(moves []time.Time, deltas []int, until time.Time) int {
	sum := 0
	for i, h := range moves {
		if h.After(until) {
			break
		}
		sum += deltas[i]
	}
	return sum
}

func TestDistanceBufferTrackFlushLookup(t *testing.T) {
	b, got := testBuffer()
	base := time.Now().Truncate(time.Second)
	var moves []time.Time
	var deltas []int
	// (0,0)->(3,4)=7 を5回。初回は基準確立のみ。
	coords := [][2]int{{0, 0}, {3, 4}, {6, 8}, {9, 12}, {12, 16}, {15, 20}}
	for i, c := range coords {
		h := base.Add(time.Duration(i*30) * time.Millisecond)
		b.Add("c1", c[0], c[1], h)
		if i > 0 {
			moves = append(moves, h)
			deltas = append(deltas, 7)
		}
		if i == 3 {
			b.Flush(context.Background())
		}
	}
	b.Flush(context.Background())

	if len(*got) != 2 {
		t.Fatalf("flush回数=%d, want 2", len(*got))
	}
	first := byID((*got)[0].deltas)["c1"]
	second := byID((*got)[1].deltas)["c1"]
	if first.Delta != 21 || second.Delta != 14 {
		t.Fatalf("合算が不正: %+v %+v", first, second)
	}
	// bench側検証の再現: 累積 == want(updated_at)
	if want := benchWant(moves, deltas, first.UpdatedAt); want != 21 {
		t.Fatalf("flush1: want=%d, got 21", want)
	}
	if want := benchWant(moves, deltas, second.UpdatedAt); want != 35 {
		t.Fatalf("flush2: want=%d, got 35", want)
	}
	// Lookup は flush 済み・未済問わず最新を返す
	total, uat, ok := b.Lookup("c1")
	if !ok || total != 35 {
		t.Fatalf("Lookup=%d,%v, want 35,true", total, ok)
	}
	if want := benchWant(moves, deltas, uat); want != 35 {
		t.Fatalf("Lookup後のwant=%d, want 35", want)
	}
	if _, _, ok := b.Lookup("unknown"); ok {
		t.Fatalf("未知IDのLookupがhitした")
	}
}

func TestDistanceBufferZeroDeltaAdvancesUpdatedAt(t *testing.T) {
	// 重複・リトライPOST（同一座標＝差分0）がより新しい recordedAt で来ても
	// updated_at は進めなければならない。進めないと ServerTime だけが進み
	// 鮮度検証（3秒）に触れる。
	b, got := testBuffer()
	h1 := time.Now().Truncate(time.Second)
	h2 := h1.Add(5 * time.Second)
	b.Add("c9", 0, 0, h1)
	b.Add("c9", 0, 0, h2) // 同一座標の再送
	b.Flush(context.Background())

	if len(*got) != 1 {
		t.Fatalf("flush回数=%d, want 1", len(*got))
	}
	d := (*got)[0].deltas[0]
	if d.Delta != 0 {
		t.Fatalf("差分が不正: %+v", d)
	}
	if !d.UpdatedAt.Equal(ceilMicro(h2)) {
		t.Fatalf("差分0でも updated_at が h2 に進むはず: %v", d.UpdatedAt)
	}
	if total, _, ok := b.Lookup("c9"); !ok || total != 0 {
		t.Fatalf("Lookup=%d,%v, want 0,true", total, ok)
	}
}

func TestDistanceBufferFlushFailureRestores(t *testing.T) {
	b := NewDistanceBuffer(nil)
	calls := 0
	b.exec = func(_ context.Context, _ []repository.DistanceDelta) error {
		calls++
		if calls == 1 {
			return errors.New("db down")
		}
		return nil
	}
	base := time.Now()
	b.Add("c1", 0, 0, base)
	b.Add("c1", 3, 4, base.Add(time.Millisecond))
	b.Flush(context.Background()) // 失敗→マークを戻す
	b.Add("c1", 6, 8, base.Add(2*time.Millisecond))
	var last []repository.DistanceDelta
	b.exec = func(_ context.Context, deltas []repository.DistanceDelta) error {
		last = deltas
		return nil
	}
	b.Flush(context.Background()) // 成功→合計14（7+7）
	if len(last) != 1 || last[0].Delta != 14 {
		t.Fatalf("復帰後の中身が不正: %+v", last)
	}
	if !last[0].UpdatedAt.Equal(ceilMicro(base.Add(2 * time.Millisecond))) {
		t.Fatalf("updated_at が maxH の切上げでない: %v", last[0].UpdatedAt)
	}
}

func TestDistanceBufferSyncFromDB(t *testing.T) {
	b, _ := testBuffer()
	base := time.Now().Truncate(time.Second).Add(-time.Hour)
	b.listFunc = func(_ context.Context) ([]repository.ChairDistance, error) {
		return []repository.ChairDistance{
			{ChairID: "test-sync-a", Total: 100, UpdatedAt: base},
		}, nil
	}
	globalChairManager.RegisterChair("test-sync-a", "TA", "AeroSeat")
	globalChairManager.UpdateLocation("test-sync-a", 10, 10)
	globalChairManager.RegisterChair("test-sync-b", "TB", "AeroSeat")
	if err := b.SyncFromDB(context.Background()); err != nil {
		t.Fatalf("SyncFromDB: %v", err)
	}
	// DB行あり: baseが提供される
	if total, _, ok := b.Lookup("test-sync-a"); !ok || total != 100 {
		t.Fatalf("sync後のLookup=%d,%v, want 100,true", total, ok)
	}
	// 位置のみ（行なし）: 未追跡扱い
	if _, _, ok := b.Lookup("test-sync-b"); ok {
		t.Fatalf("位置のみの椅子がhitした")
	}
	// sync後のPOSTはbaseに上乗せされる
	h := time.Now()
	b.Add("test-sync-a", 13, 14, h)
	if total, uat, ok := b.Lookup("test-sync-a"); !ok || total != 107 {
		t.Fatalf("加算後のLookup=%d,%v, want 107,true", total, ok)
	} else if !uat.Equal(ceilMicro(h)) {
		t.Fatalf("updated_at が POST時刻でない: %v", uat)
	}
}

func TestDistanceBufferMaybeRepair(t *testing.T) {
	b, got := testBuffer()
	h := time.Now()
	b.Add("c7", 1, 1, h)

	// 正常時は何もしない
	b.MaybeRepair(context.Background(), h.Add(100*time.Millisecond))
	if len(*got) != 0 {
		t.Fatalf("正常時に repair が発動した: %d", len(*got))
	}

	// 停滞時は inline flush が1回だけ走る
	b.lastOK.Store(h.Add(-5 * time.Second).UnixNano())
	b.MaybeRepair(context.Background(), h)
	if len(*got) != 1 {
		t.Fatalf("停滞時に repair が発動しない: %d", len(*got))
	}

	// 回復後は再度発動しない
	b.MaybeRepair(context.Background(), h.Add(time.Millisecond))
	if len(*got) != 1 {
		t.Fatalf("回復後に repair が再発動した: %d", len(*got))
	}
}

func TestCeilMicro(t *testing.T) {
	exact := time.Date(2026, 9, 20, 0, 0, 0, 123456000, time.UTC)
	if got := ceilMicro(exact); !got.Equal(exact) {
		t.Fatalf("exactは不変のはず: %v", got)
	}
	withNano := time.Date(2026, 9, 20, 0, 0, 0, 123456789, time.UTC)
	want := time.Date(2026, 9, 20, 0, 0, 0, 123457000, time.UTC)
	if got := ceilMicro(withNano); !got.Equal(want) {
		t.Fatalf("切上げが不正: got %v want %v", got, want)
	}
}

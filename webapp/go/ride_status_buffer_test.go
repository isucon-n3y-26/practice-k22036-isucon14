package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/isucon/isucon14/webapp/go/models"
)

func TestRideStatusBufferFlush(t *testing.T) {
	b := NewRideStatusBuffer(nil)
	var got [][]models.RideStatus
	b.exec = func(_ context.Context, rows []models.RideStatus) error {
		cp := make([]models.RideStatus, len(rows))
		copy(cp, rows)
		got = append(got, cp)
		return nil
	}

	h := time.Now()
	b.Append(models.RideStatus{ID: "s1", RideID: "r1", Status: "MATCHING", CreatedAt: h})
	b.Append(models.RideStatus{ID: "s2", RideID: "r1", Status: "ENROUTE", CreatedAt: h.Add(time.Millisecond)})
	b.Flush(context.Background())

	if len(got) != 1 || len(got[0]) != 2 {
		t.Fatalf("flush内容が不正: %+v", got)
	}
	if got[0][0].ID != "s1" || got[0][1].ID != "s2" {
		t.Fatalf("順序が保持されていない: %+v", got[0])
	}
	if !got[0][1].CreatedAt.Equal(h.Add(time.Millisecond)) {
		t.Fatalf("created_at が引き継がれていない: %+v", got[0][1])
	}

	// 空flushは何もしない
	b.Flush(context.Background())
	if len(got) != 1 {
		t.Fatalf("空flushで書込みが発生した")
	}
}

func TestRideStatusBufferFlushFailureKeepsOrder(t *testing.T) {
	b := NewRideStatusBuffer(nil)
	calls := 0
	b.exec = func(_ context.Context, _ []models.RideStatus) error {
		calls++
		if calls == 1 {
			return errors.New("db down")
		}
		return nil
	}

	h := time.Now()
	b.Append(models.RideStatus{ID: "s1", RideID: "r1", Status: "MATCHING", CreatedAt: h})
	b.Flush(context.Background()) // 失敗→先頭に戻る
	b.Append(models.RideStatus{ID: "s2", RideID: "r1", Status: "ENROUTE", CreatedAt: h})
	var last []models.RideStatus
	b.exec = func(_ context.Context, rows []models.RideStatus) error {
		last = rows
		return nil
	}
	b.Flush(context.Background()) // 成功→s1,s2の順で書込む
	if len(last) != 2 || last[0].ID != "s1" || last[1].ID != "s2" {
		t.Fatalf("復帰後の順序・内容が不正: %+v", last)
	}
}

package main

import (
	"context"
	"errors"
	"testing"
)

func TestSentMarkBufferFlush(t *testing.T) {
	b := NewSentMarkBuffer(nil)
	var appGot, chairGot [][]string
	b.execApp = func(_ context.Context, ids []string) error {
		appGot = append(appGot, append([]string(nil), ids...))
		return nil
	}
	b.execChair = func(_ context.Context, ids []string) error {
		chairGot = append(chairGot, append([]string(nil), ids...))
		return nil
	}

	b.MarkApp("s3")
	b.MarkApp("s1")
	b.MarkApp("s1") // 重複は1行に束ねる
	b.MarkChair("s2")
	b.Flush(context.Background())

	if len(appGot) != 1 || len(chairGot) != 1 {
		t.Fatalf("flush回数が不正: app=%d chair=%d", len(appGot), len(chairGot))
	}
	if got := appGot[0]; len(got) != 2 {
		t.Fatalf("重複排除されていない: %v", got)
	} else {
		seen := map[string]bool{}
		for _, id := range got {
			seen[id] = true
		}
		if !seen["s1"] || !seen["s3"] {
			t.Fatalf("内容が不正: %v", got)
		}
	}
	if len(chairGot[0]) != 1 || chairGot[0][0] != "s2" {
		t.Fatalf("chair側が不正: %v", chairGot[0])
	}

	// 空flushは何もしない
	b.Flush(context.Background())
	if len(appGot) != 1 || len(chairGot) != 1 {
		t.Fatalf("空flushで書込みが発生した")
	}
}

func TestSentMarkBufferFlushFailureRestores(t *testing.T) {
	b := NewSentMarkBuffer(nil)
	calls := 0
	b.execApp = func(_ context.Context, _ []string) error {
		calls++
		if calls == 1 {
			return errors.New("db down")
		}
		return nil
	}
	var chairGot [][]string
	b.execChair = func(_ context.Context, ids []string) error {
		chairGot = append(chairGot, ids)
		return nil
	}

	b.MarkApp("s1")
	b.MarkChair("s2")
	b.Flush(context.Background()) // app失敗→両方戻る
	if calls != 1 || len(chairGot) != 0 {
		t.Fatalf("失敗時の振る舞いが不正: calls=%d chair=%d", calls, len(chairGot))
	}
	b.Flush(context.Background()) // 成功
	if len(chairGot) != 1 {
		t.Fatalf("復帰後にchair側が書込まれない")
	}
}

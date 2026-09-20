package main

import (
	"testing"
)

func sampleItems() []getAppRidesResponseItem {
	return []getAppRidesResponseItem{
		{ID: "r1", Fare: 600, Evaluation: 5},
		{ID: "r2", Fare: 700, Evaluation: 4},
	}
}

func TestUserHistoryCacheHitMiss(t *testing.T) {
	c := NewUserHistoryCache()
	if _, ok := c.Get("u1"); ok {
		t.Fatalf("空のはずがhitした")
	}
	c.Set("u1", sampleItems())
	items, ok := c.Get("u1")
	if !ok || len(items) != 2 || items[0].ID != "r1" || items[1].Fare != 700 {
		t.Fatalf("内容が不正: %+v", items)
	}
	// 空履歴も載る（新規ユーザーの初回参照対策）
	c.Set("u2", []getAppRidesResponseItem{})
	if items, ok := c.Get("u2"); !ok || len(items) != 0 {
		t.Fatalf("空履歴がhitしない: %+v,%v", items, ok)
	}
}

func TestUserHistoryCacheInvalidate(t *testing.T) {
	c := NewUserHistoryCache()
	c.Set("u1", sampleItems())
	c.Invalidate("u1")
	if _, ok := c.Get("u1"); ok {
		t.Fatalf("無効化後もhitする")
	}
	// 存在しないIDの無効化は無害
	c.Invalidate("nobody")
	// 他ユーザーに影響しない
	c.Set("u1", sampleItems())
	c.Set("u2", sampleItems())
	c.Invalidate("u1")
	if _, ok := c.Get("u2"); !ok {
		t.Fatalf("他ユーザーが消えた")
	}
}

func TestUserHistoryCacheClear(t *testing.T) {
	c := NewUserHistoryCache()
	c.Set("u1", sampleItems())
	c.Clear()
	if _, ok := c.Get("u1"); ok {
		t.Fatalf("Clear後もhitする")
	}
}

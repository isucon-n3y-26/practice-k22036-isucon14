package main

import (
	"testing"
	"time"
)

// TestCompleteRideGrace は完了直後の椅子が50msだけnearbyから隠れ、
// その後載ることを保証する。猶予は評価応答のbench側反映（Evaluated）
// 待ちで、「ライド中」検証（CODE=30）を回避するための最小限。
// 長すぎると不足警告（CODE=31）の窓になるため50msに留める。
func TestCompleteRideGrace(t *testing.T) {
	cm := &ChairManager{modelSpeeds: map[string]int{"A": 2}}
	cm.RegisterChair("c1", "C1", "A")
	cm.SetActivity("c1", true)
	cm.UpdateLocation("c1", 10, 10)

	if got := cm.GetNearbyChairs(10, 10, 50); len(got) != 1 {
		t.Fatalf("割当前に載らない: %v", len(got))
	}
	cm.AssignRide("c1", "r1")
	if got := cm.GetNearbyChairs(10, 10, 50); len(got) != 0 {
		t.Fatalf("割当中に載ってはいけない: %v", len(got))
	}
	cm.CompleteRide("c1")
	if got := cm.GetNearbyChairs(10, 10, 50); len(got) != 0 {
		t.Fatalf("完了直後は猶予で隠れるはず: %v", len(got))
	}
	time.Sleep(60 * time.Millisecond)
	if got := cm.GetNearbyChairs(10, 10, 50); len(got) != 1 {
		t.Fatalf("猶予後に載らない: %v", len(got))
	}
}

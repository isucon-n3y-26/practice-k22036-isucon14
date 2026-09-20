package main

import (
	"testing"
	"time"
)

func testLog() *StatusLog {
	l := NewStatusLog()
	base := time.Now()
	// ride1: user1/chair1、MATCHING→ENROUTE（chair1に2件未送信）
	l.Append("s1", "ride1", "MATCHING", "user1", "", base.Add(time.Second))
	l.SetChair("ride1", "chair1")
	l.Append("s2", "ride1", "ENROUTE", "user1", "chair1", base.Add(2*time.Second))
	// ride2: user1/chair2、COMPLETED（優先度0）
	l.Append("s3", "ride2", "COMPLETED", "user1", "chair2", base.Add(time.Second))
	// ride3: user2/chair1、PICKUP
	l.Append("s4", "ride3", "PICKUP", "user2", "chair1", base.Add(3*time.Second))
	return l
}

func TestStatusLogChairIndexed(t *testing.T) {
	l := testLog()
	got := l.ListUnsentForChair("chair1", 20)
	if len(got) != 3 {
		t.Fatalf("chair1の未送信=3のはずが %d", len(got))
	}
	// ride1の2件（時系列順）→ride3の順。COMPLETED優先は他椅子のため無関係
	if got[0].ID != "s1" || got[1].ID != "s2" || got[2].ID != "s4" {
		t.Fatalf("順序が等価でない: %v", got)
	}

	// 存在しない椅子は空
	if out := l.ListUnsentForChair("nochair", 20); len(out) != 0 {
		t.Fatalf("未知椅子で空でない: %v", out)
	}

	// limit
	if out := l.ListUnsentForChair("chair1", 2); len(out) != 2 {
		t.Fatalf("limitが効かない: %v", out)
	}

	// COMPLETED優先: chair2はCOMPLETEDのみ
	got2 := l.ListUnsentForChair("chair2", 20)
	if len(got2) != 1 || got2[0].ID != "s3" {
		t.Fatalf("chair2の結果が不正: %v", got2)
	}
}

func TestStatusLogUserIndexed(t *testing.T) {
	l := testLog()
	got := l.ListUnsentForUser("user1", 20)
	if len(got) != 3 {
		t.Fatalf("user1の未送信=3のはずが %d", len(got))
	}
	// 全体を作成時刻で安定ソート（s1とs3は同刻→rideID順でs1先）。
	// 旧全走査版と同一ロジックのため等価。
	if got[0].ID != "s1" || got[1].ID != "s3" || got[2].ID != "s2" {
		t.Fatalf("順序が等価でない: %v", got)
	}
	if out := l.ListUnsentForUser("nouser", 20); len(out) != 0 {
		t.Fatalf("未知ユーザーで空でない: %v", out)
	}
}

func TestStatusLogCleanupUnlinks(t *testing.T) {
	l := testLog()
	// s1を両送達→ride1にはs2が残るため索引維持
	l.MarkChairSent("s1")
	l.MarkAppSent("s1")
	if out := l.ListUnsentForChair("chair1", 20); len(out) != 2 {
		t.Fatalf("s1消去後のchair1=%d", len(out))
	}
	// ride2のs3を両送達→ride2消滅、chair2/user1から外れる
	l.MarkChairSent("s3")
	l.MarkAppSent("s3")
	if out := l.ListUnsentForChair("chair2", 20); len(out) != 0 {
		t.Fatalf("ride2消滅後にchair2が残る: %v", out)
	}
	got := l.ListUnsentForUser("user1", 20)
	for _, rs := range got {
		if rs.RideID == "ride2" {
			t.Fatalf("ride2消滅後にuser1に残る: %v", got)
		}
	}
}

func TestStatusLogSetChairMove(t *testing.T) {
	l := NewStatusLog()
	base := time.Now()
	l.Append("s1", "ride1", "MATCHING", "user1", "", base)
	if out := l.ListUnsentForChair("chair9", 20); len(out) != 0 {
		t.Fatalf("割当前に椅子可視ではいけない")
	}
	l.SetChair("ride1", "chair9")
	got := l.ListUnsentForChair("chair9", 20)
	if len(got) != 1 || got[0].ID != "s1" {
		t.Fatalf("割当後に椅子可視でない: %v", got)
	}
	// ユーザー側は割当前から可視
	if out := l.ListUnsentForUser("user1", 20); len(out) != 1 {
		t.Fatalf("ユーザー可視でない: %v", out)
	}
}

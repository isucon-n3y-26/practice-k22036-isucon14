package repository

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

type recordQueryer struct {
	queries []string
	args    [][]any
}

func (f *recordQueryer) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	f.queries = append(f.queries, query)
	f.args = append(f.args, args)
	return nil, nil
}

func TestMarkSentBulkSQL(t *testing.T) {
	r := &RideStatusRepository{}
	f := &recordQueryer{}
	if err := r.MarkAppSentBulk(context.Background(), f, []string{"s3", "s1", "s2"}); err != nil {
		t.Fatalf("MarkAppSentBulk: %v", err)
	}
	if len(f.queries) != 1 {
		t.Fatalf("queries=%d, want 1", len(f.queries))
	}
	q := f.queries[0]
	if !strings.HasPrefix(q, "UPDATE ride_statuses SET app_sent_at = ? WHERE id IN (?,?,?)") {
		t.Fatalf("SQL形状が不正: %s", q)
	}
	if !strings.HasSuffix(q, "AND app_sent_at IS NULL") {
		t.Fatalf("NULLガードが無い: %s", q)
	}
	// 引数は [時刻, ソート済みID...]
	if len(f.args[0]) != 4 {
		t.Fatalf("引数数が不正: %d", len(f.args[0]))
	}
	var got []string
	for _, a := range f.args[0][1:] {
		got = append(got, a.(string))
	}
	if len(got) != 3 || got[0] != "s1" || got[1] != "s2" || got[2] != "s3" {
		t.Fatalf("引数がソートされていない: %v", got)
	}

	f2 := &recordQueryer{}
	if err := r.MarkChairSentBulk(context.Background(), f2, []string{"c1"}); err != nil {
		t.Fatalf("MarkChairSentBulk: %v", err)
	}
	if !strings.Contains(f2.queries[0], "chair_sent_at") {
		t.Fatalf("chair側の列が不正: %s", f2.queries[0])
	}

	// 空はno-op
	f3 := &recordQueryer{}
	if err := r.MarkAppSentBulk(context.Background(), f3, nil); err != nil {
		t.Fatalf("empty: %v", err)
	}
	if len(f3.queries) != 0 {
		t.Fatalf("空で書込みが発生した")
	}

	// 不正カラムは拒否
	if err := markSentBulk(context.Background(), f3, "id", []string{"x"}); err == nil {
		t.Fatalf("不正カラムが通った")
	}
}

package main

import (
	"errors"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

func TestIsDupEntryError(t *testing.T) {
	if !isDupEntryError(&mysql.MySQLError{Number: 1062}) {
		t.Fatalf("1062を重複と判定しない")
	}
	if isDupEntryError(&mysql.MySQLError{Number: 1213}) {
		t.Fatalf("1213を重複と誤判定した")
	}
	if isDupEntryError(errors.New("boom")) {
		t.Fatalf("一般エラーを重複と誤判定した")
	}
	if isDupEntryError(nil) {
		t.Fatalf("nilを重複と誤判定した")
	}
}

func TestMatchingMaxDist(t *testing.T) {
	if got := matchingMaxDist(0); got != maxMatchingDistanceBase {
		t.Fatalf("待ち0の上限=%d, want %d", got, maxMatchingDistanceBase)
	}
	if got := matchingMaxDist(10 * time.Second); got != maxMatchingDistanceBase+10*maxMatchingDistanceRelaxPerSec {
		t.Fatalf("待ち10秒の上限=%d", got)
	}
	prev := matchingMaxDist(0)
	for _, waited := range []time.Duration{time.Second, 5 * time.Second, 30 * time.Second, time.Minute} {
		got := matchingMaxDist(waited)
		if got < prev {
			t.Fatalf("上限が単調でない: %vで%d < %d", waited, got, prev)
		}
		prev = got
	}
	if got := matchingMaxDist(-time.Second); got < 0 {
		t.Fatalf("上限が負になった: %d", got)
	}
}

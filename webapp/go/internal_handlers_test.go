package main

import (
	"errors"
	"testing"

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

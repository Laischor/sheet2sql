package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHistory_SaveListAndLatest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := openHistory(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	first := historyRecord{
		FilePath:     "/tmp/one.csv",
		TableName:    "tmp_one",
		SheetName:    "Sheet1",
		StartAtRow:   2,
		CSVSeparator: ",",
		Columns:      []savedColumn{{Name: "id", DataType: "integer", Selected: true}},
	}
	second := first
	second.FilePath = "/tmp/two.xlsx"
	second.TableName = "tmp_two"
	second.SheetName = "Sales"
	second.StartAtRow = 5

	if err := store.save(first); err != nil {
		t.Fatal(err)
	}
	if err := store.save(second); err != nil {
		t.Fatal(err)
	}

	list, err := store.list(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 records, got %d", len(list))
	}
	if list[0].TableName != "tmp_two" || list[1].TableName != "tmp_one" {
		t.Fatalf("expected newest first, got %#v", list)
	}

	latest, ok, err := store.latestForPath("/tmp/one.csv")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected a config for /tmp/one.csv")
	}
	if latest.CSVSeparator != "," || latest.StartAtRow != 2 || latest.Columns[0].Name != "id" {
		t.Fatalf("latest restore payload: %#v", latest)
	}

	if _, ok, err := store.latestForPath("/missing.csv"); err != nil || ok {
		t.Fatalf("missing path should be empty, ok=%v err=%v", ok, err)
	}
}

func TestOpenHistoryRecord_ReloadsFileAndConfig(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "people.csv")
	if err := os.WriteFile(csvPath, []byte("name;city\nAda;Berlin\nBob;Paris\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := initialModel(initOpts{sep: ';'})
	got := m.openHistoryRecord(historyRecord{
		FilePath:     csvPath,
		TableName:    "people",
		SheetName:    "Sheet1",
		StartAtRow:   3,
		CSVSeparator: ";",
		Columns: []savedColumn{
			{Name: "full_name", DataType: "text", Selected: true},
			{Name: "town", DataType: "text", Selected: false},
		},
	})

	if got.historyMode {
		t.Fatal("should leave history view after a successful load")
	}
	if got.table != "people" || got.filePath != csvPath {
		t.Fatalf("file/table: path=%q table=%q", got.filePath, got.table)
	}
	if got.sheets[0].columns[0].name != "full_name" || got.sheets[0].startAtRow != 3 {
		t.Fatalf("restored sheet: %#v", got.sheets[0])
	}

	sql := generateSQL(got)
	if strings.Contains(sql, "Ada") || !strings.Contains(sql, "Bob") {
		t.Fatalf("start row should skip Ada, got:\n%s", sql)
	}
}

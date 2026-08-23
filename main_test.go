package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/xuri/excelize/v2"
)

func testModel(columns []column, data [][]string, selected ...int) model {
	sel := make(map[int]struct{})
	for _, i := range selected {
		sel[i] = struct{}{}
	}

	return model{
		table: "tmp_test",
		sheets: []sheet{{
			name:       "Sheet1",
			columns:    columns,
			data:       data,
			selected:   sel,
			startAtRow: 2,
		}},
		csvSeparator: ';',
	}
}

func TestGenerateSQL_UsesTmpSchemaOnInsert(t *testing.T) {
	sql := generateSQL(testModel(
		[]column{{"name", "text"}},
		[][]string{{"name"}, {"Ada"}},
		0,
	))

	if !strings.Contains(sql, "INSERT INTO tmp.tmp_test") {
		t.Fatalf("insert must use tmp schema, got:\n%s", sql)
	}
}

func TestGenerateSQL_EscapesQuotes(t *testing.T) {
	sql := generateSQL(testModel(
		[]column{{"name", "text"}},
		[][]string{{"name"}, {"O'Brien"}},
		0,
	))

	if !strings.Contains(sql, "('O''Brien')") {
		t.Fatalf("single quotes must be escaped, got:\n%s", sql)
	}
}

func TestGenerateSQL_ShortRowDoesNotPanic(t *testing.T) {
	sql := generateSQL(testModel(
		[]column{{"a", "text"}, {"b", "text"}},
		[][]string{{"a", "b"}, {"onlyone"}},
		0, 1,
	))

	if !strings.Contains(sql, "('onlyone','')") {
		t.Fatalf("short rows should become empty strings, got:\n%s", sql)
	}
}

func TestGenerateSQL_NoSelectionHasNoInsert(t *testing.T) {
	sql := generateSQL(testModel(
		[]column{{"a", "text"}},
		[][]string{{"a"}, {"1"}},
	))

	if strings.Contains(sql, "INSERT") {
		t.Fatalf("no selected columns should not insert, got:\n%s", sql)
	}
}

func TestGenerateSQL_HeaderOnlyHasNoInsert(t *testing.T) {
	sql := generateSQL(testModel(
		[]column{{"a", "text"}},
		[][]string{{"a"}},
		0,
	))

	if strings.Contains(sql, "INSERT") {
		t.Fatalf("header-only data should not insert, got:\n%s", sql)
	}
	if !strings.Contains(sql, "CREATE TABLE") {
		t.Fatalf("expected create table, got:\n%s", sql)
	}
}

func TestGenerateSQL_EmptyModel(t *testing.T) {
	if generateSQL(model{}) != "" {
		t.Fatal("empty model should generate nothing")
	}
}

func TestGenerateSQL_StartAtRow(t *testing.T) {
	m := testModel(
		[]column{{"name", "text"}},
		[][]string{{"name"}, {"skip"}, {"keep"}},
		0,
	)
	m.sheets[0].startAtRow = 3

	sql := generateSQL(m)
	if strings.Contains(sql, "skip") {
		t.Fatalf("row before start should be skipped, got:\n%s", sql)
	}
	if !strings.Contains(sql, "('keep')") {
		t.Fatalf("expected start-at-row data, got:\n%s", sql)
	}
}

func TestReadFile_EmptyCSV(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.csv")
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := readFile(path, ';'); err == nil {
		t.Fatal("expected error for empty csv")
	}
}

func TestReadFile_MissingFileArgType(t *testing.T) {
	if _, err := readFile("notes.txt", ';'); err == nil {
		t.Fatal("expected unsupported file type error")
	}
}

func TestReadCSV_CustomSeparator(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.csv")
	if err := os.WriteFile(path, []byte("a,b\n1,2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sheets, err := readFile(path, ',')
	if err != nil {
		t.Fatal(err)
	}
	if len(sheets) != 1 || len(sheets[0].columns) != 2 {
		t.Fatalf("expected 2 columns with comma sep, got %#v", sheets[0].columns)
	}

	oneCol, err := readFile(path, ';')
	if err != nil {
		t.Fatal(err)
	}
	if len(oneCol[0].columns) != 1 {
		t.Fatalf("semicolon should keep a single column, got %#v", oneCol[0].columns)
	}
}

func TestReadXLSX_PreservesSheetOrder(t *testing.T) {
	f := excelize.NewFile()
	t.Cleanup(func() { _ = f.Close() })

	if err := f.SetSheetName("Sheet1", "Zed"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.NewSheet("Alpha"); err != nil {
		t.Fatal(err)
	}
	if err := f.SetCellValue("Zed", "A1", "h1"); err != nil {
		t.Fatal(err)
	}
	if err := f.SetCellValue("Alpha", "A1", "h2"); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "sheets.xlsx")
	if err := f.SaveAs(path); err != nil {
		t.Fatal(err)
	}

	sheets, err := readFile(path, ';')
	if err != nil {
		t.Fatal(err)
	}
	if len(sheets) != 2 {
		t.Fatalf("expected 2 sheets, got %d", len(sheets))
	}
	if sheets[0].name != "Zed" || sheets[1].name != "Alpha" {
		t.Fatalf("sheet order should follow workbook, got %q then %q", sheets[0].name, sheets[1].name)
	}
}

func TestAppendRenameInput_IgnoresArrows(t *testing.T) {
	got := appendRenameInput("col", tea.KeyMsg{Type: tea.KeyUp})
	if got != "col" {
		t.Fatalf("arrow key should be ignored, got %q", got)
	}

	got = appendRenameInput("col", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if got != "colx" {
		t.Fatalf("runes should append, got %q", got)
	}
}

func TestRenamePrompt_UsesTargetLabel(t *testing.T) {
	m := testModel([]column{{"city", "text"}}, [][]string{{"city"}}, 0)
	m.renameMode = true
	m.renameTo = "places"

	m.renameType = renameTable
	if prompt := m.renamePrompt(m.sheets[0]); !strings.Contains(prompt, "Rename table to:") {
		t.Fatalf("table rename prompt: %q", prompt)
	}

	m.renameType = startAtRow
	m.renameTo = "3"
	if prompt := m.renamePrompt(m.sheets[0]); prompt != "Start at row: 3" {
		t.Fatalf("start-at-row prompt: %q", prompt)
	}
}

func TestApplyConfig_RestoresSettings(t *testing.T) {
	m := testModel(
		[]column{{"old", "text"}, {"skip", "text"}},
		[][]string{{"old", "skip"}, {"a", "b"}},
	)

	applyConfig(&m, historyRecord{
		TableName:  "restored",
		SheetName:  "Sheet1",
		StartAtRow: 4,
		Columns: []savedColumn{
			{Name: "id", DataType: "integer", Selected: true},
			{Name: "note", DataType: "text", Selected: false},
		},
	}, false)

	if m.table != "restored" {
		t.Fatalf("table: %q", m.table)
	}
	if m.sheets[0].startAtRow != 4 {
		t.Fatalf("startAtRow: %d", m.sheets[0].startAtRow)
	}
	if m.sheets[0].columns[0] != (column{"id", "integer"}) {
		t.Fatalf("column 0: %#v", m.sheets[0].columns[0])
	}
	if _, ok := m.sheets[0].selected[0]; !ok {
		t.Fatal("column 0 should be selected")
	}
	if _, ok := m.sheets[0].selected[1]; ok {
		t.Fatal("column 1 should not be selected")
	}
}

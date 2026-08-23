package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/xuri/excelize/v2"
	"golang.design/x/clipboard"
)

type column struct {
	name, data_type string
}

type renameType int

const (
	renameTable renameType = iota
	renameColumn
	renameDataType
	startAtRow
	endAtRow
	changeSeparator
)

type sheet struct {
	name       string
	columns    []column
	data       [][]string
	selected   map[int]struct{}
	cursor     int
	startAtRow int
}

type model struct {
	table             string
	firstColumnHeader bool
	sheets            []sheet
	activeSheet       int
	renameType        renameType
	renameMode        bool
	renameTo          string
	clipboardReady    bool
	status            string
	filePath          string
	csvSeparator      rune
	history           *historyStore
	historyMode       bool
	historyItems      []historyRecord
	historyCursor     int
}

type initOpts struct {
	sheets         []sheet
	filePath       string
	sep            rune
	history        *historyStore
	clipboardReady bool
}

func initialModel(opts initOpts) model {
	sep := opts.sep
	if sep == 0 {
		sep = ';'
	}

	m := model{
		table:          "tmp_" + time.Now().Format("20060102150405"),
		sheets:         opts.sheets,
		clipboardReady: opts.clipboardReady,
		filePath:       opts.filePath,
		csvSeparator:   sep,
		history:        opts.history,
	}

	if len(m.sheets) == 0 {
		m.openHistoryView()
	}

	return m
}

func parseSeparator(s string) rune {
	switch strings.TrimSpace(s) {
	case "", ";":
		return ';'
	case "\t", `\t`, "tab":
		return '\t'
	}

	r, _ := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError {
		return ';'
	}
	return r
}

func separatorString(r rune) string {
	switch r {
	case 0:
		return ";"
	case '\t':
		return `\t`
	default:
		return string(r)
	}
}

func parseColumns(header []string) []column {
	replace := []string{" ", "-", ":", "/", "(", ")"}
	var columns []column

	for _, col := range header {
		name := strings.ToLower(strings.TrimSpace(col))
		for _, r := range replace {
			name = strings.ReplaceAll(name, r, "_")
		}
		columns = append(columns, column{name, "text"})
	}

	return columns
}

func newSheet(name string, rows [][]string) sheet {
	return sheet{
		name:       name,
		columns:    parseColumns(rows[0]),
		data:       rows,
		selected:   make(map[int]struct{}),
		startAtRow: 2,
	}
}

func readCSV(file string, sep rune) ([]string, map[string][][]string, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	if sep == 0 {
		sep = ';'
	}

	reader := csv.NewReader(f)
	reader.Comma = sep

	data, err := reader.ReadAll()
	if err != nil {
		return nil, nil, err
	}

	return []string{"Sheet1"}, map[string][][]string{"Sheet1": data}, nil
}

func readXLSX(file string) ([]string, map[string][][]string, error) {
	f, err := excelize.OpenFile(file)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	names := f.GetSheetList()
	sheets := make(map[string][][]string, len(names))

	for _, sheetName := range names {
		rows, err := f.GetRows(sheetName)
		if err != nil {
			return nil, nil, err
		}
		sheets[sheetName] = rows
	}

	return names, sheets, nil
}

func readFile(file string, sep rune) ([]sheet, error) {
	var (
		names []string
		data  map[string][][]string
		err   error
	)

	switch filepath.Ext(strings.ToLower(file)) {
	case ".xlsx":
		names, data, err = readXLSX(file)
	case ".csv":
		names, data, err = readCSV(file, sep)
	default:
		return nil, fmt.Errorf("unsupported file type: %s", file)
	}
	if err != nil {
		return nil, err
	}

	var sheets []sheet
	for _, sheetName := range names {
		rows := data[sheetName]
		if len(rows) == 0 || len(rows[0]) == 0 {
			continue
		}
		sheets = append(sheets, newSheet(sheetName, rows))
	}

	if len(sheets) == 0 {
		return nil, fmt.Errorf("no data found in %s", file)
	}

	return sheets, nil
}

func escapeSQLString(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}

func cellValue(row []string, i int) string {
	if i >= len(row) {
		return ""
	}
	return row[i]
}

func dataStartIndex(s sheet) int {
	start := s.startAtRow
	if start <= 0 {
		start = 2
	}
	return start - 1
}

func generateSQL(model model) string {
	if model.activeSheet < 0 || model.activeSheet >= len(model.sheets) {
		return ""
	}

	sheet := model.sheets[model.activeSheet]

	var cols []string

	s := "CREATE SCHEMA IF NOT EXISTS tmp;\n"
	s += "CREATE TABLE IF NOT EXISTS tmp." + model.table + " (\n"
	s += "id SERIAL PRIMARY KEY"

	for i, choice := range sheet.columns {
		if _, ok := sheet.selected[i]; ok {
			s += fmt.Sprintf(",\n%s %s", choice.name, choice.data_type)
			cols = append(cols, choice.name)
		}
	}

	s += ");\n"

	start := dataStartIndex(sheet)
	if len(cols) == 0 || start >= len(sheet.data) {
		return s
	}

	s += "INSERT INTO tmp." + model.table + " (" + strings.Join(cols, ",") + ") VALUES \n"

	for rowIndex, row := range sheet.data[start:] {
		var tmp []string

		for i := range sheet.columns {
			if _, ok := sheet.selected[i]; ok {
				tmp = append(tmp, escapeSQLString(cellValue(row, i)))
			}
		}

		if rowIndex > 0 {
			s += ","
		}

		s += "('" + strings.Join(tmp, "','") + "')\n"
	}

	return s
}

func (m model) currentSheet() (sheet, bool) {
	if m.activeSheet < 0 || m.activeSheet >= len(m.sheets) {
		return sheet{}, false
	}
	return m.sheets[m.activeSheet], true
}

func (m model) snapshot() historyRecord {
	sh, ok := m.currentSheet()
	if !ok {
		return historyRecord{FilePath: m.filePath, TableName: m.table, CSVSeparator: separatorString(m.csvSeparator)}
	}

	cols := make([]savedColumn, 0, len(sh.columns))
	for i, col := range sh.columns {
		_, selected := sh.selected[i]
		cols = append(cols, savedColumn{
			Name:     col.name,
			DataType: col.data_type,
			Selected: selected,
		})
	}

	return historyRecord{
		FilePath:     m.filePath,
		TableName:    m.table,
		SheetName:    sh.name,
		StartAtRow:   dataStartIndex(sh) + 1,
		CSVSeparator: separatorString(m.csvSeparator),
		Columns:      cols,
	}
}

func applyConfig(m *model, rec historyRecord, restoreSep bool) {
	if rec.TableName != "" {
		m.table = rec.TableName
	}
	if restoreSep && rec.CSVSeparator != "" {
		m.csvSeparator = parseSeparator(rec.CSVSeparator)
	}

	for i, s := range m.sheets {
		if s.name == rec.SheetName {
			m.activeSheet = i
			break
		}
	}

	if m.activeSheet < 0 || m.activeSheet >= len(m.sheets) {
		return
	}

	sh := &m.sheets[m.activeSheet]
	if rec.StartAtRow > 0 {
		sh.startAtRow = rec.StartAtRow
	}

	sh.selected = make(map[int]struct{})
	for i, col := range rec.Columns {
		if i >= len(sh.columns) {
			break
		}
		if col.Name != "" {
			sh.columns[i].name = col.Name
		}
		if col.DataType != "" {
			sh.columns[i].data_type = col.DataType
		}
		if col.Selected {
			sh.selected[i] = struct{}{}
		}
	}
}

func appendRenameInput(current string, msg tea.KeyMsg) string {
	switch msg.Type {
	case tea.KeyRunes:
		return current + string(msg.Runes)
	case tea.KeySpace:
		return current + " "
	default:
		return current
	}
}

func (m *model) openHistoryView() {
	m.historyMode = true
	m.renameMode = false
	m.status = ""
	if m.history == nil {
		m.historyItems = nil
		m.status = "history unavailable"
		return
	}

	items, err := m.history.list(50)
	if err != nil {
		m.status = err.Error()
		return
	}

	m.historyItems = items
	if m.historyCursor >= len(items) {
		m.historyCursor = 0
	}
}

func (m model) openHistoryRecord(rec historyRecord) model {
	sep := parseSeparator(rec.CSVSeparator)
	sheets, err := readFile(rec.FilePath, sep)
	if err != nil {
		m.status = err.Error()
		return m
	}

	m.sheets = sheets
	m.filePath = rec.FilePath
	m.csvSeparator = sep
	m.activeSheet = 0
	applyConfig(&m, rec, true)
	m.historyMode = false
	m.status = "loaded " + rec.FilePath
	return m
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, isKey := msg.(tea.KeyMsg)
	if !isKey {
		return m, nil
	}

	if m.historyMode {
		return m.updateHistory(key)
	}

	if m.renameMode {
		return m.updateRename(key)
	}

	sheet, ok := m.currentSheet()
	if !ok {
		switch key.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "h":
			m.openHistoryView()
		}
		return m, nil
	}

	return m.updateColumns(key, sheet)
}

func (m model) updateHistory(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "ctrl+c", "q":
		return m, tea.Quit

	case "esc":
		if len(m.sheets) > 0 {
			m.historyMode = false
			m.status = ""
		}

	case "up", "k":
		if m.historyCursor > 0 {
			m.historyCursor--
		}

	case "down", "j":
		if m.historyCursor < len(m.historyItems)-1 {
			m.historyCursor++
		}

	case "enter":
		if m.historyCursor >= 0 && m.historyCursor < len(m.historyItems) {
			return m.openHistoryRecord(m.historyItems[m.historyCursor]), nil
		}
	}

	return m, nil
}

func (m model) updateRename(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "ctrl+c":
		return m, tea.Quit

	case "enter":
		m.applyRename()
		m.renameMode = false
		m.renameTo = ""

	case "esc":
		m.renameMode = false
		m.renameTo = ""
		m.status = ""

	case "backspace":
		if len(m.renameTo) > 0 {
			m.renameTo = m.renameTo[:len(m.renameTo)-1]
		}

	default:
		switch m.renameType {
		case startAtRow:
			if key.Type == tea.KeyRunes {
				for _, r := range key.Runes {
					if r >= '0' && r <= '9' {
						m.renameTo += string(r)
					}
				}
			}
		case changeSeparator:
			if key.Type == tea.KeyRunes && len(key.Runes) > 0 {
				m.renameTo = string(key.Runes[len(key.Runes)-1])
			} else if key.Type == tea.KeySpace {
				m.renameTo = " "
			} else if key.String() == "tab" {
				m.renameTo = `\t`
			}
		default:
			m.renameTo = appendRenameInput(m.renameTo, key)
		}
	}

	return m, nil
}

func (m *model) applyRename() {
	sh, ok := m.currentSheet()
	if !ok && m.renameType != renameTable && m.renameType != changeSeparator {
		return
	}

	switch m.renameType {
	case renameTable:
		m.table = m.renameTo
		m.status = ""

	case renameColumn:
		if m.sheets[m.activeSheet].cursor >= 0 && m.sheets[m.activeSheet].cursor < len(sh.columns) {
			m.sheets[m.activeSheet].columns[m.sheets[m.activeSheet].cursor].name = m.renameTo
		}
		m.status = ""

	case renameDataType:
		if m.sheets[m.activeSheet].cursor >= 0 && m.sheets[m.activeSheet].cursor < len(sh.columns) {
			m.sheets[m.activeSheet].columns[m.sheets[m.activeSheet].cursor].data_type = m.renameTo
		}
		m.status = ""

	case startAtRow:
		n, err := strconv.Atoi(strings.TrimSpace(m.renameTo))
		if err != nil || n < 1 {
			m.status = "start row must be a positive number"
			return
		}
		m.sheets[m.activeSheet].startAtRow = n
		m.status = fmt.Sprintf("start at row %d", n)

	case changeSeparator:
		if strings.TrimSpace(m.renameTo) == "" && m.renameTo != " " {
			m.status = "separator required"
			return
		}
		m.csvSeparator = parseSeparator(m.renameTo)
		m.reloadCSV()
	}
}

func (m *model) reloadCSV() {
	if m.filePath == "" || !strings.EqualFold(filepath.Ext(m.filePath), ".csv") {
		m.status = "csv separator: " + separatorString(m.csvSeparator)
		return
	}

	rec := m.snapshot()
	rec.CSVSeparator = separatorString(m.csvSeparator)

	sheets, err := readFile(m.filePath, m.csvSeparator)
	if err != nil {
		m.status = err.Error()
		return
	}

	m.sheets = sheets
	m.activeSheet = 0
	applyConfig(m, rec, true)
	m.status = "reloaded with separator " + separatorString(m.csvSeparator)
}

func (m model) updateColumns(key tea.KeyMsg, sheet sheet) (tea.Model, tea.Cmd) {
	sh := &m.sheets[m.activeSheet]
	if sh.selected == nil {
		sh.selected = make(map[int]struct{})
	}

	switch key.String() {
	case "ctrl+c", "q":
		return m, tea.Quit

	case "enter":
		m.copySQL()

	case "up", "k":
		if sh.cursor > 0 {
			sh.cursor--
		}

	case "down", "j":
		if sh.cursor < len(sheet.columns)-1 {
			sh.cursor++
		}

	case "left", "[":
		if m.activeSheet > 0 {
			m.activeSheet--
			m.status = ""
		}

	case "right", "]":
		if m.activeSheet < len(m.sheets)-1 {
			m.activeSheet++
			m.status = ""
		}

	case " ":
		if len(sheet.columns) == 0 {
			break
		}
		if _, selected := sh.selected[sh.cursor]; selected {
			delete(sh.selected, sh.cursor)
		} else {
			sh.selected[sh.cursor] = struct{}{}
		}

	case "a":
		for i := range sheet.columns {
			if _, selected := sh.selected[i]; selected {
				delete(sh.selected, i)
			} else {
				sh.selected[i] = struct{}{}
			}
		}

	case "t":
		m.startRename(renameTable, m.table)

	case "c":
		if sh.cursor >= 0 && sh.cursor < len(sheet.columns) {
			m.startRename(renameDataType, sheet.columns[sh.cursor].data_type)
		}

	case "r":
		if sh.cursor >= 0 && sh.cursor < len(sheet.columns) {
			m.startRename(renameColumn, sheet.columns[sh.cursor].name)
		}

	case "s":
		m.startRename(startAtRow, strconv.Itoa(dataStartIndex(sheet)+1))

	case "d":
		m.startRename(changeSeparator, separatorString(m.csvSeparator))

	case "h":
		m.openHistoryView()
	}

	return m, nil
}

func (m *model) startRename(kind renameType, value string) {
	m.renameMode = true
	m.renameType = kind
	m.renameTo = value
	m.status = ""
}

func (m *model) copySQL() {
	sh, ok := m.currentSheet()
	if !ok || len(sh.selected) == 0 {
		m.status = "select at least one column"
		return
	}

	sql := generateSQL(*m)
	if sql == "" {
		m.status = "nothing to generate"
		return
	}

	status := "sql copied to clipboard"
	if m.clipboardReady {
		clipboard.Write(clipboard.FmtText, []byte(sql))
	} else {
		status = "sql generated, clipboard unavailable"
	}

	if m.history != nil && m.filePath != "" {
		if err := m.history.save(m.snapshot()); err != nil {
			status += "; history not saved"
		}
	}

	m.status = status
}

func (m model) View() string {
	if m.historyMode {
		return m.viewHistory()
	}

	sheet, ok := m.currentSheet()
	if !ok {
		return "No file loaded.\n\nh - history\nq - quit\n"
	}

	longestName := 11
	for _, choice := range sheet.columns {
		if len(choice.name) > longestName {
			longestName = len(choice.name)
		}
	}

	length := fmt.Sprintf("%d", longestName+2)

	s := "Column configuration\n\n"
	if m.filePath != "" {
		s += fmt.Sprintf("File: %s\n", m.filePath)
	}
	s += fmt.Sprintf("Sheet: %s (%d/%d)\n", sheet.name, m.activeSheet+1, len(m.sheets))
	s += fmt.Sprintf("Table name: %s\n", m.table)
	s += fmt.Sprintf("Start at row: %d\n", dataStartIndex(sheet)+1)
	if strings.EqualFold(filepath.Ext(m.filePath), ".csv") {
		s += fmt.Sprintf("CSV separator: %s\n", separatorString(m.csvSeparator))
	}
	s += "\n"
	s += fmt.Sprintf("      %-"+length+"s %s\n", "column name", "data type")

	for i, choice := range sheet.columns {
		cursor := " "
		if sheet.cursor == i {
			cursor = ">"
		}

		checked := " "
		if _, selected := sheet.selected[i]; selected {
			checked = "x"
		}

		s += fmt.Sprintf("%s [%s] %-"+length+"s %s\n", cursor, checked, choice.name, choice.data_type)
	}

	if m.renameMode {
		s += "\n" + m.renamePrompt(sheet) + "\n"
	} else if m.status != "" {
		s += "\n" + m.status + "\n"
	} else {
		s += "\n\n"
	}

	s += "\n"
	s += "space - select column\n"
	s += "a - select all\n"
	s += "r - rename\n"
	s += "c - change type\n"
	s += "s - start at row\n"
	s += "d - csv separator\n"
	if len(m.sheets) > 1 {
		s += "left/right - switch sheet\n"
	}
	s += "enter - generate sql and copy to clipboard\n"
	s += "h - history\n"
	s += "q - quit\n"
	s += "\n"

	return s
}

func (m model) renamePrompt(sheet sheet) string {
	switch m.renameType {
	case renameTable:
		return fmt.Sprintf("Rename table to: %s", m.renameTo)
	case renameColumn:
		name := ""
		if sheet.cursor >= 0 && sheet.cursor < len(sheet.columns) {
			name = sheet.columns[sheet.cursor].name
		}
		return fmt.Sprintf("Rename column %s to: %s", name, m.renameTo)
	case renameDataType:
		name := ""
		if sheet.cursor >= 0 && sheet.cursor < len(sheet.columns) {
			name = sheet.columns[sheet.cursor].name
		}
		return fmt.Sprintf("Change type of %s to: %s", name, m.renameTo)
	case startAtRow:
		return fmt.Sprintf("Start at row: %s", m.renameTo)
	case changeSeparator:
		return fmt.Sprintf("CSV separator: %s", m.renameTo)
	default:
		return fmt.Sprintf("Rename to: %s", m.renameTo)
	}
}

func (m model) viewHistory() string {
	s := "History\n\n"

	if len(m.historyItems) == 0 {
		s += "No saved configs yet. Generate SQL once to store a config.\n"
	}

	for i, item := range m.historyItems {
		cursor := " "
		if i == m.historyCursor {
			cursor = ">"
		}

		when := item.CreatedAt.Local().Format("2006-01-02 15:04")
		s += fmt.Sprintf("%s %s  %s  %s  (%s)\n", cursor, when, item.FilePath, item.TableName, item.SheetName)
	}

	if m.status != "" {
		s += "\n" + m.status + "\n"
	}

	s += "\n"
	s += "enter - open\n"
	if len(m.sheets) > 0 {
		s += "esc - back\n"
	}
	s += "q - quit\n"
	s += "\n"

	return s
}

func absPath(file string) string {
	abs, err := filepath.Abs(file)
	if err != nil {
		return file
	}
	return abs
}

func main() {
	sepFlag := flag.String("sep", ";", "CSV field separator (e.g. ',' or ';'; use '\\t' for tab)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: sheet2sql [-sep ';'] [file.xlsx|file.csv]\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	sep := parseSeparator(*sepFlag)

	clipboardReady := true
	if err := clipboard.Init(); err != nil {
		clipboardReady = false
		fmt.Fprintf(os.Stderr, "warning: clipboard unavailable: %v\n", err)
	}

	var store *historyStore
	if path, err := defaultHistoryPath(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: history unavailable: %v\n", err)
	} else if opened, err := openHistory(path); err != nil {
		fmt.Fprintf(os.Stderr, "warning: history unavailable: %v\n", err)
	} else {
		store = opened
		defer store.Close()
	}

	var m model

	if flag.NArg() >= 1 {
		filePath := absPath(flag.Arg(0))
		sheets, err := readFile(filePath, sep)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}

		m = initialModel(initOpts{
			sheets:         sheets,
			filePath:       filePath,
			sep:            sep,
			history:        store,
			clipboardReady: clipboardReady,
		})

		if store != nil {
			if rec, ok, err := store.latestForPath(filePath); err == nil && ok {
				applyConfig(&m, rec, false)
			}
		}
	} else {
		m = initialModel(initOpts{
			sep:            sep,
			history:        store,
			clipboardReady: clipboardReady,
		})
	}

	p := tea.NewProgram(m)
	if _, err := p.Run(); err != nil {
		fmt.Printf("Alas, there's been an error: %v", err)
		os.Exit(1)
	}
}

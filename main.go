package main

import (
	"encoding/csv"
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/xuri/excelize/v2"
	"golang.design/x/clipboard"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func throw(err error) {
	if err != nil {
		panic(err)
	}
}

type column struct {
	name, data_type string
}

type renameType int

const (
	renameTable renameType = iota
	renameColumn
	renameDataType
)

type model struct {
	table      string
	columns    []column
	data       [][]string
	cursor     int
	selected   map[int]struct{}
	renameType renameType
	renameMode bool
	renameTo   string
}

func initialModel(columns []column, data [][]string) model {
	now := time.Now().Format("20060102150405")

	return model{
		table:    "tmp_" + now,
		columns:  columns,
		data:     data,
		selected: make(map[int]struct{}),
	}
}

func readCSV(file string) [][]string {
	f, err := os.Open(file)
	throw(err)

	defer f.Close()

	reader := csv.NewReader(f)
	reader.Comma = ';'

	data, err := reader.ReadAll()
	throw(err)

	return data
}

func readXLSX(file string) [][]string {
	f, err := excelize.OpenFile(file)
	throw(err)

	defer f.Close()

	rows, err := f.GetRows("Sheet1")
	throw(err)

	return rows
}

func readFile(file string) ([]column, [][]string) {
	var data [][]string

	switch filepath.Ext(strings.ToLower(file)) {
	case ".xlsx":
		data = readXLSX(file)
	case ".csv":
		data = readCSV(file)
	default:
		throw(fmt.Errorf("unsupported file type: %s", file))
	}

	var columns []column

	replace := []string{" ", "-", ":", "/", "(", ")"}

	for _, col := range data[0] {
		name := strings.TrimSpace(col)
		name = strings.ToLower(name)

		for _, r := range replace {
			name = strings.ReplaceAll(name, r, "_")
		}

		columns = append(columns, column{name, "text"})
	}

	return columns, data
}

func generateSQL(model model) string {
	s := "CREATE SCHEMA IF NOT EXISTS tmp;\n"
	s += "CREATE TABLE IF NOT EXISTS tmp." + model.table + " (\n"
	s += "id SERIAL PRIMARY KEY"

	var cols []string

	for i, choice := range model.columns {
		if _, ok := model.selected[i]; ok {
			s += fmt.Sprintf(",\n%s %s", choice.name, choice.data_type)

			cols = append(cols, choice.name)
		}
	}

	s += ");\n"
	s += "INSERT INTO " + model.table + " (" + strings.Join(cols, ",") + ") VALUES \n"

	for rowIndex, row := range model.data[1:] {
		var tmp []string

		for i, _ := range model.columns {
			if _, ok := model.selected[i]; ok {
				tmp = append(tmp, row[i])
			}
		}

		if rowIndex > 0 {
			s += ","
		}

		s += "('" + strings.Join(tmp, "','") + "')"
		s += "\n"
	}

	return s
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.renameMode {
			switch msg.String() {
			case "ctrl+c":
				return m, tea.Quit

			case "enter":
				switch m.renameType {
				case renameTable:
					m.table = m.renameTo

				case renameColumn:
					m.columns[m.cursor].name = m.renameTo

				case renameDataType:
					m.columns[m.cursor].data_type = m.renameTo
				}

				m.renameMode = false
				m.renameTo = ""

			case "esc":
				m.renameMode = false
				m.renameTo = ""

			case "backspace":
				if len(m.renameTo) > 0 {
					m.renameTo = m.renameTo[:len(m.renameTo)-1]
				}

			default:
				m.renameTo += msg.String()
			}
		} else {
			switch msg.String() {
			case "ctrl+c", "q":
				return m, tea.Quit

			case "enter":
				sql := generateSQL(m)

				clipboard.Write(clipboard.FmtText, []byte(sql))

			case "up", "k":
				if m.cursor > 0 {
					m.cursor--
				}

			case "down", "j":
				if m.cursor < len(m.columns)-1 {
					m.cursor++
				}

			case " ":
				_, ok := m.selected[m.cursor]
				if ok {
					delete(m.selected, m.cursor)
				} else {
					m.selected[m.cursor] = struct{}{}
				}

			case "a":
				for i := range m.columns {
					_, ok := m.selected[i]
					if ok {
						delete(m.selected, i)
					} else {
						m.selected[i] = struct{}{}
					}
				}

			case "t":
				m.renameMode = true
				m.renameType = renameTable
				m.renameTo = m.table

			case "c":
				m.renameMode = true
				m.renameType = renameDataType
				m.renameTo = m.columns[m.cursor].data_type

			case "r":
				m.renameMode = true
				m.renameType = renameColumn
				m.renameTo = m.columns[m.cursor].name
			}
		}
	}

	return m, nil
}

func (m model) View() string {
	longestName := 11

	for _, choice := range m.columns {
		if len(choice.name) > longestName {
			longestName = len(choice.name)
		}
	}

	length := fmt.Sprintf("%d", longestName+2)

	s := "Column configuration\n\n"
	s += fmt.Sprintf("Table name: %s\n\n", m.table)
	s += fmt.Sprintf("      %-"+length+"s %s\n", "column name", "data type")

	for i, choice := range m.columns {
		cursor := " "
		if m.cursor == i {
			cursor = ">"
		}

		checked := " "
		if _, ok := m.selected[i]; ok {
			checked = "x"
		}

		s += fmt.Sprintf("%s [%s] %-"+length+"s %s\n", cursor, checked, choice.name, choice.data_type)
	}

	if m.renameMode {
		s += fmt.Sprintf("\nRename %s to: %s\n", m.columns[m.cursor].name, m.renameTo)
	} else {
		s += "\n\n"
	}

	s += "\nspace - select column, a - select all, r - rename, c - change type, enter - generate sql and copy to clipboard, h - history, q - quit\n"

	return s
}

func main() {
	var columns []column
	var data [][]string

	err := clipboard.Init()
	throw(err)

	if len(os.Args) > 1 {
		file := os.Args[1]

		columns, data = readFile(file)
	}

	p := tea.NewProgram(initialModel(columns, data))
	if _, err := p.Run(); err != nil {
		fmt.Printf("Alas, there's been an error: %v", err)
		os.Exit(1)
	}
}

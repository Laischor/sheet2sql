package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type savedColumn struct {
	Name     string `json:"name"`
	DataType string `json:"data_type"`
	Selected bool   `json:"selected"`
}

type historyRecord struct {
	ID           int64
	FilePath     string
	TableName    string
	SheetName    string
	StartAtRow   int
	CSVSeparator string
	Columns      []savedColumn
	CreatedAt    time.Time
}

type configJSON struct {
	StartAtRow   int           `json:"start_at_row"`
	CSVSeparator string        `json:"csv_separator"`
	Columns      []savedColumn `json:"columns"`
}

type historyStore struct {
	db *sql.DB
}

func defaultHistoryPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}

	dir = filepath.Join(dir, "sheet2sql")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	return filepath.Join(dir, "history.db"), nil
}

func openHistory(path string) (*historyStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			file_path TEXT NOT NULL,
			table_name TEXT NOT NULL,
			sheet_name TEXT NOT NULL,
			config_json TEXT NOT NULL,
			created_at TEXT NOT NULL
		)
	`); err != nil {
		db.Close()
		return nil, err
	}

	return &historyStore{db: db}, nil
}

func (h *historyStore) Close() error {
	if h == nil || h.db == nil {
		return nil
	}
	return h.db.Close()
}

func (h *historyStore) save(rec historyRecord) error {
	if h == nil {
		return nil
	}

	cfg, err := json.Marshal(configJSON{
		StartAtRow:   rec.StartAtRow,
		CSVSeparator: rec.CSVSeparator,
		Columns:      rec.Columns,
	})
	if err != nil {
		return err
	}

	_, err = h.db.Exec(
		`INSERT INTO history (file_path, table_name, sheet_name, config_json, created_at) VALUES (?, ?, ?, ?, ?)`,
		rec.FilePath,
		rec.TableName,
		rec.SheetName,
		string(cfg),
		time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

func scanHistory(scanner interface {
	Scan(dest ...any) error
}) (historyRecord, error) {
	var (
		rec     historyRecord
		cfgRaw  string
		created string
	)

	if err := scanner.Scan(&rec.ID, &rec.FilePath, &rec.TableName, &rec.SheetName, &cfgRaw, &created); err != nil {
		return historyRecord{}, err
	}

	var cfg configJSON
	if err := json.Unmarshal([]byte(cfgRaw), &cfg); err != nil {
		return historyRecord{}, err
	}

	rec.StartAtRow = cfg.StartAtRow
	rec.CSVSeparator = cfg.CSVSeparator
	rec.Columns = cfg.Columns
	rec.CreatedAt, _ = time.Parse(time.RFC3339, created)

	return rec, nil
}

func (h *historyStore) list(limit int) ([]historyRecord, error) {
	if h == nil {
		return nil, nil
	}

	rows, err := h.db.Query(
		`SELECT id, file_path, table_name, sheet_name, config_json, created_at
		 FROM history ORDER BY id DESC LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []historyRecord
	for rows.Next() {
		rec, err := scanHistory(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, rec)
	}

	return records, rows.Err()
}

func (h *historyStore) latestForPath(path string) (historyRecord, bool, error) {
	if h == nil {
		return historyRecord{}, false, nil
	}

	rec, err := scanHistory(h.db.QueryRow(
		`SELECT id, file_path, table_name, sheet_name, config_json, created_at
		 FROM history WHERE file_path = ? ORDER BY id DESC LIMIT 1`,
		path,
	))
	if err == sql.ErrNoRows {
		return historyRecord{}, false, nil
	}
	if err != nil {
		return historyRecord{}, false, err
	}

	return rec, true, nil
}

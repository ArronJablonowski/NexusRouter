package app

import (
	"context"
	"database/sql"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ArronJablonowski/NexusRouter/webui"
)

// Storage totals describe bytes on disk and stored rows/lines, not unique
// semantic events. Collector replicas remain distinct physical storage.
func measureLogStorage(ctx context.Context, p *webui.LoggingPage) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var seen []os.FileInfo
	for i := range p.Items {
		x := &p.Items[i]
		if x.Scope == "External" || x.ID == "qa" {
			x.Measurement = "Not included: external storage or mixed build artifacts"
			continue
		}
		paths := []string{x.Location}
		if x.ID == "dns" || x.ID == "process-audit" {
			pattern := "dns-*.jsonl"
			if x.ID == "process-audit" {
				pattern = "process-*.jsonl"
			}
			entries, err := os.ReadDir(x.Location)
			if err != nil {
				if os.IsNotExist(err) {
					zero := int64(0)
					x.Bytes = &zero
					x.Entries = &zero
					x.Measurement = "No files present"
				} else {
					p.TotalsPartial = true
					x.Measurement = "Unavailable"
				}
				continue
			}
			paths = nil
			for _, entry := range entries {
				match, _ := filepath.Match(pattern, entry.Name())
				if match {
					paths = append(paths, filepath.Join(x.Location, entry.Name()))
				}
			}
		}
		isDB := x.Format == "SQLite"
		if isDB {
			paths = append(paths, x.Location+"-wal", x.Location+"-shm", x.Location+"-journal")
		}
		size, count := int64(0), int64(0)
		sizeOK, countOK := true, true
		present := false
		for _, path := range paths {
			info, err := os.Lstat(path)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil || !info.Mode().IsRegular() {
				sizeOK = false
				countOK = false
				continue
			}
			present = true
			size += info.Size()
			duplicate := false
			for _, old := range seen {
				if os.SameFile(info, old) {
					duplicate = true
					break
				}
			}
			if !duplicate {
				seen = append(seen, info)
				p.TotalBytes += info.Size()
			}
			if isDB && path != x.Location {
				continue
			}
			var n int64
			if isDB {
				n, err = countDatabaseRows(ctx, path)
			} else {
				n, err = countLogLines(ctx, path, info.Size())
			}
			if err != nil {
				countOK = false
				continue
			}
			count += n
			if !duplicate {
				p.TotalEntries += n
			}
		}
		if sizeOK {
			x.Bytes = &size
		}
		if countOK {
			x.Entries = &count
		}
		if !sizeOK || !countOK {
			p.TotalsPartial = true
			x.Measurement = "Partial: a size or entry count is unavailable"
		} else if !present {
			x.Measurement = "No files present"
		} else if isDB {
			x.Measurement = "All stored database rows; size includes WAL/SHM/journal"
		} else {
			x.Measurement = "Text lines, including a final unterminated line"
		}
	}
}
func countLogLines(ctx context.Context, path string, size int64) (int64, error) {
	// Limit work on a page request. Large files retain accurate byte sizes but
	// report unavailable entries rather than an invented or truncated count.
	if size > 64<<20 {
		return 0, context.DeadlineExceeded
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	reader := io.LimitReader(f, size)
	buf := make([]byte, 64<<10)
	var count, read int64
	var last byte
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		n, err := reader.Read(buf)
		for _, b := range buf[:n] {
			if b == '\n' {
				count++
			}
			last = b
		}
		read += int64(n)
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}
	}
	if read > 0 && last != '\n' {
		count++
	}
	return count, nil
}
func countDatabaseRows(ctx context.Context, path string) (int64, error) {
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return 0, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%'")
	if err != nil {
		return 0, err
	}
	var tables []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			return 0, err
		}
		tables = append(tables, name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	var total int64
	for _, table := range tables {
		var n int64
		err = tx.QueryRowContext(ctx, `SELECT count(*) FROM "`+strings.ReplaceAll(table, `"`, `""`)+`"`).Scan(&n)
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

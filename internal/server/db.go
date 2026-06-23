package server

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/sherifhamad/shixo-msn/internal/proto"

	_ "modernc.org/sqlite"
)

type DB struct {
	sql *sql.DB
}

func OpenDB(path string) (*DB, error) {
	d, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)")
	if err != nil {
		return nil, err
	}
	if _, err := d.Exec(`
		CREATE TABLE IF NOT EXISTS items (
			id         TEXT PRIMARY KEY,
			kind       TEXT NOT NULL,
			source     TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			text       TEXT,
			filename   TEXT,
			size       INTEGER,
			sha256     TEXT,
			mime       TEXT,
			path       TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_items_created ON items(created_at DESC);
	`); err != nil {
		return nil, fmt.Errorf("init schema: %w", err)
	}
	// Idempotent additive migrations: extra columns on older DBs.
	// Each ALTER errors with "duplicate column name" once the column exists —
	// we discard the error on purpose.
	_, _ = d.Exec(`ALTER TABLE items ADD COLUMN title TEXT`)
	_, _ = d.Exec(`ALTER TABLE items ADD COLUMN folder TEXT`)
	return &DB{sql: d}, nil
}

func (db *DB) Close() error { return db.sql.Close() }

func (db *DB) Insert(it proto.Item, path string) error {
	_, err := db.sql.Exec(`
		INSERT INTO items (id, kind, source, created_at, title, folder, text, filename, size, sha256, mime, path)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		it.ID, string(it.Kind), it.Source, it.CreatedAt.UnixMilli(),
		nullStr(it.Title), nullStr(it.Folder),
		nullStr(it.Text), nullStr(it.Filename), nullInt(it.Size),
		nullStr(it.SHA256), nullStr(it.MIME), nullStr(path),
	)
	return err
}

// Update applies a PATCH. Only non-nil fields touch the row; text edits are
// ignored on file items.
func (db *DB) Update(id string, upd proto.UpdateItemRequest) error {
	var sets []string
	var args []any
	if upd.Title != nil {
		sets = append(sets, "title = ?")
		args = append(args, nullStr(*upd.Title))
	}
	if upd.Folder != nil {
		sets = append(sets, "folder = ?")
		args = append(args, nullStr(*upd.Folder))
	}
	if upd.Text != nil {
		// Guard text edits to text rows only.
		sets = append(sets, "text = CASE WHEN kind = 'text' THEN ? ELSE text END")
		args = append(args, nullStr(*upd.Text))
		// keep size of text items in sync with the new text length
		sets = append(sets, "size = CASE WHEN kind = 'text' THEN ? ELSE size END")
		args = append(args, int64(len(*upd.Text)))
	}
	if len(sets) == 0 {
		return nil
	}
	args = append(args, id)
	q := `UPDATE items SET ` + strings.Join(sets, ", ") + ` WHERE id = ?`
	res, err := db.sql.Exec(q, args...)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// Get returns the item plus its on-disk path (empty for text items).
func (db *DB) Get(id string) (proto.Item, string, error) {
	var (
		it      proto.Item
		created int64
		kind    string
		title   sql.NullString
		folder  sql.NullString
		text    sql.NullString
		fname   sql.NullString
		size    sql.NullInt64
		sha     sql.NullString
		mime    sql.NullString
		path    sql.NullString
	)
	err := db.sql.QueryRow(`
		SELECT id, kind, source, created_at, title, folder, text, filename, size, sha256, mime, path
		FROM items WHERE id = ?`, id,
	).Scan(&it.ID, &kind, &it.Source, &created, &title, &folder, &text, &fname, &size, &sha, &mime, &path)
	if err != nil {
		return proto.Item{}, "", err
	}
	it.Kind = proto.Kind(kind)
	it.CreatedAt = time.UnixMilli(created)
	it.Title = title.String
	it.Folder = folder.String
	it.Text = text.String
	it.Filename = fname.String
	it.Size = size.Int64
	it.SHA256 = sha.String
	it.MIME = mime.String
	return it, path.String, nil
}

// Delete removes the item row. Caller is responsible for the on-disk files.
func (db *DB) Delete(id string) error {
	res, err := db.sql.Exec(`DELETE FROM items WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// List returns items newest-first, capped at limit.
func (db *DB) List(limit int) ([]proto.Item, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := db.sql.Query(`
		SELECT id, kind, source, created_at, title, folder, text, filename, size, sha256, mime
		FROM items ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []proto.Item{}
	for rows.Next() {
		var (
			it      proto.Item
			created int64
			kind    string
			title   sql.NullString
			folder  sql.NullString
			text    sql.NullString
			fname   sql.NullString
			size    sql.NullInt64
			sha     sql.NullString
			mime    sql.NullString
		)
		if err := rows.Scan(&it.ID, &kind, &it.Source, &created, &title, &folder, &text, &fname, &size, &sha, &mime); err != nil {
			return nil, err
		}
		it.Kind = proto.Kind(kind)
		it.CreatedAt = time.UnixMilli(created)
		it.Title = title.String
		it.Folder = folder.String
		it.Text = text.String
		it.Filename = fname.String
		it.Size = size.Int64
		it.SHA256 = sha.String
		it.MIME = mime.String
		out = append(out, it)
	}
	return out, rows.Err()
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(i int64) any {
	if i == 0 {
		return nil
	}
	return i
}

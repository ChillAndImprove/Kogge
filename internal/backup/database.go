// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package backup

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Tables whose rows are not part of a backup: the migration versions (the restore migrates), River's
// coordination of running processes, and the position of the audit forwarding, which belongs to the database
// cluster's transactions (a restored installation forwards the events to come).
var skipTables = map[string]bool{
	"kogge_schema_version": true, "river_migration": true, "river_leader": true, "river_client": true,
	"river_client_queue": true, "audit_forwarding": true,
}

// Columns that are not part of a backup: the transactions that wrote audit events are the source cluster's; restored
// events get the restore's.
var skipColumns = map[string]map[string]bool{
	"audit_events": {"xact": true},
}

// textSettings make COPY's text format independent of the server's configuration and exact for floats.
var textSettings = []string{
	"SET LOCAL DateStyle = 'ISO, YMD'", "SET LOCAL IntervalStyle = 'postgres'", "SET LOCAL extra_float_digits = 3",
	"SET LOCAL bytea_output = 'hex'", "SET LOCAL statement_timeout = 0", "SET LOCAL idle_in_transaction_session_timeout = 0",
}

// snapshot describes the database as tx sees it and locks its tables against schema changes until tx ends.
func snapshot(ctx context.Context, tx pgx.Tx) (*Manifest, error) {
	for _, set := range textSettings {
		if _, err := tx.Exec(ctx, set); err != nil {
			return nil, err
		}
	}
	m := &Manifest{Format: formatV1}
	if err := tx.QueryRow(ctx, "SELECT COALESCE(max(version_id), 0) FROM kogge_schema_version").Scan(&m.Schema.Schema); err != nil {
		return nil, fmt.Errorf("schema version: %w", err)
	}
	if err := tx.QueryRow(ctx, "SELECT COALESCE(max(version), 0) FROM river_migration WHERE line = 'main'").Scan(&m.Schema.Jobs); err != nil {
		return nil, fmt.Errorf("job queue schema version: %w", err)
	}
	rows, err := tx.Query(ctx, `SELECT c.oid, c.relname FROM pg_class c
		WHERE c.relnamespace = current_schema()::regnamespace AND c.relkind = 'r' AND c.relpersistence = 'p' ORDER BY c.relname`)
	if err != nil {
		return nil, err
	}
	type rel struct {
		oid  uint32
		name string
	}
	rels, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (rel, error) {
		var x rel
		err := r.Scan(&x.oid, &x.name)
		return x, err
	})
	if err != nil {
		return nil, err
	}
	var names []string
	for _, r := range rels {
		if skipTables[r.name] {
			continue
		}
		rows, err := tx.Query(ctx, `SELECT attname FROM pg_attribute
			WHERE attrelid = $1 AND attnum > 0 AND NOT attisdropped AND attgenerated = '' ORDER BY attnum`, r.oid)
		if err != nil {
			return nil, err
		}
		cols, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return nil, err
		}
		cols = slices.DeleteFunc(cols, func(c string) bool { return skipColumns[r.name][c] })
		m.Tables = append(m.Tables, Table{Name: r.name, Columns: cols})
		names = append(names, pgx.Identifier{r.name}.Sanitize())
	}
	if len(names) > 0 {
		if _, err := tx.Exec(ctx, "LOCK TABLE "+strings.Join(names, ", ")+" IN ACCESS SHARE MODE"); err != nil {
			return nil, err
		}
	}

	// Sequences, except those of tables that are not backed up. Sequences are not transactional: the values may
	// be ahead of the snapshot, which only leaves gaps.
	rows, err = tx.Query(ctx, `SELECT s.relname, COALESCE(t.relname, '') FROM pg_class s
		LEFT JOIN pg_depend d ON d.classid = 'pg_class'::regclass AND d.objid = s.oid AND d.refclassid = 'pg_class'::regclass
			AND d.deptype IN ('a', 'i')
		LEFT JOIN pg_class t ON t.oid = d.refobjid
		WHERE s.relkind = 'S' AND s.relnamespace = current_schema()::regnamespace ORDER BY s.relname`)
	if err != nil {
		return nil, err
	}
	type seq struct{ name, owner string }
	seqs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (seq, error) {
		var x seq
		err := r.Scan(&x.name, &x.owner)
		return x, err
	})
	if err != nil {
		return nil, err
	}
	for _, s := range seqs {
		if skipTables[s.owner] {
			continue
		}
		v := Sequence{Name: s.name}
		if err := tx.QueryRow(ctx, "SELECT last_value, is_called FROM "+pgx.Identifier{s.name}.Sanitize()).Scan(&v.Value, &v.Called); err != nil {
			return nil, err
		}
		m.Sequences = append(m.Sequences, v)
	}
	return m, nil
}

func copySQL(t Table, direction string) string {
	cols := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		cols[i] = pgx.Identifier{c}.Sanitize()
	}
	return fmt.Sprintf("COPY %s (%s) %s", pgx.Identifier{t.Name}.Sanitize(), strings.Join(cols, ", "), direction)
}

// database adds the rows of every table of the snapshot.
func (b *backup) database(ctx context.Context, snap pgx.Tx, m *Manifest) error {
	conn := snap.Conn().PgConn()
	for _, t := range m.Tables {
		c := &chunker{b: b, table: t.Name}
		tag, err := conn.CopyTo(ctx, c, copySQL(t, "TO STDOUT"))
		if err != nil {
			return fmt.Errorf("table %s: %w", t.Name, err)
		}
		if err := c.flush(); err != nil {
			return err
		}
		b.sum.Rows[t.Name] = tag.RowsAffected()
	}
	return nil
}

// loader loads the rows of a backup in one transaction, into the schema the migrations created. Foreign keys are
// dropped while loading (tables refer to each other in cycles) and added again at the end, which checks them.
type loader struct {
	tx       pgx.Tx
	tables   map[string]Table
	rows     map[string]int64
	fks      []foreignKey
	triggers []string
}

type foreignKey struct{ table, name, def string }

func newLoader(ctx context.Context, tx pgx.Tx, m *Manifest) (*loader, error) {
	l := &loader{tx: tx, tables: map[string]Table{}, rows: map[string]int64{}}
	for _, set := range textSettings {
		if _, err := tx.Exec(ctx, set); err != nil {
			return nil, err
		}
	}
	rows, err := tx.Query(ctx, `SELECT conrelid::regclass::text, conname, pg_get_constraintdef(oid) FROM pg_constraint
		WHERE contype = 'f' AND connamespace = current_schema()::regnamespace ORDER BY 1, 2`)
	if err != nil {
		return nil, err
	}
	l.fks, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (foreignKey, error) {
		var fk foreignKey
		err := r.Scan(&fk.table, &fk.name, &fk.def)
		return fk, err
	})
	if err != nil {
		return nil, err
	}
	for _, fk := range l.fks {
		if _, err := tx.Exec(ctx, "ALTER TABLE "+fk.table+" DROP CONSTRAINT "+pgx.Identifier{fk.name}.Sanitize()); err != nil {
			return nil, err
		}
	}
	rows, err = tx.Query(ctx, `SELECT DISTINCT t.tgrelid::regclass::text FROM pg_trigger t JOIN pg_class c ON c.oid = t.tgrelid
		WHERE NOT t.tgisinternal AND c.relnamespace = current_schema()::regnamespace`)
	if err != nil {
		return nil, err
	}
	if l.triggers, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return nil, err
	}
	for _, t := range l.triggers {
		if _, err := tx.Exec(ctx, "ALTER TABLE "+t+" DISABLE TRIGGER USER"); err != nil {
			return nil, err
		}
	}
	// Migrations insert some rows of their own (the system user, default grants); the backup has them too.
	var names []string
	for _, t := range m.Tables {
		l.tables[t.Name] = t
		names = append(names, pgx.Identifier{t.Name}.Sanitize())
	}
	if len(names) > 0 {
		if _, err := tx.Exec(ctx, "TRUNCATE "+strings.Join(names, ", ")); err != nil {
			return nil, err
		}
	}
	return l, nil
}

// load loads rows of a table in COPY's text format.
func (l *loader) load(ctx context.Context, table string, r io.Reader) error {
	t, ok := l.tables[table]
	if !ok {
		return fmt.Errorf("the backup has data of table %s, which its manifest does not list", table)
	}
	tag, err := l.tx.Conn().PgConn().CopyFrom(ctx, r, copySQL(t, "FROM STDIN"))
	if err != nil {
		return fmt.Errorf("table %s: %w", table, err)
	}
	l.rows[table] += tag.RowsAffected()
	return nil
}

// finish checks the row counts, adds the foreign keys again and sets the sequences.
func (l *loader) finish(ctx context.Context, m *Manifest, sum *Summary) error {
	for table, n := range sum.Rows {
		if l.rows[table] != n {
			return fmt.Errorf("table %s: %d rows restored, but the backup has %d", table, l.rows[table], n)
		}
	}
	for _, fk := range l.fks {
		if _, err := l.tx.Exec(ctx, "ALTER TABLE "+fk.table+" ADD CONSTRAINT "+pgx.Identifier{fk.name}.Sanitize()+" "+fk.def); err != nil {
			return fmt.Errorf("foreign key %s of %s: %w", fk.name, fk.table, err)
		}
	}
	for _, t := range l.triggers {
		if _, err := l.tx.Exec(ctx, "ALTER TABLE "+t+" ENABLE TRIGGER USER"); err != nil {
			return err
		}
	}
	for _, s := range m.Sequences {
		if _, err := l.tx.Exec(ctx, "SELECT setval($1::regclass, $2, $3)", pgx.Identifier{s.Name}.Sanitize(), s.Value, s.Called); err != nil {
			return fmt.Errorf("sequence %s: %w", s.Name, err)
		}
	}
	return nil
}

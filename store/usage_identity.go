package store

// SQLite cannot drop a table primary key. Preserve every ledger column in a
// transaction, then install separate identities for request-addressable rows
// and legacy transcript rows. A timestamp is not a request identifier.
func (s *Store) migrateSQLiteUsageIdentity() error {
	rows, err := s.writer.Query(`PRAGMA table_info(usage_records)`)
	if err != nil {
		return err
	}
	hasPrimaryKey := false
	for rows.Next() {
		var cid, notNull, pk int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			return err
		}
		hasPrimaryKey = hasPrimaryKey || pk != 0
	}
	err = rows.Err()
	rows.Close()
	if err != nil || !hasPrimaryKey {
		return err
	}
	tx, err := s.writer.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.Exec(`CREATE TABLE usage_records_identity (
 source TEXT, session_id TEXT, project TEXT, model TEXT, timestamp TEXT,
 input_tokens INTEGER, output_tokens INTEGER, cache_read_tokens INTEGER, cache_write_tokens INTEGER,
 tool TEXT, cost_usd REAL, host TEXT, provenance TEXT, provenance_rank INTEGER DEFAULT 0,
 token_quality TEXT DEFAULT 'exact', cost_kind TEXT DEFAULT 'calculated',
 conversation_id TEXT, trace_id TEXT, turn_id TEXT, request_id TEXT, runtime_id TEXT
);
INSERT INTO usage_records_identity
 SELECT source,session_id,project,model,timestamp,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,
 tool,cost_usd,host,provenance,provenance_rank,token_quality,cost_kind,conversation_id,trace_id,turn_id,request_id,runtime_id
 FROM usage_records;
DROP TABLE usage_records;
ALTER TABLE usage_records_identity RENAME TO usage_records;`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

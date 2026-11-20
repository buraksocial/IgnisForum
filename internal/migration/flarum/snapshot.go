package flarum

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const SnapshotFormatVersion = 1

// Snapshot is the versioned envelope written by tools/flarum_snapshot.php.
type Snapshot struct {
	FormatVersion int                        `json:"format_version"`
	Source        string                     `json:"source"`
	Tables        map[string]json.RawMessage `json:"tables"`
	GeneratedAt   string                     `json:"generated_at"`
}

type tableEnvelope struct {
	SourceCount int               `json:"source_count"`
	Rows        []json.RawMessage `json:"rows"`
}

func ReadSnapshot(r io.Reader) (*Snapshot, [32]byte, error) {
	var zero [32]byte
	data, err := io.ReadAll(io.LimitReader(r, 1<<30+1))
	if err != nil {
		return nil, zero, err
	}
	if len(data) > 1<<30 {
		return nil, zero, errors.New("flarum snapshot exceeds 1 GiB limit")
	}
	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, zero, fmt.Errorf("decode flarum snapshot: %w", err)
	}
	if err := snapshot.Validate(); err != nil {
		return nil, zero, err
	}
	canonical, err := json.Marshal(snapshot.canonical())
	if err != nil {
		return nil, zero, fmt.Errorf("canonicalize flarum snapshot: %w", err)
	}
	return &snapshot, sha256Bytes(canonical), nil
}

func sha256Bytes(data []byte) [32]byte {
	return sha256.Sum256(data)
}

// Validate checks the source marker, format version, and the snapshot's own
// per-table reconciliation counts before any destination write is attempted.
func (s *Snapshot) Validate() error {
	if s.FormatVersion != SnapshotFormatVersion {
		return fmt.Errorf("unsupported snapshot format version %d", s.FormatVersion)
	}
	if s.Source != "flarum" {
		return fmt.Errorf("unsupported snapshot source %q", s.Source)
	}
	if s.Tables == nil {
		return errors.New("snapshot has no tables")
	}
	for name, raw := range s.Tables {
		var table tableEnvelope
		if err := json.Unmarshal(raw, &table); err != nil {
			return fmt.Errorf("decode table %q: %w", name, err)
		}
		if table.SourceCount < 0 || table.SourceCount != len(table.Rows) {
			return fmt.Errorf("table %q source count %d does not match exported row count %d", name, table.SourceCount, len(table.Rows))
		}
	}
	for _, name := range []string{"users", "groups", "group_user", "discussions", "posts", "tags", "discussion_tag", "post_likes", "flags", "notifications"} {
		if _, ok := s.Tables[name]; !ok {
			return fmt.Errorf("snapshot is missing required table %q", name)
		}
	}
	return nil
}

func (s *Snapshot) canonical() Snapshot {
	copy := *s
	copy.GeneratedAt = ""
	return copy
}

func DecodeTable[T any](s *Snapshot, name string) ([]T, error) {
	raw, ok := s.Tables[name]
	if !ok {
		return nil, fmt.Errorf("snapshot is missing table %q", name)
	}
	var table tableEnvelope
	if err := json.Unmarshal(raw, &table); err != nil {
		return nil, fmt.Errorf("decode table %q: %w", name, err)
	}
	rows := make([]T, 0, len(table.Rows))
	for i, row := range table.Rows {
		var value T
		if err := json.Unmarshal(row, &value); err != nil {
			return nil, fmt.Errorf("decode %s row %d: %w", name, i+1, err)
		}
		rows = append(rows, value)
	}
	return rows, nil
}

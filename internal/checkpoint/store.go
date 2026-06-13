// Package checkpoint persists resumable DepBisect trial state.
package checkpoint

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/skyneticist/depbisect/internal/engine"
)

// FileStore stores an append-only JSON-lines checkpoint. A crash can damage
// only the final record, which Load safely ignores when it is truncated.
type FileStore struct {
	path string
}

// NewFileStore returns a checkpoint store at path.
func NewFileStore(path string) *FileStore {
	return &FileStore{path: path}
}

type record struct {
	Type       string             `json:"type"`
	Checkpoint *engine.Checkpoint `json:"checkpoint,omitempty"`
	Trial      *engine.Trial      `json:"trial,omitempty"`
}

// Start replaces any prior checkpoint with a new header.
func (s *FileStore) Start(cp engine.Checkpoint) error {
	cp.Trials = nil
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if err := writeRecord(f, record{Type: "checkpoint", Checkpoint: &cp}); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// Append durably records one completed trial.
func (s *FileStore) Append(trial engine.Trial) error {
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	if err := writeRecord(f, record{Type: "trial", Trial: &trial}); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func writeRecord(f *os.File, value record) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = f.Write(data)
	return err
}

// Load reads the most recent complete checkpoint state.
func (s *FileStore) Load() (*engine.Checkpoint, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil, err
	}
	lines := bytes.Split(data, []byte{'\n'})
	var cp *engine.Checkpoint
	for i, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var rec record
		if err := json.Unmarshal(line, &rec); err != nil {
			isTruncatedTail := i == len(lines)-1 && !bytes.HasSuffix(data, []byte{'\n'})
			if isTruncatedTail {
				break
			}
			return nil, fmt.Errorf("parse checkpoint record %d: %w", i+1, err)
		}
		switch rec.Type {
		case "checkpoint":
			if cp != nil || rec.Checkpoint == nil {
				return nil, fmt.Errorf("invalid checkpoint header at record %d", i+1)
			}
			cp = rec.Checkpoint
			cp.Trials = nil
		case "trial":
			if cp == nil || rec.Trial == nil {
				return nil, fmt.Errorf("trial before checkpoint header at record %d", i+1)
			}
			cp.Trials = append(cp.Trials, *rec.Trial)
		default:
			return nil, fmt.Errorf("unknown checkpoint record type %q at record %d", rec.Type, i+1)
		}
	}
	if cp == nil {
		return nil, errors.New("checkpoint contains no complete header")
	}
	return cp, nil
}

// Clear removes a completed checkpoint.
func (s *FileStore) Clear() error {
	err := os.Remove(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

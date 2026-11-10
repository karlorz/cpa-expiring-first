package live

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const stateFileVersion = 1

// StateFile saves last-used Live pick state and remembered usage-limit deadlines.
type StateFile struct {
	path string
}

// NewStateFile returns a StateFile at an absolute path.
func NewStateFile(path string) StateFile {
	return StateFile{path: path}
}

type stateFileBody struct {
	Version    int                 `json:"version"`
	LastAuthID string              `json:"last_auth_id,omitempty"`
	LastAt     *time.Time          `json:"last_picked_at,omitempty"`
	Limits     []stateFileLimit    `json:"limits,omitempty"`
	Decisions  []stateFileDecision `json:"decisions,omitempty"`
}

type stateFileDecision struct {
	ID       int64           `json:"id"`
	At       time.Time       `json:"at"`
	Outcome  DecisionOutcome `json:"outcome"`
	AuthID   string          `json:"auth_id,omitempty"`
	Matching int             `json:"matching"`
	Ready    int             `json:"ready"`
	Skips    []Skip          `json:"skips,omitempty"`
	Reason   string          `json:"reason,omitempty"`
}

type stateFileLimit struct {
	AuthID string    `json:"auth_id"`
	Until  time.Time `json:"until"`
}

// Load reads a saved snapshot. A missing file is not an error.
func (f StateFile) Load() (Snapshot, error) {
	raw, errRead := os.ReadFile(f.path)
	if errors.Is(errRead, fs.ErrNotExist) {
		return Snapshot{}, nil
	}
	if errRead != nil {
		return Snapshot{}, fmt.Errorf("read live state file: %w", errRead)
	}
	var body stateFileBody
	if errUnmarshal := json.Unmarshal(raw, &body); errUnmarshal != nil {
		return Snapshot{}, fmt.Errorf("decode live state file: %w", errUnmarshal)
	}
	if body.Version != stateFileVersion {
		return Snapshot{}, fmt.Errorf("live state file version %d, want %d", body.Version, stateFileVersion)
	}
	out := Snapshot{LastAuthID: strings.TrimSpace(body.LastAuthID)}
	if body.LastAt != nil {
		out.LastAt = body.LastAt.UTC()
	}
	if len(body.Limits) > 0 {
		out.Limits = make(map[string]time.Time, len(body.Limits))
		for _, entry := range body.Limits {
			id := strings.TrimSpace(entry.AuthID)
			if id == "" || entry.Until.IsZero() {
				continue
			}
			out.Limits[id] = entry.Until.UTC()
		}
	}
	if len(body.Decisions) > 0 {
		out.Decisions = make([]Decision, 0, len(body.Decisions))
		for _, entry := range body.Decisions {
			if entry.ID <= 0 || entry.Outcome == "" {
				continue
			}
			out.Decisions = append(out.Decisions, Decision{
				ID:       entry.ID,
				At:       entry.At.UTC(),
				Outcome:  entry.Outcome,
				AuthID:   strings.TrimSpace(entry.AuthID),
				Matching: entry.Matching,
				Ready:    entry.Ready,
				Skips:    copySkips(entry.Skips),
				Reason:   strings.TrimSpace(entry.Reason),
			})
		}
	}
	return out, nil
}

// Save replaces the file atomically with mode 0600.
func (f StateFile) Save(snapshot Snapshot) error {
	body := stateFileBody{Version: stateFileVersion, LastAuthID: strings.TrimSpace(snapshot.LastAuthID)}
	if !snapshot.LastAt.IsZero() {
		at := snapshot.LastAt.UTC()
		body.LastAt = &at
	}
	for id, until := range snapshot.Limits {
		id = strings.TrimSpace(id)
		if id == "" || until.IsZero() {
			continue
		}
		body.Limits = append(body.Limits, stateFileLimit{AuthID: id, Until: until.UTC()})
	}
	for _, decision := range snapshot.Decisions {
		if decision.ID <= 0 || decision.Outcome == "" {
			continue
		}
		body.Decisions = append(body.Decisions, stateFileDecision{
			ID:       decision.ID,
			At:       decision.At.UTC(),
			Outcome:  decision.Outcome,
			AuthID:   strings.TrimSpace(decision.AuthID),
			Matching: decision.Matching,
			Ready:    decision.Ready,
			Skips:    copySkips(decision.Skips),
			Reason:   strings.TrimSpace(decision.Reason),
		})
	}
	raw, errMarshal := json.MarshalIndent(body, "", "  ")
	if errMarshal != nil {
		return fmt.Errorf("encode live state file: %w", errMarshal)
	}
	tmp, errCreate := os.CreateTemp(filepath.Dir(f.path), filepath.Base(f.path)+".*.tmp")
	if errCreate != nil {
		return fmt.Errorf("create live state file: %w", errCreate)
	}
	tmpPath := tmp.Name()
	_, errWrite := tmp.Write(append(raw, '\n'))
	if errWrite == nil {
		errWrite = tmp.Sync()
	}
	if errClose := tmp.Close(); errWrite == nil {
		errWrite = errClose
	}
	if errWrite == nil {
		errWrite = os.Chmod(tmpPath, 0o600)
	}
	if errWrite == nil {
		errWrite = os.Rename(tmpPath, f.path)
	}
	if errWrite != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write live state file: %w", errWrite)
	}
	return nil
}

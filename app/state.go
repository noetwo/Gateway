package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

func loadState(path string, cooldown float64) (*AppState, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}

	state := &AppState{filePath: path, Cooldown: cooldown, Keys: map[string]*Key{}}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		state.normalizeRetrySettings()
		state.normalizeHobbyBlockedModels()
		state.normalizeTierRoutingSettings()
		if err := state.save(); err != nil {
			return nil, err
		}
		return state, nil
	}

	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, state); err != nil {
			return nil, err
		}
	}
	if state.Keys == nil {
		state.Keys = map[string]*Key{}
	}
	state.filePath = path
	if state.Cooldown <= 0 {
		state.Cooldown = cooldown
	}
	state.normalizeRetrySettings()
	state.normalizeHobbyBlockedModels()
	state.normalizeTierRoutingSettings()

	for id, k := range state.Keys {
		if k.CreatedAt.IsZero() {
			k.CreatedAt = keyImportTime(id, k)
		}
		if k.UpdatedAt.IsZero() && !k.CreatedAt.IsZero() {
			k.UpdatedAt = k.CreatedAt
		}
		roll30DayWindow(k)
	}
	if err := state.save(); err != nil {
		return nil, err
	}
	return state, nil
}

func (s *AppState) save() error {
	s.LastSaved = time.Now().UTC()
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.filePath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.filePath)
}

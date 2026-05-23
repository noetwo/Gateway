package app

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

func publicView(k *Key) PublicKeyView {
	ref := k.LastProxyAt
	if ref.IsZero() {
		ref = k.CreatedAt
	}
	resetAt := ref.Add(30 * 24 * time.Hour)
	return PublicKeyView{
		ID:              k.ID,
		Name:            k.Name,
		Tier:            k.Tier,
		MonthlySpentUSD: round2(k.MonthlySpentUSD),
		Paused:          k.Paused,
		LastBalance:     round2(k.LastBalance),
		LastUsedTotal:   round2(k.LastUsedTotal),
		LastPollAt:      k.LastPollAt,
		LastProxyAt:     k.LastProxyAt,
		LastErr:         k.LastErr,
		RequestCount:    k.RequestCount,
		ProxyReqCount:   k.ProxyReqCount,
		CreatedAt:       k.CreatedAt,
		UpdatedAt:       k.UpdatedAt,
		ResetAt:         resetAt,
		Scrapped:        k.Scrapped,
		ScrappedErr:     k.ScrappedErr,
		ScrappedAt:      k.ScrappedAt,
		ConsecFails:     k.ConsecFails,
	}
}

func keyImportTime(id string, k *Key) time.Time {
	if k == nil {
		return time.Time{}
	}
	if !k.CreatedAt.IsZero() {
		return k.CreatedAt
	}
	if ns, err := strconv.ParseInt(id, 36, 64); err == nil && ns > 0 {
		t := time.Unix(0, ns).UTC()
		if t.After(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)) && t.Before(time.Now().UTC().Add(24*time.Hour)) {
			return t
		}
	}
	if !k.UpdatedAt.IsZero() {
		return k.UpdatedAt
	}
	return time.Time{}
}

func importTimeLess(a time.Time, aID string, b time.Time, bID string) bool {
	switch {
	case a.IsZero() && b.IsZero():
		return aID < bID
	case a.IsZero():
		return false
	case b.IsZero():
		return true
	case !a.Equal(b):
		return a.Before(b)
	default:
		return aID < bID
	}
}

func keyAvailableForSticky(k *Key) bool {
	return k != nil && !k.Scrapped && !k.Paused && strings.TrimSpace(k.APIKey) != ""
}

func (s *AppState) stickyKeyAvailableLocked(id string) bool {
	k, ok := s.Keys[id]
	return ok && keyAvailableForSticky(k)
}

func (s *AppState) firstAvailableStickyKeyIDLocked() string {
	bestID := ""
	var bestTime time.Time
	for id, k := range s.Keys {
		if !keyAvailableForSticky(k) {
			continue
		}
		keyID := k.ID
		if keyID == "" {
			keyID = id
		}
		t := keyImportTime(keyID, k)
		if bestID == "" || importTimeLess(t, keyID, bestTime, bestID) {
			bestID = keyID
			bestTime = t
		}
	}
	return bestID
}

func roll30DayWindow(k *Key) {
	ref := k.LastProxyAt
	if ref.IsZero() {
		ref = k.CreatedAt
	}
	if time.Since(ref) >= 30*24*time.Hour {
		k.MonthlySpentUSD = 0
		k.Paused = false
	}
}

// renumberKeys 只对自动编号的 key 重排（活跃池里 "N"、报废池里 "报废N"）。
// 自定义名称（导入时带的）和空名称一概不动。
func (s *AppState) renumberKeys() {
	type pair struct {
		id  string
		num int
	}
	var active, scrapped []pair
	for id, k := range s.Keys {
		raw := k.Name
		if k.Scrapped {
			raw = strings.TrimPrefix(raw, "报废")
		}
		if raw == "" {
			continue
		}
		n, err := strconv.Atoi(raw)
		if err != nil {
			continue
		}
		if k.Scrapped {
			scrapped = append(scrapped, pair{id, n})
		} else {
			active = append(active, pair{id, n})
		}
	}
	sort.Slice(active, func(i, j int) bool { return active[i].num < active[j].num })
	sort.Slice(scrapped, func(i, j int) bool { return scrapped[i].num < scrapped[j].num })
	for i, p := range active {
		s.Keys[p.id].Name = strconv.Itoa(i + 1)
	}
	for i, p := range scrapped {
		s.Keys[p.id].Name = "报废" + strconv.Itoa(i+1)
	}
}

func (s *AppState) keyIDs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.Keys))
	for id := range s.Keys {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (s *AppState) activeKeyIDs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.Keys))
	for id, k := range s.Keys {
		if !k.Scrapped {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

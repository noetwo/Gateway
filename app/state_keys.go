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

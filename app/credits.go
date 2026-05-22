package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func fetchCredits(baseURL, apiKey string) (*struct {
	Balance   float64
	TotalUsed float64
}, error) {
	req, err := http.NewRequest(http.MethodGet, baseURL+"/credits", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("credits status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var cr CreditsResp
	if err := json.Unmarshal(b, &cr); err != nil {
		return nil, err
	}
	balance, err := strconv.ParseFloat(strings.TrimSpace(cr.Balance), 64)
	if err != nil {
		return nil, err
	}
	totalUsed, err := strconv.ParseFloat(strings.TrimSpace(cr.TotalUsed), 64)
	if err != nil {
		return nil, err
	}
	return &struct {
		Balance   float64
		TotalUsed float64
	}{Balance: balance, TotalUsed: totalUsed}, nil
}

func pollOne(cfg Config, state *AppState, id string) error {
	state.mu.Lock()
	k, ok := state.Keys[id]
	if !ok {
		state.mu.Unlock()
		return errors.New("key not found")
	}
	roll30DayWindow(k)
	wasPaused := k.Paused
	apiKey := k.APIKey
	prev := k.LastUsedTotal
	state.mu.Unlock()

	credits, err := fetchCredits(cfg.GatewayBaseURL, apiKey)
	now := time.Now().UTC()

	state.mu.Lock()
	defer state.mu.Unlock()
	k2, ok := state.Keys[id]
	if !ok {
		return errors.New("key not found")
	}
	roll30DayWindow(k2)
	k2.LastPollAt = now
	k2.UpdatedAt = now
	if err != nil {
		k2.LastErr = err.Error()
		statusCode := statusCodeFromError(err.Error())
		if retryStatusEnabled(statusCode, cfg.RefreshScrapStatusCodes) {
			k2.Scrapped = true
			k2.ScrappedErr = err.Error()
			k2.ScrappedAt = now
			k2.Paused = true
			if state.StickyMode && state.StickyKeyID == id {
				state.StickyKeyID = ""
			}
			state.renumberKeys()
		} else if retryStatusEnabled(statusCode, cfg.RefreshCooldownStatusCodes) {
			k2.Paused = true
			if state.StickyMode && state.StickyKeyID == id {
				state.StickyKeyID = ""
			}
		}
		_ = state.save()
		return err
	}
	k2.LastErr = ""
	k2.RequestCount++
	k2.LastBalance = credits.Balance
	k2.LastUsedTotal = credits.TotalUsed
	if !wasPaused {
		if credits.TotalUsed > prev {
			k2.MonthlySpentUSD += credits.TotalUsed - prev
		}
		if k2.MonthlySpentUSD >= state.Cooldown {
			k2.Paused = true
			if state.StickyMode && state.StickyKeyID == id {
				state.StickyKeyID = ""
			}
		}
	}
	return state.save()
}

func statusCodeFromError(msg string) int {
	idx := strings.Index(msg, "status=")
	if idx < 0 {
		return 0
	}
	rest := msg[idx+len("status="):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil {
		return 0
	}
	return n
}

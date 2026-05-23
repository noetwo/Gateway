package app

import (
	"path/filepath"
	"testing"
	"time"
)

func testState(t *testing.T, keys map[string]*Key) *AppState {
	t.Helper()
	now := time.Now().UTC()
	for _, k := range keys {
		if k.CreatedAt.IsZero() {
			k.CreatedAt = now
		}
		if k.UpdatedAt.IsZero() {
			k.UpdatedAt = now
		}
	}
	return &AppState{
		filePath: filepath.Join(t.TempDir(), "state.json"),
		Cooldown: defaultCooldownUSD,
		Keys:     keys,
	}
}

func candidateIDs(cands []proxyCandidate) []string {
	ids := make([]string, 0, len(cands))
	for _, c := range cands {
		ids = append(ids, c.ID)
	}
	return ids
}

func requireIDs(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got ids %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got ids %v, want %v", got, want)
		}
	}
}

func TestNextProxyCandidatesPreferTeamBeforeHobby(t *testing.T) {
	state := testState(t, map[string]*Key{
		"01-hobby": {ID: "01-hobby", Tier: "hobby", APIKey: "vck_hobby_1"},
		"02-team":  {ID: "02-team", Tier: "team", APIKey: "vck_team_1"},
		"04-team":  {ID: "04-team", Tier: "team", APIKey: "vck_team_2"},
	})

	got := candidateIDs(state.nextProxyCandidates("openai/gpt-4.1"))
	requireIDs(t, got, []string{"02-team", "04-team"})
}

func TestNextProxyCandidatesOnlyRotatesTeamWhileTeamAvailable(t *testing.T) {
	state := testState(t, map[string]*Key{
		"01-team":  {ID: "01-team", Tier: "team", APIKey: "vck_team_1"},
		"02-team":  {ID: "02-team", Tier: "team", APIKey: "vck_team_2"},
		"03-hobby": {ID: "03-hobby", Tier: "hobby", APIKey: "vck_hobby_1"},
		"04-hobby": {ID: "04-hobby", Tier: "hobby", APIKey: "vck_hobby_2"},
	})

	_ = state.nextProxyCandidates("openai/gpt-4.1")
	got := candidateIDs(state.nextProxyCandidates("openai/gpt-4.1"))
	requireIDs(t, got, []string{"02-team", "01-team"})
}

func TestNextProxyCandidatesUsesHobbyWhenNoTeamAvailable(t *testing.T) {
	state := testState(t, map[string]*Key{
		"01-team":  {ID: "01-team", Tier: "team", APIKey: "vck_team_1", Paused: true},
		"02-hobby": {ID: "02-hobby", Tier: "hobby", APIKey: "vck_hobby_1"},
	})

	got := candidateIDs(state.nextProxyCandidates("openai/gpt-4.1"))
	requireIDs(t, got, []string{"02-hobby"})
}

func TestNextProxyCandidatesTreatsBlankAndCustomTierAsNonHobby(t *testing.T) {
	state := testState(t, map[string]*Key{
		"01-blank": {ID: "01-blank", Tier: "", APIKey: "vck_blank_1"},
		"02-pro":   {ID: "02-pro", Tier: "pro", APIKey: "vck_pro_1"},
		"03-hobby": {ID: "03-hobby", Tier: "hobby", APIKey: "vck_hobby_1"},
	})

	got := candidateIDs(state.nextProxyCandidates("openai/gpt-4.1"))
	requireIDs(t, got, []string{"01-blank", "02-pro"})
}

func TestNextProxyCandidatesUsesBlankTierForHobbyBlockedModel(t *testing.T) {
	state := testState(t, map[string]*Key{
		"01-blank": {ID: "01-blank", Tier: "", APIKey: "vck_blank_1"},
		"02-hobby": {ID: "02-hobby", Tier: "hobby", APIKey: "vck_hobby_1"},
	})
	state.HobbyBlocked = []string{"anthropic/claude-opus-*"}

	got := candidateIDs(state.nextProxyCandidates("anthropic/claude-opus-4.5"))
	requireIDs(t, got, []string{"01-blank"})
}

func TestNextProxyCandidatesCanPreferHobbyByDefault(t *testing.T) {
	state := testState(t, map[string]*Key{
		"01-team":  {ID: "01-team", Tier: "team", APIKey: "vck_team_1"},
		"02-hobby": {ID: "02-hobby", Tier: "hobby", APIKey: "vck_hobby_1"},
	})
	state.PreferredTier = "hobby"

	got := candidateIDs(state.nextProxyCandidates("openai/gpt-4.1"))
	requireIDs(t, got, []string{"02-hobby"})
}

func TestNextProxyCandidatesTeamPriorityRuleOverridesHobbyDefault(t *testing.T) {
	state := testState(t, map[string]*Key{
		"01-team":  {ID: "01-team", Tier: "team", APIKey: "vck_team_1"},
		"02-hobby": {ID: "02-hobby", Tier: "hobby", APIKey: "vck_hobby_1"},
	})
	state.PreferredTier = "hobby"
	state.TeamPriority = []string{"anthropic/claude-opus-*"}

	got := candidateIDs(state.nextProxyCandidates("anthropic/claude-opus-4.5"))
	requireIDs(t, got, []string{"01-team"})
}

func TestNextProxyCandidatesHobbyPriorityRuleOverridesTeamDefault(t *testing.T) {
	state := testState(t, map[string]*Key{
		"01-team":  {ID: "01-team", Tier: "team", APIKey: "vck_team_1"},
		"02-hobby": {ID: "02-hobby", Tier: "hobby", APIKey: "vck_hobby_1"},
	})
	state.HobbyPriority = []string{"anthropic/claude-haiku-*"}

	got := candidateIDs(state.nextProxyCandidates("anthropic/claude-haiku-4.5"))
	requireIDs(t, got, []string{"02-hobby"})
}

func TestNormalizeModelPatternsKeepsThinkingSuffix(t *testing.T) {
	got := normalizeModelPatterns([]string{"openai/gpt-5.5-xhigh"}, false)
	requireIDs(t, got, []string{"openai/gpt-5.5-xhigh"})
}

func TestNextProxyCandidatesHobbyPriorityCanTargetThinkingSuffix(t *testing.T) {
	state := testState(t, map[string]*Key{
		"01-team":  {ID: "01-team", Tier: "team", APIKey: "vck_team_1"},
		"02-hobby": {ID: "02-hobby", Tier: "hobby", APIKey: "vck_hobby_1"},
	})
	state.HobbyPriority = []string{"openai/gpt-5.5-xhigh"}

	got := candidateIDs(state.nextProxyCandidates("openai/gpt-5.5-xhigh"))
	requireIDs(t, got, []string{"02-hobby"})
}

func TestNextProxyCandidatesBasePriorityStillMatchesThinkingSuffix(t *testing.T) {
	state := testState(t, map[string]*Key{
		"01-team":  {ID: "01-team", Tier: "team", APIKey: "vck_team_1"},
		"02-hobby": {ID: "02-hobby", Tier: "hobby", APIKey: "vck_hobby_1"},
	})
	state.HobbyPriority = []string{"openai/gpt-5.5"}

	got := candidateIDs(state.nextProxyCandidates("openai/gpt-5.5-xhigh"))
	requireIDs(t, got, []string{"02-hobby"})
}

func TestNextProxyCandidatesStillBlocksHobbyForBlockedModels(t *testing.T) {
	state := testState(t, map[string]*Key{
		"01-hobby": {ID: "01-hobby", Tier: "hobby", APIKey: "vck_hobby_1"},
		"02-team":  {ID: "02-team", Tier: "team", APIKey: "vck_team_1"},
	})
	state.HobbyBlocked = []string{"anthropic/claude-opus-*"}

	got := candidateIDs(state.nextProxyCandidates("anthropic/claude-opus-4.5"))
	requireIDs(t, got, []string{"02-team"})
}

func TestNextProxyCandidatesHobbyBlockedOverridesHobbyPriority(t *testing.T) {
	state := testState(t, map[string]*Key{
		"01-hobby": {ID: "01-hobby", Tier: "hobby", APIKey: "vck_hobby_1"},
		"02-team":  {ID: "02-team", Tier: "team", APIKey: "vck_team_1"},
	})
	state.HobbyBlocked = []string{"anthropic/claude-opus-*"}
	state.HobbyPriority = []string{"anthropic/claude-opus-*"}

	got := candidateIDs(state.nextProxyCandidates("anthropic/claude-opus-4.5"))
	requireIDs(t, got, []string{"02-team"})
}

func TestNextProxyCandidatesReturnsNoneWhenOnlyHobbyBlocked(t *testing.T) {
	state := testState(t, map[string]*Key{
		"01-hobby": {ID: "01-hobby", Tier: "hobby", APIKey: "vck_hobby_1"},
	})
	state.HobbyBlocked = []string{"anthropic/claude-opus-*"}

	got := candidateIDs(state.nextProxyCandidates("anthropic/claude-opus-4.5"))
	requireIDs(t, got, nil)
}

func TestNextProxyCandidatesStickyHobbyDoesNotOverrideTeam(t *testing.T) {
	state := testState(t, map[string]*Key{
		"01-hobby": {ID: "01-hobby", Tier: "hobby", APIKey: "vck_hobby_1"},
		"02-team":  {ID: "02-team", Tier: "team", APIKey: "vck_team_1"},
	})
	state.StickyMode = true
	state.StickyKeyID = "01-hobby"

	got := candidateIDs(state.nextProxyCandidates("openai/gpt-4.1"))
	requireIDs(t, got, []string{"02-team"})
}

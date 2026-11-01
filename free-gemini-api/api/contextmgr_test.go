package api

import (
	"strings"
	"testing"
)

func msg(role, content string) OpenAIChatMessage {
	c := content
	return OpenAIChatMessage{Role: role, Content: &c}
}

func TestDedupeNeverRewritesUserOrFinalMessage(t *testing.T) {
	big := strings.Repeat("x", 500)
	msgs := []OpenAIChatMessage{
		msg("user", big),
		msg("user", big),
	}
	out, replaced, _ := DedupeRepeatedContent(msgs)
	if replaced != 0 {
		t.Fatalf("user messages must never be rewritten, replaced=%d", replaced)
	}
	if *out[0].Content != big || *out[1].Content != big {
		t.Fatalf("user content was modified")
	}
}

func TestDedupeReplacesEligibleDuplicate(t *testing.T) {
	big := strings.Repeat("tool output line ", 60)
	msgs := []OpenAIChatMessage{
		msg("assistant", big),
		msg("user", "now summarise it"),
		msg("assistant", big),
		msg("user", "and again"),
	}
	out, replaced, saved := DedupeRepeatedContent(msgs)
	if replaced != 1 {
		t.Fatalf("want 1 replacement, got %d", replaced)
	}
	if saved <= 0 {
		t.Fatalf("want positive bytes saved, got %d", saved)
	}
	if !strings.Contains(*out[2].Content, "identical to message 1") {
		t.Fatalf("expected back-reference, got %q", *out[2].Content)
	}
	if *out[3].Content != "and again" {
		t.Fatalf("final message must be untouched")
	}
}

func TestEvictKeepsSystemAndFinalTurn(t *testing.T) {
	msgs := []OpenAIChatMessage{msg("system", "be terse")}
	for i := 0; i < 20; i++ {
		msgs = append(msgs, msg("user", "q"), msg("assistant", "a"))
	}
	msgs = append(msgs, msg("user", "FINAL"))

	out, evicted := EvictOldTurns(msgs, 4)
	if evicted <= 0 {
		t.Fatalf("expected evictions, got %d", evicted)
	}
	if out[0].Role != "system" || *out[0].Content != "be terse" {
		t.Fatalf("leading system prompt must survive verbatim")
	}
	last := out[len(out)-1]
	if last.Role != "user" || *last.Content != "FINAL" {
		t.Fatalf("final turn must survive, got %q", *last.Content)
	}
	if len(out) >= len(msgs) {
		t.Fatalf("expected a shorter message list")
	}
}

func TestApplyContextPolicyUnderBudgetKeepsMessages(t *testing.T) {
	msgs := []OpenAIChatMessage{
		msg("system", "sys"),
		msg("user", "hello"),
		msg("assistant", "hi"),
		msg("user", "bye"),
	}
	out, st := ApplyContextPolicy(msgs, 100000, 10)
	if len(out) != len(msgs) {
		t.Fatalf("want %d messages, got %d", len(msgs), len(out))
	}
	if st.Truncated {
		t.Fatalf("must not truncate when under budget")
	}
	if st.InputMessages != 4 || st.OutputMessages != 4 {
		t.Fatalf("unexpected counts: %+v", st)
	}
}

func TestApplyContextPolicyTruncatesOverBudget(t *testing.T) {
	msgs := []OpenAIChatMessage{msg("system", "sys")}
	for i := 0; i < 40; i++ {
		msgs = append(msgs, msg("user", strings.Repeat("word ", 50)), msg("assistant", strings.Repeat("reply ", 50)))
	}
	msgs = append(msgs, msg("user", "FINAL"))

	out, st := ApplyContextPolicy(msgs, 200, 4)
	if !st.Truncated {
		t.Fatalf("expected truncation, state=%+v", st)
	}
	if len(out) >= len(msgs) {
		t.Fatalf("expected fewer messages")
	}
	if *out[len(out)-1].Content != "FINAL" {
		t.Fatalf("final turn lost")
	}
	if st.OutputTokens >= st.InputTokens {
		t.Fatalf("expected token reduction, in=%d out=%d", st.InputTokens, st.OutputTokens)
	}
}

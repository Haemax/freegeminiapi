package api

import "fmt"

// ContextState reports what the context policy did to one request.
type ContextState struct {
	InputMessages  int
	InputTokens    int
	OutputMessages int
	OutputTokens   int
	SavedTokens    int
	DedupedRepeats int
	EvictedTurns   int
	Truncated      bool
}

// CountTokensFor sums the BPE token count of every message body.
func CountTokensFor(messages []OpenAIChatMessage) int {
	total := 0
	for i := range messages {
		if messages[i].Content != nil {
			total += CountTokens(*messages[i].Content)
		}
	}
	return total
}

// dedupeEligible reports whether a message may be replaced by a reference.
// User messages are never rewritten: their wording is the instruction the
// model must act on. The final message is never rewritten because it is the
// live turn being answered.
func dedupeEligible(role string, idx, last int) bool {
	if idx == last {
		return false
	}
	switch role {
	case "assistant", "tool", "function":
		return true
	}
	return false
}

// DedupeRepeatedContent replaces an earlier message whose body is byte-identical
// to a later eligible message with a short back-reference. It returns the
// messages, the number of message bodies replaced, and the bytes saved.
func DedupeRepeatedContent(messages []OpenAIChatMessage) ([]OpenAIChatMessage, int, int) {
	last := len(messages) - 1
	saved := 0
	replaced := 0

	// seen maps an exact body to the last index at which it appeared.
	seen := make(map[string]int, len(messages))
	for i := 0; i <= last; i++ {
		if messages[i].Content == nil || *messages[i].Content == "" {
			continue
		}
		body := *messages[i].Content
		if prev, ok := seen[body]; ok && dedupeEligible(messages[i].Role, i, last) {
			ref := fmt.Sprintf("[identical to message %d, unchanged]", prev+1)
			if len(body) > len(ref) {
				saved += len(body) - len(ref)
				replaced++
				*messages[i].Content = ref
			}
		}
		// Record under the original body so later duplicates still match it.
		if _, ok := seen[body]; !ok {
			seen[body] = i
		}
	}

	return messages, replaced, saved
}

// EvictOldTurns keeps the system prompt, a summary stub, and the most recent
// turns, dropping whole turns from the middle. It never drops the final
// message, which is the turn being answered.
func EvictOldTurns(messages []OpenAIChatMessage, keepTurns int) ([]OpenAIChatMessage, int) {
	if keepTurns < 1 {
		keepTurns = 1
	}
	if len(messages) <= keepTurns+1 {
		return messages, 0
	}

	systemCount := 0
	for i := range messages {
		if messages[i].Role == "system" {
			systemCount++
		}
	}

	// Keep every leading system message verbatim.
	lead := 0
	for lead < len(messages) && messages[lead].Role == "system" {
		lead++
	}

	body := messages[lead:]
	if len(body) <= keepTurns {
		return messages, 0
	}

	keep := body[len(body)-keepTurns:]
	evicted := len(body) - len(keep)

	result := make([]OpenAIChatMessage, 0, lead+1+len(keep))
	result = append(result, messages[:lead]...)
	stub := fmt.Sprintf("[%d earlier messages omitted to fit the context budget]", evicted)
	result = append(result, OpenAIChatMessage{Role: "system", Content: &stub})
	result = append(result, keep...)

	_ = systemCount
	return result, evicted
}

// ApplyContextPolicy deduplicates repeated bodies and, only if the request is
// still over budget, evicts whole old turns. It is lossless with respect to the
// live turn: the final message and all user instructions are preserved.
func ApplyContextPolicy(messages []OpenAIChatMessage, maxTokens int, keepTurns int) ([]OpenAIChatMessage, ContextState) {
	st := ContextState{
		InputMessages: len(messages),
		InputTokens:   CountTokensFor(messages),
	}

	deduped, replaced, savedBytes := DedupeRepeatedContent(messages)
	st.DedupedRepeats = replaced
	st.SavedTokens = savedBytes / 4

	if maxTokens > 0 && CountTokensFor(deduped) > maxTokens {
		evicted, n := EvictOldTurns(deduped, keepTurns)
		st.EvictedTurns = n
		st.Truncated = n > 0
		deduped = evicted
	}

	st.OutputMessages = len(deduped)
	st.OutputTokens = CountTokensFor(deduped)
	return deduped, st
}

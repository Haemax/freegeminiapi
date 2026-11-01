package gemini

// ConvKey builds a session-scoped conversation key.
// Conversation state must be keyed by (session, model) rather than by model alone:
// a GeminiClient is shared by every caller routed to one worker, so a model-only
// key makes two independent users continue each other's chat.
func ConvKey(sessionID, modelID string) string {
	return sessionID + "\x00" + modelID
}

// GetConvState returns the stored upstream conversation identifiers for this
// session and model. A miss returns empty strings, which means "start a new
// conversation" — it never falls back to another session's or another model's
// conversation.
func (c *GeminiClient) GetConvState(sessionID, modelID string) (string, string, string) {
	c.convMu.Lock()
	defer c.convMu.Unlock()

	if c.convStates == nil {
		return "", "", ""
	}
	if st, ok := c.convStates[ConvKey(sessionID, modelID)]; ok && st != nil {
		return st.ConversationID, st.ResponseID, st.ChoiceID
	}
	return "", "", ""
}

// SetConvState records the upstream conversation identifiers for this session
// and model.
func (c *GeminiClient) SetConvState(sessionID, modelID, convID, respID, choiceID string) {
	c.convMu.Lock()
	defer c.convMu.Unlock()

	if c.convStates == nil {
		c.convStates = make(map[string]*ModelConversationState)
	}
	c.convStates[ConvKey(sessionID, modelID)] = &ModelConversationState{
		ConversationID: convID,
		ResponseID:     respID,
		ChoiceID:       choiceID,
	}
}

// ClearConvState forgets the conversation for this session and model, forcing
// the next request to start a new upstream chat.
func (c *GeminiClient) ClearConvState(sessionID, modelID string) {
	c.convMu.Lock()
	defer c.convMu.Unlock()

	if c.convStates == nil {
		return
	}
	delete(c.convStates, ConvKey(sessionID, modelID))
}

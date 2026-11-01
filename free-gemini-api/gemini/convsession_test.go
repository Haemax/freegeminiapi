package gemini

import "testing"

func TestConvStateSessionIsolation(t *testing.T) {
	c := &GeminiClient{}
	c.SetConvState("sessA", "gemini-3.8-flash", "convA", "respA", "chA")
	c.SetConvState("sessB", "gemini-3.8-flash", "convB", "respB", "chB")

	if id, _, _ := c.GetConvState("sessA", "gemini-3.8-flash"); id != "convA" {
		t.Fatalf("sessA got %q, want convA", id)
	}
	if id, _, _ := c.GetConvState("sessB", "gemini-3.8-flash"); id != "convB" {
		t.Fatalf("sessB got %q, want convB", id)
	}
}

func TestConvStateNoCrossModelBleed(t *testing.T) {
	c := &GeminiClient{}
	c.SetConvState("s1", "gemini-3.8-flash", "flashConv", "r", "ch")

	// A model with no state must start fresh, not inherit flash's conversation.
	if id, _, _ := c.GetConvState("s1", "gemini-3.1-pro"); id != "" {
		t.Fatalf("expected empty (new conversation), got inherited %q", id)
	}
}

func TestConvStateClear(t *testing.T) {
	c := &GeminiClient{}
	c.SetConvState("s", "m", "x", "y", "z")
	c.ClearConvState("s", "m")
	if id, _, _ := c.GetConvState("s", "m"); id != "" {
		t.Fatalf("expected cleared state, got %q", id)
	}
}

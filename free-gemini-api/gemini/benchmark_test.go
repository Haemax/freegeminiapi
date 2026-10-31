package gemini

import (
	"testing"
	"time"
)

// BenchmarkAllModels_GuestMode benchmarks all registered models in guest mode
func BenchmarkAllModels_GuestMode(b *testing.B) {
	client, err := NewClient("guest")
	if err != nil {
		b.Fatalf("Failed to create guest client: %v", err)
	}
	if err := client.InitSession(); err != nil {
		b.Logf("Session init warning: %v", err)
	}

	models := GetModelRegistry().GetAll()
	b.Logf("Benchmarking %d models in guest mode", len(models))

	for _, modelCfg := range models {
		b.Run(modelCfg.ID, func(b *testing.B) {
			if modelCfg.ThinkingLevel > 0 {
				b.Logf("Skipping thinking model: %s (level: %d)", modelCfg.ID, modelCfg.ThinkingLevel)
				return
			}

			prompt := "Write a hello world program in Go."
			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				start := time.Now()
				resp, err := client.AskModel(prompt, modelCfg.ID)
				elapsed := time.Since(start)

				if err != nil {
					b.Logf("Model %s error: %v", modelCfg.ID, err)
					continue
				}
				if resp == nil || resp.Text == "" {
					b.Logf("Model %s returned empty response", modelCfg.ID)
					continue
				}

				tokensPerSec := float64(len(resp.Text)/4) / elapsed.Seconds()
				b.ReportMetric(elapsed.Seconds(), "latency_sec")
				b.ReportMetric(tokensPerSec, "tokens_per_sec")
			}
		})
	}
}

// BenchmarkModel_Streaming benchmarks streaming response for a specific model
func BenchmarkModel_Streaming(b *testing.B) {
	client, err := NewClient("guest")
	if err != nil {
		b.Fatalf("Failed to create guest client: %v", err)
	}
	if err := client.InitSession(); err != nil {
		b.Logf("Session init warning: %v", err)
	}

	models := GetModelRegistry().GetAll()
	var modelCfg *ModelConfig
	for _, m := range models {
		if m.ThinkingLevel == 0 {
			modelCfg = &m
			break
		}
	}
	if modelCfg == nil {
		b.Skip("No non-thinking model available")
	}

	prompt := "Explain how HTTP/3 QUIC works in simple terms."
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		start := time.Now()
		var firstChunkTime time.Duration
		chunkCount := 0

		_, err := client.AskStreamModel(prompt, modelCfg.ID, func(chunk string) {
			if chunkCount == 0 {
				firstChunkTime = time.Since(start)
			}
			chunkCount++
		})
		elapsed := time.Since(start)

		if err != nil {
			b.Logf("Streaming error: %v", err)
			continue
		}

		b.ReportMetric(firstChunkTime.Seconds(), "ttft_sec")
		b.ReportMetric(elapsed.Seconds(), "total_sec")
		b.ReportMetric(float64(chunkCount)/elapsed.Seconds(), "chunks_per_sec")
	}
}

// BenchmarkSessionCacheHit benchmarks the zero-wait session cache
func BenchmarkSessionCacheHit(b *testing.B) {
	client, err := NewClient("guest")
	if err != nil {
		b.Fatalf("Failed to create guest client: %v", err)
	}
	if err := client.InitSession(); err != nil {
		b.Logf("Session init warning: %v", err)
	}
	client.persistSession()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		newClient, err := NewClient("guest")
		if err != nil {
			b.Fatalf("Failed to create client: %v", err)
		}
		_ = newClient.loadCachedSession()
	}
}

// BenchmarkModelRegistry_Resolve benchmarks model resolution
func BenchmarkModelRegistry_Resolve(b *testing.B) {
	reg := GetModelRegistry()
	models := reg.GetAll()
	modelNames := make([]string, len(models))
	for i, m := range models {
		modelNames[i] = m.ID
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		name := modelNames[i%len(modelNames)]
		_ = reg.Resolve(name)
	}
}

// BenchmarkArrangeOutput benchmarks the output arranger
func BenchmarkArrangeOutput(b *testing.B) {
	testCases := []string{
		"Simple response without special formatting.",
		"<thought>Let me think about this...</thought>Response after thinking.",
		"[TOOL] {name: test_tool, arguments: {arg: value}} [/TOOL] Response after tool call.",
		"Some text with <thought>reasoning</thought> and then  more text",
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		text := testCases[i%len(testCases)]
		_ = ArrangeGeminiOutput(text)
	}
}

// BenchmarkCookieHealthInspection benchmarks cookie health checking
func BenchmarkCookieHealthInspection(b *testing.B) {
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = InspectAccountCookieHealth()
	}
}

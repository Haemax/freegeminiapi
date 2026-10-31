package api

import (
	"context"
	"testing"
	"time"

	"goapi/gemini"
)

// BenchmarkChatCompletion_FreeModels benchmarks chat completions across free models
func BenchmarkChatCompletion_FreeModels(b *testing.B) {
	client, err := gemini.NewClient("guest")
	if err != nil {
		b.Fatalf("Failed to create guest client: %v", err)
	}
	if err := client.InitSession(); err != nil {
		b.Logf("Session init warning: %v", err)
	}

	models := gemini.GetModelRegistry().GetAll()
	var freeModels []gemini.ModelConfig
	for _, m := range models {
		if m.MinTier == "free" && m.ThinkingLevel == 0 {
			freeModels = append(freeModels, m)
		}
	}

	b.Logf("Benchmarking %d free models", len(freeModels))

	for _, modelCfg := range freeModels {
		b.Run(modelCfg.ID, func(b *testing.B) {
			prompt := "Write a short function to calculate fibonacci in Go."
			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				start := time.Now()
				resp, err := client.AskModel(prompt, modelCfg.ID)
				elapsed := time.Since(start)

				if err != nil {
					b.Logf("Error: %v", err)
					continue
				}
				if resp == nil || resp.Text == "" {
					continue
				}

				tokensPerSec := float64(len(resp.Text)/4) / elapsed.Seconds()
				b.ReportMetric(elapsed.Seconds(), "latency_sec")
				b.ReportMetric(tokensPerSec, "tokens_per_sec")
			}
		})
	}
}

// BenchmarkWorkerPool_Concurrency benchmarks worker pool under concurrent load
func BenchmarkWorkerPool_Concurrency(b *testing.B) {
	pool := GetWorkerPool()
	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			worker, err := pool.AcquireWorkerWithTier(ctx, "", "free")
			if err != nil {
				b.Fatalf("Failed to acquire worker: %v", err)
			}
			pool.ReleaseWorker(worker, nil)
		}
	})
}

// BenchmarkTokenCount benchmarks token counting
func BenchmarkTokenCount(b *testing.B) {
	texts := []string{
		"Short",
		"Medium length text with multiple words and punctuation.",
		"Very long text that simulates a typical LLM response. It contains many words, sentences, and various characters. The token estimation algorithm needs to process this efficiently while providing accurate token counts that approximate what the actual tokenizer would produce.",
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		text := texts[i%len(texts)]
		_ = EstimateTokens(text)
	}
}

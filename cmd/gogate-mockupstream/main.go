// Command gogate-mockupstream exposes the deterministic mock provider over an
// OpenAI-compatible HTTP API. It is used as a load-test baseline and as an
// optional upstream provider.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/yugrinovD/gogate/internal/provider"
	"github.com/yugrinovD/gogate/internal/provider/mock"
)

const defaultModels = "mock,mock-fast"

func main() {
	addr := env("MOCK_ADDR", ":8081")
	models := split(env("MOCK_MODELS", defaultModels))
	cfg := map[string]any{
		"latency_ms":        envInt("MOCK_LATENCY_MS", 0),
		"chunk_delay_ms":    envInt("MOCK_CHUNK_DELAY_MS", 0),
		"fail_before_first": envBool("MOCK_FAIL_BEFORE_FIRST", false),
		"fail_after_chunks": envInt("MOCK_FAIL_AFTER_CHUNKS", 0),
		"fail_rate":         envFloat("MOCK_FAIL_RATE", 0),
	}
	raw, _ := json.Marshal(cfg)
	mp, err := mock.New(provider.Config{Name: "mock-upstream", Models: models, RawConfig: raw})
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		type model struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			OwnedBy string `json:"owned_by"`
		}
		data := make([]model, 0, len(models))
		for _, m := range models {
			data = append(data, model{ID: m, Object: "model", OwnedBy: "mock-upstream"})
		}
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
	})
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		handleChat(w, r, mp)
	})

	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second}
	log.Printf("mock upstream listening on %s models=%v", addr, models)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

type request struct {
	Model       string             `json:"model"`
	Messages    []provider.Message `json:"messages"`
	Temperature *float64           `json:"temperature"`
	TopP        *float64           `json:"top_p"`
	MaxTokens   *int               `json:"max_tokens"`
	Seed        *int               `json:"seed"`
	Stream      bool               `json:"stream"`
	Stop        []string           `json:"stop"`
}

func handleChat(w http.ResponseWriter, r *http.Request, p provider.Provider) {
	var in request
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "invalid JSON"}})
		return
	}
	preq := provider.Request{
		Model: in.Model, Messages: in.Messages, Temperature: in.Temperature,
		TopP: in.TopP, MaxTokens: in.MaxTokens, Seed: in.Seed, Stream: in.Stream, Stop: in.Stop,
	}
	stream, err := p.Chat(r.Context(), preq)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]any{"message": err.Error(), "type": "upstream_error"}})
		return
	}
	defer func() { _ = stream.Close() }()

	if in.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for {
			chunk, err := stream.Next(r.Context())
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				writeSSE(w, map[string]any{"error": map[string]any{"message": err.Error(), "type": "upstream_error"}})
				break
			}
			out := chunkPayload(chunk, in.Model)
			writeSSE(w, out)
			if flusher != nil {
				flusher.Flush()
			}
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		return
	}

	var b strings.Builder
	var usage *provider.Usage
	finish := "stop"
	var id string
	created := time.Now().Unix()
	for {
		chunk, err := stream.Next(r.Context())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]any{"message": err.Error(), "type": "upstream_error"}})
			return
		}
		b.WriteString(chunk.Content)
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
		if chunk.FinishReason != "" {
			finish = chunk.FinishReason
		}
		if chunk.ID != "" {
			id = chunk.ID
		}
	}
	if usage == nil {
		usage = &provider.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}
	}
	if id == "" {
		id = fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": id, "object": "chat.completion", "created": created, "model": in.Model,
		"choices": []any{map[string]any{
			"index": 0, "message": map[string]any{"role": "assistant", "content": b.String()},
			"finish_reason": finish,
		}},
		"usage": map[string]any{
			"prompt_tokens": usage.PromptTokens, "completion_tokens": usage.CompletionTokens, "total_tokens": usage.TotalTokens,
		},
	})
}

func chunkPayload(c provider.Chunk, fallbackModel string) map[string]any {
	model := c.Model
	if model == "" {
		model = fallbackModel
	}
	delta := map[string]any{"content": c.Content}
	if c.Role != "" {
		delta["role"] = c.Role
	}
	choice := map[string]any{"index": c.Index, "delta": delta, "finish_reason": nil}
	if c.FinishReason != "" {
		choice["finish_reason"] = c.FinishReason
	}
	out := map[string]any{
		"id": c.ID, "object": "chat.completion.chunk", "created": c.Created, "model": model,
		"choices": []any{choice},
	}
	if c.Usage != nil {
		out["usage"] = map[string]any{
			"prompt_tokens": c.Usage.PromptTokens, "completion_tokens": c.Usage.CompletionTokens, "total_tokens": c.Usage.TotalTokens,
		}
	}
	return out
}

func writeSSE(w io.Writer, v any) {
	b, _ := json.Marshal(v)
	_, _ = w.Write([]byte("data: "))
	_, _ = w.Write(b)
	_, _ = io.WriteString(w, "\n\n")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v, err := strconv.Atoi(env(key, ""))
	if err != nil {
		return def
	}
	return v
}

func envFloat(key string, def float64) float64 {
	v, err := strconv.ParseFloat(env(key, ""), 64)
	if err != nil {
		return def
	}
	return v
}

func envBool(key string, def bool) bool {
	v, err := strconv.ParseBool(env(key, ""))
	if err != nil {
		return def
	}
	return v
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

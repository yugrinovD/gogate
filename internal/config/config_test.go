package config

import "testing"

func TestLoadRequiresPepper(t *testing.T) {
	t.Setenv("GOGATE_API_KEY_PEPPER", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected error without pepper")
	}
}

func TestLoadMinimal(t *testing.T) {
	t.Setenv("GOGATE_API_KEY_PEPPER", "0123456789abcdef")
	t.Setenv("GOGATE_ADMIN_TOKEN", "admin")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AdminEnabled() {
		t.Fatal("admin should be enabled")
	}
	if cfg.MaxOutputTokens <= 0 || cfg.RequestTimeout <= 0 {
		t.Fatal("defaults not applied")
	}
}

func TestValidateRejectsSharedListeners(t *testing.T) {
	t.Setenv("GOGATE_API_KEY_PEPPER", "0123456789abcdef")
	t.Setenv("GOGATE_ADMIN_TOKEN", "admin")
	t.Setenv("GOGATE_PUBLIC_ADDR", ":9000")
	t.Setenv("GOGATE_ADMIN_ADDR", ":9000")
	if _, err := Load(); err == nil {
		t.Fatal("expected shared listener error")
	}
}

func TestLoadRejectsMalformedValues(t *testing.T) {
	t.Setenv("GOGATE_API_KEY_PEPPER", "0123456789abcdef")
	t.Setenv("GOGATE_CACHE_ENABLED", "maybe")
	if _, err := Load(); err == nil {
		t.Fatal("expected malformed bool error")
	}
}

func TestLoadRejectsNegativeInflight(t *testing.T) {
	t.Setenv("GOGATE_API_KEY_PEPPER", "0123456789abcdef")
	t.Setenv("GOGATE_MAX_INFLIGHT", "-1")
	if _, err := Load(); err == nil {
		t.Fatal("expected max inflight validation error")
	}
}

func TestPrivateUpstreamsDeniedByDefault(t *testing.T) {
	t.Setenv("GOGATE_API_KEY_PEPPER", "0123456789abcdef")
	t.Setenv("GOGATE_ALLOW_PRIVATE_UPSTREAMS", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AllowPrivateUpstream {
		t.Fatal("private upstreams must be denied by default")
	}
}

func TestEmbeddingProviderValidation(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		ollama   string
		openai   string
		prod     bool
		wantErr  bool
	}{
		{"mock dev", "mock", "", "", false, false},
		{"mock prod", "mock", "", "", true, true},
		{"ollama with url", "ollama", "http://ollama:11434", "", false, false},
		{"ollama without url", "ollama", "", "", false, true},
		{"openai with url", "openai", "", "https://api.openai.com/v1", false, false},
		{"openai without url", "openai", "", "", false, true},
		{"unknown", "bogus", "", "", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GOGATE_API_KEY_PEPPER", "0123456789abcdef")
			t.Setenv("GOGATE_SEMANTIC_CACHE_ENABLED", "true")
			t.Setenv("GOGATE_EMBEDDING_PROVIDER", tc.provider)
			t.Setenv("GOGATE_OLLAMA_BASE_URL", tc.ollama)
			t.Setenv("GOGATE_OPENAI_BASE_URL", tc.openai)
			if tc.prod {
				t.Setenv("GOGATE_ENV", "prod")
			} else {
				t.Setenv("GOGATE_ENV", "dev")
			}
			_, err := Load()
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestMaxAPIKeyBytesCoversGeneratedKeys(t *testing.T) {
	t.Setenv("GOGATE_API_KEY_PEPPER", "0123456789abcdef")
	t.Setenv("GOGATE_ENV", "dev")
	t.Setenv("GOGATE_MAX_API_KEY_BYTES", "64")
	if _, err := Load(); err == nil {
		t.Fatal("64-byte limit must be rejected")
	}
	t.Setenv("GOGATE_MAX_API_KEY_BYTES", "128")
	if _, err := Load(); err != nil {
		t.Fatalf("128-byte limit should be valid: %v", err)
	}
}

func TestControlReloadIntervalCanDisable(t *testing.T) {
	t.Setenv("GOGATE_API_KEY_PEPPER", "0123456789abcdef")
	t.Setenv("GOGATE_CONTROL_RELOAD_INTERVAL", "0")
	if _, err := Load(); err != nil {
		t.Fatalf("0 should disable periodic reload: %v", err)
	}
	t.Setenv("GOGATE_CONTROL_RELOAD_INTERVAL", "-1s")
	if _, err := Load(); err == nil {
		t.Fatal("negative interval must be rejected")
	}
}

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

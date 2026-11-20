package security

import (
	"strings"
	"testing"
)

func TestGenerateAndParseKey(t *testing.T) {
	mat, err := GenerateKey("dev", "pepper-value-at-least-16")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(mat.Plaintext, "gk_dev_") {
		t.Fatalf("unexpected key format: %s", mat.Plaintext)
	}
	prefix, secret, err := ParseKey(mat.Plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if prefix != mat.Prefix {
		t.Fatalf("prefix mismatch: %q != %q", prefix, mat.Prefix)
	}
	if !Verify(mat.Hash, HashSecret("pepper-value-at-least-16", secret)) {
		t.Fatal("hash should verify")
	}
	if Verify(mat.Hash, HashSecret("wrong-pepper-value-16", secret)) {
		t.Fatal("hash should not verify with a different pepper")
	}
}

func TestParseKeyRejectsMalformed(t *testing.T) {
	for _, key := range []string{"", "nope", "gk_", "gk_dev", "gk_dev_only"} {
		if _, _, err := ParseKey(key); err == nil {
			t.Fatalf("expected error for %q", key)
		}
	}
}

func TestGeneratedKeysAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		mat, err := GenerateKey("test", "pepper-value-at-least-16")
		if err != nil {
			t.Fatal(err)
		}
		if seen[mat.Plaintext] {
			t.Fatal("duplicate key generated")
		}
		seen[mat.Plaintext] = true
	}
}

func TestParseKeyLimited(t *testing.T) {
	mat, err := GenerateKey("test", "pepper-value-at-least-16")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ParseKeyLimited(mat.Plaintext, 32); err == nil {
		t.Fatal("expected key length rejection")
	}
	if _, _, err := ParseKeyLimited(mat.Plaintext, 256); err != nil {
		t.Fatal(err)
	}
}

func TestValidateEnvName(t *testing.T) {
	if err := ValidateEnvName("GOGATE_UPSTREAM_OPENAI_KEY", "GOGATE_UPSTREAM_"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"GOGATE_API_KEY_PEPPER", "AWS_SECRET_ACCESS_KEY", "GOGATE_UPSTREAM_bad-name"} {
		if err := ValidateEnvName(name, "GOGATE_UPSTREAM_"); err == nil {
			t.Fatalf("expected rejection for %s", name)
		}
	}
}

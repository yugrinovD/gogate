package security

import "testing"

func BenchmarkHashSecret(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = HashSecret("pepper-value-at-least-16", "0123456789abcdef0123456789abcdef")
	}
}

func BenchmarkGenerateKey(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := GenerateKey("dev", "pepper-value-at-least-16"); err != nil {
			b.Fatal(err)
		}
	}
}

package security

import "testing"

func TestValidateUpstreamURL(t *testing.T) {
	cases := []struct {
		name         string
		url          string
		allowPrivate bool
		wantErr      bool
	}{
		{"public ip", "http://192.0.2.10:8080/v1", false, false},
		{"localhost rejected", "http://127.0.0.1:11434", false, true},
		{"localhost allowed", "http://127.0.0.1:11434", true, false},
		{"metadata always rejected", "http://169.254.169.254/latest", true, true},
		{"metadata host rejected", "http://metadata.google.internal", true, true},
		{"scheme rejected", "ftp://192.0.2.10", true, true},
		{"credentials rejected", "http://user:pass@192.0.2.10", true, true},
		{"empty rejected", "", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateUpstreamURL(tc.url, tc.allowPrivate)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

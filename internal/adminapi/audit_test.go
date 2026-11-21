package adminapi

import (
	"testing"
	"time"
)

func TestParseTimeQuery(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		endOfDay bool
		want     string
		wantErr  bool
	}{
		{name: "empty", raw: "", want: ""},
		{name: "date from", raw: "2026-11-20", want: "2026-11-20T00:00:00Z"},
		{name: "date to inclusive", raw: "2026-11-20", endOfDay: true, want: "2026-11-21T00:00:00Z"},
		{name: "rfc3339", raw: "2026-11-20T12:30:00+03:00", want: "2026-11-20T09:30:00Z"},
		{name: "invalid", raw: "20.11.2026", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseTimeQuery(test.raw, test.endOfDay)
			if test.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if test.want == "" {
				if got != nil {
					t.Fatalf("expected nil, got %v", got)
				}
				return
			}
			if got == nil || got.Format(time.RFC3339) != test.want {
				t.Fatalf("got %v, want %s", got, test.want)
			}
		})
	}
}

package client

import "testing"

func TestMeetsMinSeverity(t *testing.T) {
	tests := []struct {
		alertSev string
		minSev   string
		want     bool
	}{
		{"critical", "critical", true},
		{"critical", "high", true},
		{"critical", "medium", true},
		{"critical", "low", true},
		{"high", "critical", false},
		{"high", "high", true},
		{"high", "medium", true},
		{"high", "low", true},
		{"medium", "critical", false},
		{"medium", "high", false},
		{"medium", "medium", true},
		{"medium", "low", true},
		{"low", "critical", false},
		{"low", "high", false},
		{"low", "medium", false},
		{"low", "low", true},
		{"", "high", false},
		{"high", "", true},
		{"unknown", "high", false},
	}

	for _, tt := range tests {
		t.Run(tt.alertSev+"_vs_"+tt.minSev, func(t *testing.T) {
			got := meetsMinSeverity(tt.alertSev, tt.minSev)
			if got != tt.want {
				t.Errorf("meetsMinSeverity(%q, %q) = %v, want %v",
					tt.alertSev, tt.minSev, got, tt.want)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{"short", "short", 10, "short"},
		{"exact", "exactly10!", 10, "exactly10!"},
		{"long", "this is too long", 10, "this is..."},
		{"exact_boundary", "abc", 3, "abc"},
		{"over_boundary", "abcd", 3, "abc"},
		{"empty", "", 5, ""},
		{"unicode_under", "héllo", 10, "héllo"},
		{"unicode_truncate", "日本語のテキスト", 5, "日本..."},
		{"emoji", "🔥🔥🔥🔥🔥", 4, "🔥..."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncate(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncate(%q, %d) = %q, want %q",
					tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}

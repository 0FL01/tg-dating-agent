package utils

import "testing"

func TestTruncate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		s    string
		max  int
		want string
	}{
		{
			name: "no truncation",
			s:    "hello",
			max:  5,
			want: "hello",
		},
		{
			name: "ascii truncation",
			s:    "hello world",
			max:  5,
			want: "hello...",
		},
		{
			name: "cyrillic truncation",
			s:    "привет мир",
			max:  6,
			want: "привет...",
		},
		{
			name: "emoji truncation",
			s:    "🙂🙂🙂🙂",
			max:  3,
			want: "🙂🙂🙂...",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := Truncate(tt.s, tt.max)
			if got != tt.want {
				t.Fatalf("Truncate(%q, %d) = %q, want %q", tt.s, tt.max, got, tt.want)
			}
		})
	}
}

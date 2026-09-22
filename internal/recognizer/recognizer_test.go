package recognizer

import "testing"

func TestEmailFind(t *testing.T) {
	r := EmailRecognizer{}
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "single", in: "contact me at a.b@example.com please", want: []string{"a.b@example.com"}},
		{name: "multiple", in: "x@y.io and z@w.co", want: []string{"x@y.io", "z@w.co"}},
		{name: "none", in: "no email here", want: nil},
		{name: "cyrillic", in: "пишите на почту", want: nil},
		{name: "emoji", in: "привет 👋 a@b.ru", want: []string{"a@b.ru"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frags := r.Find(tt.in)
			got := make([]string, 0, len(frags))
			for _, f := range frags {
				if f.Type != Email {
					t.Fatalf("type = %q, want %q", f.Type, Email)
				}
				got = append(got, tt.in[f.Start:f.End])
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestEmailByteOffsets(t *testing.T) {
	r := EmailRecognizer{}
	// Cyrillic prefix shifts byte offsets relative to rune offsets.
	in := "почта: a@b.ru"
	frags := r.Find(in)
	if len(frags) != 1 {
		t.Fatalf("got %d fragments, want 1", len(frags))
	}
	f := frags[0]
	if got := in[f.Start:f.End]; got != "a@b.ru" {
		t.Fatalf("fragment = %q, want a@b.ru", got)
	}
	if f.Start != len("почта: ") {
		t.Fatalf("start = %d, want %d", f.Start, len("почта: "))
	}
}

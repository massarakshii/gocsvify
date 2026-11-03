package str

import (
	"regexp"
	"testing"
)

func TestOnlyNum(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want bool
	}{
		{"0", true},
		{"12345", true},
		{"007", true},
		{"", true}, // пустая строка считается числом - вызывающий код сам проверяет на пустоту
		{"-1", false},
		{"1.5", false},
		{"1e5", false},
		{" 1", false},
		{"1 ", false},
		{"abc", false},
		{"١٢٣", false}, // арабско-индийские цифры - не ASCII
		{"１２", false},  // полноширинные цифры
		{"1\n", false},
		{"../1", false},
	}
	for _, tt := range tests {
		if got := OnlyNum(tt.in); got != tt.want {
			t.Errorf("OnlyNum(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestGenerateUUIDFormat(t *testing.T) {
	t.Parallel()

	for range 1000 {
		u, err := GenerateUUID()
		if err != nil {
			t.Fatalf("GenerateUUID: %v", err)
		}
		if !uuidRe.MatchString(u) {
			t.Fatalf("%q is not a valid v4 UUID", u)
		}
	}
}

func TestGenerateUUIDUnique(t *testing.T) {
	t.Parallel()

	seen := make(map[string]struct{}, 10000)
	for range 10000 {
		u, _ := GenerateUUID()
		if _, dup := seen[u]; dup {
			t.Fatalf("duplicate UUID %s", u)
		}
		seen[u] = struct{}{}
	}
}

package store

import (
	"regexp"
	"testing"
)

func TestNewMessage(t *testing.T) {
	t.Parallel()

	m, err := NewMessage("отчёт.xlsx")
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	if m.Filename != "отчёт.xlsx" || m.Status != StatusNew || m.Message != "" {
		t.Errorf("unexpected message: %+v", m)
	}
	if !regexp.MustCompile(`^[0-9a-f-]{36}$`).MatchString(m.Key) {
		t.Errorf("key %q does not look like a UUID", m.Key)
	}

	m2, _ := NewMessage("отчёт.xlsx")
	if m.Key == m2.Key {
		t.Error("keys must be unique")
	}
}

func TestNewMessageWithKey(t *testing.T) {
	t.Parallel()

	m := NewMessageWithKey("book.xlsx", "book")
	if m.Key != "book" || m.Filename != "book.xlsx" || m.Status != StatusNew {
		t.Errorf("unexpected message: %+v", m)
	}
}

func TestUpdateStatus(t *testing.T) {
	t.Parallel()

	m := NewMessageWithKey("f.xls", "f")
	for _, st := range []string{StatusProcessing, StatusError, StatusDone, StatusNew} {
		m.UpdateStatus(st, "msg "+st)
		if m.Status != st || m.Message != "msg "+st {
			t.Fatalf("after UpdateStatus(%q): %+v", st, m)
		}
	}
	if m.Key != "f" || m.Filename != "f.xls" {
		t.Errorf("UpdateStatus must not touch key/filename: %+v", m)
	}
}

func TestStatusConstantsAreDistinct(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}
	for _, s := range []string{StatusNew, StatusProcessing, StatusError, StatusDone} {
		if s == "" || seen[s] {
			t.Fatalf("status %q is empty or duplicated", s)
		}
		seen[s] = true
	}
}

package converters

import (
	"path/filepath"
	"testing"
	"time"

	"gocsvify/store"

	"go.etcd.io/bbolt"
)

func openDB(t *testing.T) (*bbolt.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := bbolt.Open(path, 0o600, &bbolt.Options{Timeout: time.Second})
	if err != nil {
		t.Fatalf("bbolt.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, path
}

func TestMessageStoreSaveGet(t *testing.T) {
	t.Parallel()

	db, _ := openDB(t)
	ms := NewMessageStoreBolt(db)

	want := &store.Message{Key: "k1", Filename: "отчёт 2024.xlsx", Status: store.StatusDone, Message: "готово"}
	if err := ms.SaveMessage(want); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}
	got, err := ms.GetMessage("k1")
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if got == nil || *got != *want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestMessageStoreGetUnknownReturnsNil(t *testing.T) {
	t.Parallel()

	db, _ := openDB(t)
	ms := NewMessageStoreBolt(db)

	// Бакета ещё нет.
	if m, err := ms.GetMessage("nope"); err != nil || m != nil {
		t.Fatalf("empty db: got %+v, %v; want nil, nil", m, err)
	}
	// Бакет есть, ключа нет.
	_ = ms.SaveMessage(store.NewMessageWithKey("a.xls", "a"))
	if m, err := ms.GetMessage("nope"); err != nil || m != nil {
		t.Fatalf("missing key: got %+v, %v; want nil, nil", m, err)
	}
}

func TestMessageStoreEmptyKey(t *testing.T) {
	t.Parallel()

	db, _ := openDB(t)
	ms := NewMessageStoreBolt(db)

	if _, err := ms.GetMessage(""); err == nil {
		t.Error("GetMessage(\"\") must fail")
	}
	if err := ms.SaveMessage(&store.Message{Filename: "x"}); err == nil {
		t.Error("SaveMessage with empty key must fail")
	}
}

func TestMessageStoreOverwrite(t *testing.T) {
	t.Parallel()

	db, _ := openDB(t)
	ms := NewMessageStoreBolt(db)

	m := store.NewMessageWithKey("a.xlsx", "a")
	_ = ms.SaveMessage(m)
	m.UpdateStatus(store.StatusError, "boom")
	if err := ms.SaveMessage(m); err != nil {
		t.Fatal(err)
	}
	got, _ := ms.GetMessage("a")
	if got.Status != store.StatusError || got.Message != "boom" {
		t.Errorf("update not persisted: %+v", got)
	}
}

func TestMessageStoreDelete(t *testing.T) {
	t.Parallel()

	db, _ := openDB(t)
	ms := NewMessageStoreBolt(db)

	// Удаление несуществующего ключа и в пустой базе - не ошибка.
	if err := ms.DeleteMessage("ghost"); err != nil {
		t.Fatalf("delete from empty db: %v", err)
	}
	_ = ms.SaveMessage(store.NewMessageWithKey("a.xlsx", "a"))
	_ = ms.SaveMessage(store.NewMessageWithKey("b.xlsx", "b"))
	if err := ms.DeleteMessage("a"); err != nil {
		t.Fatal(err)
	}
	if m, _ := ms.GetMessage("a"); m != nil {
		t.Errorf("a must be deleted, got %+v", m)
	}
	if m, _ := ms.GetMessage("b"); m == nil {
		t.Error("b must stay")
	}
}

func TestMessageStorePersistsAcrossReopen(t *testing.T) {
	t.Parallel()

	db, path := openDB(t)
	ms := NewMessageStoreBolt(db)
	_ = ms.SaveMessage(store.NewMessageWithKey("a.xlsx", "a"))
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db2, err := bbolt.Open(path, 0o600, &bbolt.Options{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	got, err := NewMessageStoreBolt(db2).GetMessage("a")
	if err != nil || got == nil || got.Filename != "a.xlsx" {
		t.Fatalf("after reopen: %+v, %v", got, err)
	}
}

func TestMessageStoreClosedDB(t *testing.T) {
	t.Parallel()

	db, _ := openDB(t)
	ms := NewMessageStoreBolt(db)
	_ = db.Close()

	if _, err := ms.GetMessage("a"); err == nil {
		t.Error("GetMessage on closed db must fail")
	}
	if err := ms.SaveMessage(store.NewMessageWithKey("a.xlsx", "a")); err == nil {
		t.Error("SaveMessage on closed db must fail")
	}
	if err := ms.DeleteMessage("a"); err == nil {
		t.Error("DeleteMessage on closed db must fail")
	}
}

func TestMessageStoreCorruptedValue(t *testing.T) {
	t.Parallel()

	db, _ := openDB(t)
	ms := NewMessageStoreBolt(db)
	err := db.Update(func(tx *bbolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte("Message"))
		if err != nil {
			return err
		}
		return b.Put([]byte("bad"), []byte("{not json"))
	})
	if err != nil {
		t.Fatal(err)
	}
	if m, err := ms.GetMessage("bad"); err == nil {
		t.Fatalf("expected decode error, got %+v", m)
	}
}

func TestMessageStoreConcurrent(t *testing.T) {
	t.Parallel()

	db, _ := openDB(t)
	ms := NewMessageStoreBolt(db)

	done := make(chan struct{})
	for i := range 8 {
		go func() {
			defer func() { done <- struct{}{} }()
			key := string(rune('a' + i))
			for range 50 {
				m := store.NewMessageWithKey(key+".xlsx", key)
				_ = ms.SaveMessage(m)
				_, _ = ms.GetMessage(key)
			}
		}()
	}
	for range 8 {
		<-done
	}
	for i := range 8 {
		key := string(rune('a' + i))
		if m, err := ms.GetMessage(key); err != nil || m == nil {
			t.Errorf("%s: %+v, %v", key, m, err)
		}
	}
}

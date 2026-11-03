package converters

import (
	"encoding/json/v2"
	"fmt"
	"xls_converter/store"

	"go.etcd.io/bbolt"
)

type MessageStoreBolt struct {
	db *bbolt.DB
}

func NewMessageStoreBolt(db *bbolt.DB) *MessageStoreBolt {
	return &MessageStoreBolt{
		db: db,
	}
}

func (ms *MessageStoreBolt) GetMessage(key string) (*store.Message, error) {
	if key == "" {
		return nil, fmt.Errorf("key cannot be empty fn find")
	}
	mess := store.Message{}
	err := ms.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte("Message"))
		if b == nil {
			return nil
		}

		val := b.Get([]byte(key))
		if val == nil {
			return nil
		}
		if err := json.Unmarshal(val, &mess); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to find message: %v", err)
	}

	if mess.Key == "" {
		return nil, nil
	}

	return &mess, nil
}

func (ms *MessageStoreBolt) SaveMessage(message *store.Message) error {
	if message.Key == "" {
		return fmt.Errorf("key cannot be empty fn update")
	}
	err := ms.db.Update(func(tx *bbolt.Tx) error {
		b, _ := tx.CreateBucketIfNotExists([]byte("Message"))
		mess, err := json.Marshal(message)
		if err != nil {
			return err
		}

		return b.Put([]byte(message.Key), mess)
	})
	if err != nil {
		return err
	}

	return nil
}

func (ms *MessageStoreBolt) DeleteMessage(key string) error {
	err := ms.db.Update(func(tx *bbolt.Tx) error {
		b, _ := tx.CreateBucketIfNotExists([]byte("Message"))
		return b.Delete([]byte(key))
	})
	if err != nil {
		return err
	}

	return nil
}

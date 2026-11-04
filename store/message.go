package store

import (
	"fmt"
	"gocsvify/libs/str"
)

const (
	StatusNew        = "new"
	StatusProcessing = "processing"
	StatusError      = "error"
	StatusDone       = "done"
)

type Message struct {
	Key      string `json:"key"`
	Filename string `json:"filename"`
	Status   string `json:"status"`
	Message  string `json:"message"`
}

func NewMessageWithKey(Filename string, key string) *Message {
	mess := Message{
		Filename: Filename,
		Key:      key,
		Status:   StatusNew,
	}

	return &mess
}

func NewMessage(Filename string) (*Message, error) {
	var key string
	var err error

	key, err = str.GenerateUUID()
	if err != nil {
		return nil, fmt.Errorf("failed to generate UUID: %v", err)
	}

	mess := Message{
		Filename: Filename,
		Key:      key,
		Status:   StatusNew,
	}

	return &mess, nil
}

func (m *Message) UpdateStatus(status string, message string) {
	m.Status = status
	m.Message = message
}

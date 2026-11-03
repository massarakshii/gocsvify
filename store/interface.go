package store

type MessageStore interface {
	SaveMessage(message *Message) error
	GetMessage(key string) (*Message, error)
	DeleteMessage(key string) error
}

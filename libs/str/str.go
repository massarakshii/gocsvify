package str

import (
	"crypto/rand"
	"fmt"
)

func OnlyNum(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func GenerateUUID() (string, error) {
	uuid := make([]byte, 16)
	_, err := rand.Read(uuid)
	if err != nil {
		return "", err
	}

	// Установка версии 4 (биты 4-7 в 6-м байте равны 0100)
	uuid[6] = (uuid[6] & 0x0f) | 0x40
	// Установка варианта RFC 4122 (биты 6-7 в 8-м байте равны 10)
	uuid[8] = (uuid[8] & 0x3f) | 0x80

	// Форматирование в стандартный строковый вид UUID
	return fmt.Sprintf("%x-%x-%x-%x-%x",
		uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:16]), nil
}

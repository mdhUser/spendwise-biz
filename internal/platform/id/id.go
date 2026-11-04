package id

import (
	"github.com/google/uuid"
)

func New() uuid.UUID {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.New()
	}
	return id
}

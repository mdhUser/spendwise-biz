package app

import (
	"testing"
	"time"

	"github.com/mdhuser/spendwise-biz/internal/conversation/domain"
	"github.com/mdhuser/spendwise-biz/internal/platform/id"
)

func TestConversationCursorRoundTrip(t *testing.T) {
	conversation := domain.Conversation{ID: id.New(), UpdatedAt: time.Date(2026, 11, 6, 12, 0, 0, 123000000, time.UTC)}
	cursor := encodeCursor(conversation)
	parsedTime, parsedID, err := decodeCursor(cursor)
	if err != nil {
		t.Fatalf("decodeCursor: %v", err)
	}
	if !parsedTime.Equal(conversation.UpdatedAt) || parsedID != conversation.ID {
		t.Fatalf("round trip = %v/%v, want %v/%v", parsedTime, parsedID, conversation.UpdatedAt, conversation.ID)
	}
}

func TestDecodeCursorRejectsGarbage(t *testing.T) {
	for _, value := range []string{"not-base64", "Zm9v", "MjAyNi0xMS0wNg=="} {
		if _, _, err := decodeCursor(value); err == nil {
			t.Fatalf("cursor %q should be rejected", value)
		}
	}
}

func TestMessageValidation(t *testing.T) {
	service := &Service{}
	if _, _, err := service.CreateMessage(t.Context(), "owner", "not-a-uuid", "12345678", MessageInput{Content: "hi"}); err == nil {
		t.Fatal("invalid conversation id should fail")
	}
}

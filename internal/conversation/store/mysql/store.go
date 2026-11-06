package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/mdhuser/spendwise-biz/internal/conversation/domain"
	"github.com/mdhuser/spendwise-biz/internal/platform/db"
)

type Store struct{ db *sql.DB }

func New(database *sql.DB) *Store { return &Store{db: database} }

// WithTx runs fn in a short transaction; the app layer uses it to compose
// module writes (message + run + job) without exposing sql.DB.
func (s *Store) WithTx(ctx context.Context, fn func(*sql.Tx) error) error {
	return db.WithTx(ctx, s.db, nil, fn)
}

func (s *Store) ListConversations(ctx context.Context, ownerID string, cursorTime *time.Time, cursorID *uuid.UUID, limit int) ([]domain.Conversation, error) {
	query := `SELECT id, owner_id, title, needs, asks, version, expires_at, created_at, updated_at
	          FROM conv_conversations
	          WHERE owner_id = ? AND expires_at > UTC_TIMESTAMP(3)`
	args := []any{ownerID}
	if cursorTime != nil && cursorID != nil {
		query += ` AND (updated_at < ? OR (updated_at = ? AND id < ?))`
		args = append(args, *cursorTime, *cursorTime, db.UUIDBytes(*cursorID))
	}
	query += ` ORDER BY updated_at DESC, id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Conversation
	for rows.Next() {
		conversation, err := scanConversation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *conversation)
	}
	return out, rows.Err()
}

func (s *Store) GetConversation(ctx context.Context, ownerID string, conversationID uuid.UUID) (*domain.Conversation, error) {
	return scanConversation(s.db.QueryRowContext(ctx, `
		SELECT id, owner_id, title, needs, asks, version, expires_at, created_at, updated_at
		FROM conv_conversations WHERE id = ? AND owner_id = ? AND expires_at > UTC_TIMESTAMP(3)`,
		db.UUIDBytes(conversationID), ownerID))
}

func (s *Store) GetConversationByCreateKey(ctx context.Context, ownerID, key string) (*domain.Conversation, string, error) {
	var hash string
	conversation, err := scanConversationWithHash(s.db.QueryRowContext(ctx, `
		SELECT id, owner_id, title, needs, asks, version, expires_at, created_at, updated_at, COALESCE(request_hash, '')
		FROM conv_conversations WHERE owner_id = ? AND create_key = ?`, ownerID, key), &hash)
	return conversation, hash, err
}

func (s *Store) CreateConversation(ctx context.Context, conversation domain.Conversation, createKey, requestHash string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO conv_conversations
		  (id, owner_id, title, needs, asks, expires_at, create_key, request_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		db.UUIDBytes(conversation.ID), conversation.OwnerID, conversation.Title,
		string(conversation.Needs), conversation.Asks, conversation.ExpiresAt,
		nullableString(createKey), nullableString(requestHash))
	return err
}

func (s *Store) DeleteConversation(ctx context.Context, ownerID string, conversationID uuid.UUID) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM conv_conversations WHERE id = ? AND owner_id = ?`,
		db.UUIDBytes(conversationID), ownerID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (s *Store) DeleteAllConversations(ctx context.Context, ownerID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM conv_conversations WHERE owner_id = ?`, ownerID)
	return err
}

func (s *Store) ListMessages(ctx context.Context, conversationID uuid.UUID, limit int) ([]domain.Message, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, conversation_id, role, content, created_at
		FROM conv_messages WHERE conversation_id = ?
		ORDER BY created_at DESC, id DESC LIMIT ?`, db.UUIDBytes(conversationID), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Message
	for rows.Next() {
		message, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *message)
	}
	return out, rows.Err()
}

func (s *Store) GetMessageByCreateKey(ctx context.Context, conversationID uuid.UUID, key string) (*domain.Message, string, error) {
	var hash string
	message, err := scanMessageWithHash(s.db.QueryRowContext(ctx, `
		SELECT id, conversation_id, role, content, created_at, COALESCE(request_hash, '')
		FROM conv_messages WHERE conversation_id = ? AND create_key = ?`, db.UUIDBytes(conversationID), key), &hash)
	return message, hash, err
}

// CreateMessage verifies ownership/expiry and updates the conversation's
// activity timestamp in the same transaction.
func (s *Store) CreateMessage(ctx context.Context, ownerID string, message domain.Message, createKey, requestHash string, now time.Time) error {
	return db.WithTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		var lockedID []byte
		err := tx.QueryRowContext(ctx, `
			SELECT id FROM conv_conversations
			WHERE id = ? AND owner_id = ? AND expires_at > ? FOR UPDATE`,
			db.UUIDBytes(message.ConversationID), ownerID, now).Scan(&lockedID)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := insertMessageTx(ctx, tx, message, createKey, requestHash); err != nil {
			return err
		}
		return touchConversationTx(ctx, tx, ownerID, message.ConversationID, message.Role, now)
	})
}

func scanConversation(row interface{ Scan(...any) error }) (*domain.Conversation, error) {
	return scanConversationWithHash(row, nil)
}

func scanConversationWithHash(row interface{ Scan(...any) error }, hash *string) (*domain.Conversation, error) {
	var conversation domain.Conversation
	var rawID []byte
	var needs []byte
	dest := []any{&rawID, &conversation.OwnerID, &conversation.Title, &needs,
		&conversation.Asks, &conversation.Version, &conversation.ExpiresAt,
		&conversation.CreatedAt, &conversation.UpdatedAt}
	if hash != nil {
		dest = append(dest, hash)
	}
	if err := row.Scan(dest...); err != nil {
		return nil, mapScanErr(err)
	}
	parsed, err := db.UUIDFromBytes(rawID)
	if err != nil {
		return nil, err
	}
	conversation.ID = parsed
	conversation.Needs = needs
	return &conversation, nil
}

func scanMessage(row interface{ Scan(...any) error }) (*domain.Message, error) {
	return scanMessageWithHash(row, nil)
}

func scanMessageWithHash(row interface{ Scan(...any) error }, hash *string) (*domain.Message, error) {
	var message domain.Message
	var rawID, rawConversation []byte
	dest := []any{&rawID, &rawConversation, &message.Role, &message.Content, &message.CreatedAt}
	if hash != nil {
		dest = append(dest, hash)
	}
	if err := row.Scan(dest...); err != nil {
		return nil, mapScanErr(err)
	}
	parsed, err := db.UUIDFromBytes(rawID)
	if err != nil {
		return nil, err
	}
	conversation, err := db.UUIDFromBytes(rawConversation)
	if err != nil {
		return nil, err
	}
	message.ID = parsed
	message.ConversationID = conversation
	return &message, nil
}

func mapScanErr(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrNotFound
	}
	return err
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func insertMessageTx(ctx context.Context, tx *sql.Tx, message domain.Message, createKey, requestHash string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO conv_messages (id, conversation_id, role, content, create_key, request_hash, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		db.UUIDBytes(message.ID), db.UUIDBytes(message.ConversationID), message.Role,
		message.Content, nullableString(createKey), nullableString(requestHash), message.CreatedAt)
	return err
}

func touchConversationTx(ctx context.Context, tx *sql.Tx, ownerID string, conversationID uuid.UUID, role string, now time.Time) error {
	increment := 0
	if role == "user" {
		increment = 1
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE conv_conversations
		SET asks = asks + ?, version = version + 1, updated_at = ?
		WHERE id = ? AND owner_id = ?`, increment, now, db.UUIDBytes(conversationID), ownerID)
	return err
}

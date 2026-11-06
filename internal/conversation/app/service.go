package app

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/mdhuser/spendwise-biz/internal/conversation/domain"
	conversationstore "github.com/mdhuser/spendwise-biz/internal/conversation/store/mysql"
	"github.com/mdhuser/spendwise-biz/internal/platform/apperr"
	"github.com/mdhuser/spendwise-biz/internal/platform/db"
	"github.com/mdhuser/spendwise-biz/internal/platform/id"
)

type Service struct {
	store *conversationstore.Store
	now   func() time.Time
}

func New(store *conversationstore.Store) *Service {
	return &Service{store: store, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) SetClock(now func() time.Time) { s.now = now }

type CreateInput struct {
	Title string `json:"title"`
}

type MessageInput struct {
	Content string `json:"content"`
}

type ConversationView struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Asks      int       `json:"asks"`
	Version   int       `json:"version"`
	ExpiresAt time.Time `json:"expiresAt"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type MessageView struct {
	ID             string    `json:"id"`
	ConversationID string    `json:"conversationId"`
	Role           string    `json:"role"`
	Content        string    `json:"content"`
	CreatedAt      time.Time `json:"createdAt"`
}

type List struct {
	Items      []ConversationView `json:"items"`
	NextCursor *string            `json:"nextCursor"`
}

type Detail struct {
	Conversation ConversationView `json:"conversation"`
	Messages     []MessageView    `json:"messages"`
}

func (s *Service) List(ctx context.Context, ownerID, cursor string, limit int) (List, error) {
	limit = clampLimit(limit)
	var cursorTime *time.Time
	var cursorID *uuid.UUID
	if strings.TrimSpace(cursor) != "" {
		parsedTime, parsedID, err := decodeCursor(cursor)
		if err != nil {
			return List{}, err
		}
		cursorTime = &parsedTime
		cursorID = &parsedID
	}
	conversations, err := s.store.ListConversations(ctx, ownerID, cursorTime, cursorID, limit)
	if err != nil {
		return List{}, err
	}
	next := ""
	if len(conversations) > limit {
		conversations = conversations[:limit]
		next = encodeCursor(conversations[len(conversations)-1])
	}
	items := make([]ConversationView, 0, len(conversations))
	for _, conversation := range conversations {
		items = append(items, conversationView(conversation))
	}
	result := List{Items: items}
	if next != "" {
		result.NextCursor = &next
	}
	return result, nil
}

func (s *Service) Create(ctx context.Context, ownerID, idempotencyKey string, input CreateInput) (ConversationView, bool, error) {
	key, err := requireKey(idempotencyKey)
	if err != nil {
		return ConversationView{}, false, err
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		title = "新的选购"
	}
	if utf8.RuneCountInString(title) > domain.MaxTitleRunes {
		return ConversationView{}, false, apperr.Validation("会话标题不能超过 120 个字符")
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	conversation := domain.Conversation{
		ID: id.New(), OwnerID: ownerID, Title: title, Needs: json.RawMessage(`{}`), Version: 1,
		ExpiresAt: now.Add(domain.ConversationTTL), CreatedAt: now, UpdatedAt: now,
	}
	digest := requestDigest("conversation.create", input)
	if err := s.store.CreateConversation(ctx, conversation, key, digest); err != nil {
		if db.IsDuplicate(err) {
			existing, existingHash, findErr := s.store.GetConversationByCreateKey(ctx, ownerID, key)
			if findErr == nil {
				if existingHash != digest {
					return ConversationView{}, false, apperr.Conflict("IDEMPOTENCY_MISMATCH", "Idempotency-Key 已用于不同请求")
				}
				return conversationView(*existing), true, nil
			}
			return ConversationView{}, false, apperr.Conflict("CONFLICT", "会话创建冲突，请重试")
		}
		return ConversationView{}, false, err
	}
	return conversationView(conversation), false, nil
}

func (s *Service) Get(ctx context.Context, ownerID, rawID string, messageLimit int) (Detail, error) {
	conversationID, err := parseID(rawID)
	if err != nil {
		return Detail{}, err
	}
	conversation, err := s.store.GetConversation(ctx, ownerID, conversationID)
	if err != nil {
		return Detail{}, mapError(err)
	}
	messages, err := s.store.ListMessages(ctx, conversationID, clampMessageLimit(messageLimit))
	if err != nil {
		return Detail{}, err
	}
	// The store returns newest-first so the LIMIT keeps the most recent 100;
	// the API returns chronological order for rendering.
	for left, right := 0, len(messages)-1; left < right; left, right = left+1, right-1 {
		messages[left], messages[right] = messages[right], messages[left]
	}
	views := make([]MessageView, 0, len(messages))
	for _, message := range messages {
		views = append(views, messageView(message))
	}
	return Detail{Conversation: conversationView(*conversation), Messages: views}, nil
}

func (s *Service) Delete(ctx context.Context, ownerID, rawID string) error {
	conversationID, err := parseID(rawID)
	if err != nil {
		return err
	}
	return mapError(s.store.DeleteConversation(ctx, ownerID, conversationID))
}

func (s *Service) DeleteAll(ctx context.Context, ownerID string) error {
	return s.store.DeleteAllConversations(ctx, ownerID)
}

func (s *Service) ListMessages(ctx context.Context, ownerID, rawID string, limit int) ([]MessageView, error) {
	conversationID, err := parseID(rawID)
	if err != nil {
		return nil, err
	}
	if _, err := s.store.GetConversation(ctx, ownerID, conversationID); err != nil {
		return nil, mapError(err)
	}
	messages, err := s.store.ListMessages(ctx, conversationID, clampMessageLimit(limit))
	if err != nil {
		return nil, err
	}
	for left, right := 0, len(messages)-1; left < right; left, right = left+1, right-1 {
		messages[left], messages[right] = messages[right], messages[left]
	}
	out := make([]MessageView, 0, len(messages))
	for _, message := range messages {
		out = append(out, messageView(message))
	}
	return out, nil
}

func (s *Service) CreateMessage(ctx context.Context, ownerID, rawConversationID, idempotencyKey string, input MessageInput) (MessageView, bool, error) {
	conversationID, err := parseID(rawConversationID)
	if err != nil {
		return MessageView{}, false, err
	}
	key, err := requireKey(idempotencyKey)
	if err != nil {
		return MessageView{}, false, err
	}
	content := strings.TrimSpace(input.Content)
	if content == "" {
		return MessageView{}, false, apperr.Validation("消息内容不能为空")
	}
	if utf8.RuneCountInString(content) > domain.MaxContentRunes {
		return MessageView{}, false, apperr.Validation("消息内容不能超过 4000 个字符")
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	message := domain.Message{
		ID: id.New(), ConversationID: conversationID, Role: "user",
		Content: content, CreatedAt: now,
	}
	digest := requestDigest("conversation.message.create:"+conversationID.String(), input)
	if err := s.store.CreateMessage(ctx, ownerID, message, key, digest, now); err != nil {
		if db.IsDuplicate(err) {
			existing, existingHash, findErr := s.store.GetMessageByCreateKey(ctx, conversationID, key)
			if findErr == nil {
				if existingHash != digest {
					return MessageView{}, false, apperr.Conflict("IDEMPOTENCY_MISMATCH", "Idempotency-Key 已用于不同请求")
				}
				return messageView(*existing), true, nil
			}
			return MessageView{}, false, apperr.Conflict("CONFLICT", "消息创建冲突，请重试")
		}
		return MessageView{}, false, mapError(err)
	}
	return messageView(message), false, nil
}

// InsertMessage is used by the M3 worker to append an assistant reply without a
// user idempotency key. The run id is the idempotency boundary at that layer.
func (s *Service) InsertMessage(ctx context.Context, ownerID string, conversationID uuid.UUID, role, content string, createdAt time.Time) (MessageView, error) {
	message := domain.Message{ID: id.New(), ConversationID: conversationID, Role: role, Content: content, CreatedAt: createdAt}
	if err := s.store.CreateMessage(ctx, ownerID, message, "", "", createdAt); err != nil {
		return MessageView{}, mapError(err)
	}
	return messageView(message), nil
}

func conversationView(conversation domain.Conversation) ConversationView {
	return ConversationView{
		ID: conversation.ID.String(), Title: conversation.Title, Asks: conversation.Asks,
		Version: conversation.Version, ExpiresAt: conversation.ExpiresAt,
		CreatedAt: conversation.CreatedAt, UpdatedAt: conversation.UpdatedAt,
	}
}

func messageView(message domain.Message) MessageView {
	return MessageView{
		ID: message.ID.String(), ConversationID: message.ConversationID.String(),
		Role: message.Role, Content: message.Content, CreatedAt: message.CreatedAt,
	}
}

func encodeCursor(conversation domain.Conversation) string {
	raw := conversation.UpdatedAt.UTC().Format("2006-01-02T15:04:05.000Z") + "|" + conversation.ID.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(value string) (time.Time, uuid.UUID, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return time.Time{}, uuid.Nil, apperr.Validation("cursor 不合法")
	}
	parts := strings.SplitN(string(decoded), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, uuid.Nil, apperr.Validation("cursor 不合法")
	}
	parsedTime, err := time.Parse("2006-01-02T15:04:05.000Z", parts[0])
	if err != nil {
		return time.Time{}, uuid.Nil, apperr.Validation("cursor 不合法")
	}
	parsedID, err := uuid.Parse(parts[1])
	if err != nil {
		return time.Time{}, uuid.Nil, apperr.Validation("cursor 不合法")
	}
	return parsedTime.UTC(), parsedID, nil
}

func requireKey(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) < 8 || len(value) > 128 {
		return "", apperr.New(400, "IDEMPOTENCY_KEY_REQUIRED", "必须提供 8-128 字符的 Idempotency-Key")
	}
	return value, nil
}

func requestDigest(scope string, payload any) string {
	raw, err := json.Marshal(payload)
	if err != nil {
		raw = []byte(scope)
	}
	hash := sha256.Sum256(append([]byte(scope+"\n"), raw...))
	return hex.EncodeToString(hash[:])
}

func parseID(value string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return uuid.Nil, apperr.Validation("id 不合法")
	}
	return parsed, nil
}

func clampLimit(value int) int {
	if value <= 0 {
		return 50
	}
	if value > 100 {
		return 100
	}
	return value
}

func clampMessageLimit(value int) int {
	if value <= 0 {
		return 100
	}
	if value > 100 {
		return 100
	}
	return value
}

func mapError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, domain.ErrNotFound):
		return apperr.NotFound("会话不存在或已过期")
	default:
		return err
	}
}

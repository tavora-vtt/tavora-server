package ws

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/tavora-vtt/tavora-server/internal/core/auth"
	"github.com/tavora-vtt/tavora-server/internal/core/dice"
	"github.com/tavora-vtt/tavora-server/internal/storage"
)

const (
	KindMessage    = "message"
	maxMessageLen  = 2000
	AudiencePublic = "public"
	AudienceGM     = "gm"
)

type ChatPostPayload struct {
	Text     string `json:"text"`
	Audience string `json:"audience,omitempty"`
}

type ChatRollPayload struct {
	Expression string `json:"expression"`
	Reason     string `json:"reason,omitempty"`
	Audience   string `json:"audience,omitempty"`
}

type ChatMessage struct {
	ID       string         `json:"id"`
	Seq      int64          `json:"seq"`
	AuthorID string         `json:"authorId"`
	Author   string         `json:"author"`
	At       string         `json:"at"`
	Text     string         `json:"text,omitempty"`
	Key      string         `json:"key,omitempty"`
	Params   map[string]any `json:"params,omitempty"`
	Roll     *dice.Result   `json:"roll,omitempty"`
	Audience string         `json:"audience"`
	Reason   string         `json:"reason,omitempty"`
}

func RegisterChatIntents(router *Router) {
	router.Handle("chat.post", handleChatPost)
	router.Handle("chat.roll", handleChatRoll)
}

func handleChatPost(ctx context.Context, session *Session, intent Intent) (IntentResult, error) {
	var payload ChatPostPayload
	if err := json.Unmarshal(intent.Payload, &payload); err != nil {
		return IntentResult{}, NewIntentError(CodeBadRequest, "core.intent.malformedPayload")
	}

	text := strings.TrimSpace(payload.Text)
	if text == "" {
		return IntentResult{}, NewIntentError(CodeBadRequest, "core.chat.emptyMessage")
	}
	if len(text) > maxMessageLen {
		text = text[:maxMessageLen]
	}

	return publishMessage(ctx, session, ChatMessage{
		Text:     text,
		Audience: audienceOf(payload.Audience, session),
	})
}

func handleChatRoll(ctx context.Context, session *Session, intent Intent) (IntentResult, error) {
	var payload ChatRollPayload
	if err := json.Unmarshal(intent.Payload, &payload); err != nil {
		return IntentResult{}, NewIntentError(CodeBadRequest, "core.intent.malformedPayload")
	}

	result, err := dice.Roll(payload.Expression, dice.CryptoSource())
	switch {
	case errors.Is(err, dice.ErrTooManyDice):
		return IntentResult{}, NewIntentError(CodeBadRequest, "core.dice.tooManyDice")
	case err != nil:
		return IntentResult{}, NewIntentError(CodeBadRequest, "core.dice.malformedExpression")
	}

	return publishMessage(ctx, session, ChatMessage{
		Key:      "core.chat.rolled",
		Params:   map[string]any{"total": result.Total, "expression": result.Expression},
		Roll:     &result,
		Reason:   strings.TrimSpace(payload.Reason),
		Audience: audienceOf(payload.Audience, session),
	})
}

func audienceOf(requested string, session *Session) string {
	if requested == AudienceGM && session.Subject().Role.IsStaff() {
		return AudienceGM
	}
	return AudiencePublic
}

func publishMessage(ctx context.Context, session *Session, message ChatMessage) (IntentResult, error) {
	message.ID = string(auth.GenerateID("message"))
	message.AuthorID = string(session.UserID())
	message.Author = session.DisplayName()
	message.At = time.Now().UTC().Format(time.RFC3339)

	body, err := json.Marshal(message)
	if err != nil {
		return IntentResult{}, err
	}

	var (
		seq      int64
		audience []storage.ID
	)

	err = session.Store().Tx(ctx, func(tx storage.Tx) error {
		if message.Audience == AudienceGM {
			members, err := tx.ListMembers(ctx, session.WorldID())
			if err != nil {
				return err
			}
			for _, member := range members {
				if member.Role == "gm" || member.Role == "assistant" || member.UserID == session.UserID() {
					audience = append(audience, member.UserID)
				}
			}
		}

		encodedAudience, err := json.Marshal(audience)
		if err != nil {
			return err
		}

		seq, err = tx.AppendEvent(ctx, storage.Event{
			WorldID:     session.WorldID(),
			ActorUserID: session.UserID(),
			Kind:        "chat.message",
			TargetKind:  KindMessage,
			TargetID:    storage.ID(message.ID),
			Payload:     body,
			Audience:    encodedAudience,
		})
		if err != nil {
			return err
		}

		return tx.PutDocument(ctx, &storage.Document{
			WorldID:   session.WorldID(),
			ID:        storage.ID(message.ID),
			Kind:      KindMessage,
			Name:      message.Author,
			Data:      body,
			Ownership: json.RawMessage(`{"default":"observer"}`),
		})
	})
	if err != nil {
		return IntentResult{}, err
	}

	message.Seq = seq
	final, err := json.Marshal(message)
	if err != nil {
		return IntentResult{}, err
	}

	session.Hub().Publish(Publication{
		Channel:  WorldChannel(session.WorldID()),
		Lane:     LaneDocument,
		Audience: audience,
		Frame: Frame{
			Lane: LaneDocument,
			Type: TypeEvent,
			Event: &Event{
				Seq:     seq,
				Kind:    "chat.message",
				WorldID: string(session.WorldID()),
				Payload: final,
			},
		},
	})

	return IntentResult{Seq: seq, Result: final}, nil
}

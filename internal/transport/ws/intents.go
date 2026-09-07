package ws

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/tavora-vtt/tavora-server/internal/storage"
)

type DocumentPatchPayload struct {
	ID    string         `json:"id"`
	Name  *string        `json:"name,omitempty"`
	Sort  *int           `json:"sort,omitempty"`
	Set   map[string]any `json:"set,omitempty"`
	Unset []string       `json:"unset,omitempty"`
}

type DocumentPatchResult struct {
	ID         string          `json:"id"`
	Seq        int64           `json:"seq"`
	Name       string          `json:"name"`
	Data       json.RawMessage `json:"data"`
	UpdatedSeq int64           `json:"updatedSeq"`
}

func RegisterCoreIntents(router *Router) {
	router.Handle("document.patch", handleDocumentPatch)
}

func handleDocumentPatch(ctx context.Context, session *Session, intent Intent) (json.RawMessage, error) {
	var payload DocumentPatchPayload
	if err := json.Unmarshal(intent.Payload, &payload); err != nil {
		return nil, NewIntentError(CodeBadRequest, "core.intent.malformedPayload")
	}
	if payload.ID == "" {
		return nil, NewIntentError(CodeBadRequest, "core.intent.missingDocumentId")
	}

	var (
		patched *storage.Document
		seq     int64
	)

	err := session.Store().Tx(ctx, func(tx storage.Tx) error {
		eventPayload, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			return marshalErr
		}

		appended, appendErr := tx.AppendEvent(ctx, storage.Event{
			WorldID:     session.WorldID(),
			ActorUserID: session.UserID(),
			Kind:        "document.patch",
			TargetKind:  "document",
			TargetID:    storage.ID(payload.ID),
			Payload:     eventPayload,
		})
		if appendErr != nil {
			return appendErr
		}
		seq = appended

		var patchErr error
		patched, patchErr = tx.PatchDocument(ctx, session.WorldID(), storage.ID(payload.ID), storage.Patch{
			Name:  payload.Name,
			Sort:  payload.Sort,
			Set:   payload.Set,
			Unset: payload.Unset,
			Seq:   appended,
		})
		return patchErr
	})

	switch {
	case errors.Is(err, storage.ErrNotFound):
		return nil, NewIntentError(CodeNotFound, "core.intent.documentNotFound")
	case errors.Is(err, storage.ErrInvalidPath):
		return nil, NewIntentError(CodeBadRequest, "core.intent.invalidPath")
	case err != nil:
		return nil, err
	}

	eventPayload, err := json.Marshal(DocumentPatchResult{
		ID:         string(patched.ID),
		Seq:        seq,
		Name:       patched.Name,
		Data:       patched.Data,
		UpdatedSeq: patched.UpdatedSeq,
	})
	if err != nil {
		return nil, err
	}

	session.Hub().Publish(Publication{
		Channel: WorldChannel(session.WorldID()),
		Lane:    LaneDocument,
		Frame: Frame{
			Lane: LaneDocument,
			Type: TypeEvent,
			Event: &Event{
				Seq:     seq,
				Kind:    "document.patch",
				WorldID: string(session.WorldID()),
				Payload: eventPayload,
			},
		},
	})

	return eventPayload, nil
}

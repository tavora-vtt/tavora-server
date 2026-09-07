package ws

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/tavora-vtt/tavora-server/internal/core/access"
	"github.com/tavora-vtt/tavora-server/internal/core/sight"
	"github.com/tavora-vtt/tavora-server/internal/storage"
)

type DocumentPatchPayload struct {
	ID    string         `json:"id"`
	Name  *string        `json:"name,omitempty"`
	Sort  *int           `json:"sort,omitempty"`
	Set   map[string]any `json:"set,omitempty"`
	Unset []string       `json:"unset,omitempty"`
}

type DocumentView struct {
	ID         string          `json:"id"`
	Seq        int64           `json:"seq"`
	Kind       string          `json:"kind"`
	Subtype    string          `json:"subtype,omitempty"`
	Name       string          `json:"name"`
	Img        string          `json:"img,omitempty"`
	Data       json.RawMessage `json:"data"`
	Flags      json.RawMessage `json:"flags,omitempty"`
	Ownership  json.RawMessage `json:"ownership,omitempty"`
	UpdatedSeq int64           `json:"updatedSeq"`
}

type TokenMovePayload struct {
	TokenID string  `json:"tokenId"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
}

type SceneActivatePayload struct {
	SceneID string `json:"sceneId"`
}

type SceneActivated struct {
	SceneID string `json:"sceneId"`
	Name    string `json:"name"`
	Seq     int64  `json:"seq"`
}

func RegisterCoreIntents(router *Router) {
	router.Handle("document.patch", handleDocumentPatch)
	router.Handle("scene.token.move", handleTokenMove)
	router.Handle("scene.activate", handleSceneActivate)
}

func handleSceneActivate(ctx context.Context, session *Session, intent Intent) (IntentResult, error) {
	var payload SceneActivatePayload
	if err := json.Unmarshal(intent.Payload, &payload); err != nil {
		return IntentResult{}, NewIntentError(CodeBadRequest, "core.intent.malformedPayload")
	}
	if payload.SceneID == "" {
		return IntentResult{}, NewIntentError(CodeBadRequest, "core.intent.missingSceneId")
	}

	if !session.Subject().Role.IsStaff() {
		return IntentResult{}, NewIntentError(CodeForbidden, "core.intent.gameMasterOnly")
	}

	var activated SceneActivated

	err := session.Store().Tx(ctx, func(tx storage.Tx) error {
		scene, err := tx.GetDocument(ctx, session.WorldID(), storage.ID(payload.SceneID))
		if err != nil {
			return err
		}
		if scene.Kind != "scene" {
			return storage.ErrNotFound
		}

		world, err := tx.GetWorld(ctx, session.WorldID())
		if err != nil {
			return err
		}
		world.ActiveScene = scene.ID

		if err := tx.PutWorld(ctx, world); err != nil {
			return err
		}

		encoded, err := json.Marshal(SceneActivatePayload{SceneID: payload.SceneID})
		if err != nil {
			return err
		}

		seq, err := tx.AppendEvent(ctx, storage.Event{
			WorldID:     session.WorldID(),
			ActorUserID: session.UserID(),
			Kind:        "scene.activate",
			TargetKind:  "document",
			TargetID:    scene.ID,
			SceneID:     scene.ID,
			Payload:     encoded,
		})
		if err != nil {
			return err
		}

		activated = SceneActivated{SceneID: string(scene.ID), Name: scene.Name, Seq: seq}
		return nil
	})

	switch {
	case errors.Is(err, storage.ErrNotFound):
		return IntentResult{}, NewIntentError(CodeNotFound, "core.intent.sceneNotFound")
	case err != nil:
		return IntentResult{}, err
	}

	encoded, err := json.Marshal(activated)
	if err != nil {
		return IntentResult{}, err
	}

	session.Hub().Publish(Publication{
		Channel: WorldChannel(session.WorldID()),
		Lane:    LaneDocument,
		Frame: Frame{
			Lane: LaneDocument,
			Type: TypeEvent,
			Event: &Event{
				Seq:     activated.Seq,
				Kind:    "scene.activate",
				WorldID: string(session.WorldID()),
				SceneID: activated.SceneID,
				Payload: encoded,
			},
		},
	})

	return IntentResult{Seq: activated.Seq, Result: encoded}, nil
}

func handleTokenMove(ctx context.Context, session *Session, intent Intent) (IntentResult, error) {
	var payload TokenMovePayload
	if err := json.Unmarshal(intent.Payload, &payload); err != nil {
		return IntentResult{}, NewIntentError(CodeBadRequest, "core.intent.malformedPayload")
	}
	if payload.TokenID == "" {
		return IntentResult{}, NewIntentError(CodeBadRequest, "core.intent.missingDocumentId")
	}

	return applyPatch(ctx, session, storage.ID(payload.TokenID), storage.Patch{
		Set: map[string]any{"x": payload.X, "y": payload.Y},
	}, "scene.token.move", payload)
}

func handleDocumentPatch(ctx context.Context, session *Session, intent Intent) (IntentResult, error) {
	var payload DocumentPatchPayload
	if err := json.Unmarshal(intent.Payload, &payload); err != nil {
		return IntentResult{}, NewIntentError(CodeBadRequest, "core.intent.malformedPayload")
	}
	if payload.ID == "" {
		return IntentResult{}, NewIntentError(CodeBadRequest, "core.intent.missingDocumentId")
	}

	return applyPatch(ctx, session, storage.ID(payload.ID), storage.Patch{
		Name:  payload.Name,
		Sort:  payload.Sort,
		Set:   payload.Set,
		Unset: payload.Unset,
	}, "document.patch", payload)
}

func applyPatch(
	ctx context.Context,
	session *Session,
	documentID storage.ID,
	patch storage.Patch,
	eventKind string,
	eventPayload any,
) (IntentResult, error) {
	var (
		seq   int64
		views map[storage.ID]access.View
	)

	err := session.Store().Tx(ctx, func(tx storage.Tx) error {
		current, err := tx.GetDocument(ctx, session.WorldID(), documentID)
		if err != nil {
			return err
		}

		if _, err := session.Access().Authorize(
			ctx, tx, session.WorldID(), session.UserID(), current, true,
		); err != nil {
			return err
		}

		encoded, err := json.Marshal(eventPayload)
		if err != nil {
			return err
		}

		seq, err = tx.AppendEvent(ctx, storage.Event{
			WorldID:     session.WorldID(),
			ActorUserID: session.UserID(),
			Kind:        eventKind,
			TargetKind:  "document",
			TargetID:    documentID,
			Payload:     encoded,
		})
		if err != nil {
			return err
		}

		patch.Seq = seq

		patched, err := tx.PatchDocument(ctx, session.WorldID(), documentID, patch)
		if err != nil {
			return err
		}

		views, err = session.Access().ProjectForMembers(ctx, tx, patched)
		if err != nil {
			return err
		}

		views, err = sight.Filter(ctx, tx, session.Access(), session.WorldID(), patched, views)
		return err
	})

	switch {
	case errors.Is(err, access.ErrForbidden):
		return IntentResult{}, NewIntentError(CodeForbidden, "core.intent.forbidden")
	case errors.Is(err, storage.ErrNotFound):
		return IntentResult{}, NewIntentError(CodeNotFound, "core.intent.documentNotFound")
	case errors.Is(err, storage.ErrInvalidPath):
		return IntentResult{}, NewIntentError(CodeBadRequest, "core.intent.invalidPath")
	case err != nil:
		return IntentResult{}, err
	}

	perUser := make(map[storage.ID]Frame, len(views))
	for userID, view := range views {
		encoded, err := json.Marshal(viewOf(view, seq))
		if err != nil {
			return IntentResult{}, err
		}
		perUser[userID] = Frame{
			Lane: LaneDocument,
			Type: TypeEvent,
			Event: &Event{
				Seq:     seq,
				Kind:    eventKind,
				WorldID: string(session.WorldID()),
				Payload: encoded,
			},
		}
	}

	session.Hub().Publish(Publication{
		Channel: WorldChannel(session.WorldID()),
		Lane:    LaneDocument,
		PerUser: perUser,
	})

	own, present := views[session.UserID()]
	if !present {
		return IntentResult{}, NewIntentError(CodeForbidden, "core.intent.forbidden")
	}

	result, err := json.Marshal(viewOf(own, seq))
	if err != nil {
		return IntentResult{}, err
	}
	return IntentResult{Seq: seq, Result: result}, nil
}

func viewOf(view access.View, seq int64) DocumentView {
	doc := view.Document
	return DocumentView{
		ID:         string(doc.ID),
		Seq:        seq,
		Kind:       doc.Kind,
		Subtype:    doc.Subtype,
		Name:       doc.Name,
		Img:        doc.Img,
		Data:       doc.Data,
		Flags:      doc.Flags,
		Ownership:  doc.Ownership,
		UpdatedSeq: doc.UpdatedSeq,
	}
}

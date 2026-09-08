package ws

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/tavora-vtt/tavora-server/internal/core/perm"
	"github.com/tavora-vtt/tavora-server/internal/core/sight"
	"github.com/tavora-vtt/tavora-server/internal/storage"
)

type DoorTogglePayload struct {
	WallID string `json:"wallId"`
}

type DoorState struct {
	WallID   string `json:"wallId"`
	SceneID  string `json:"sceneId"`
	DoorOpen bool   `json:"doorOpen"`
	Seq      int64  `json:"seq"`
}

type VisibilityUpdate struct {
	SceneID string       `json:"sceneId"`
	Tokens  []tokenBrief `json:"tokens"`
}

type tokenBrief struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Img  string          `json:"img,omitempty"`
	Data json.RawMessage `json:"data"`
}

func RegisterDoorIntents(router *Router) {
	router.Handle("scene.door.toggle", handleDoorToggle)
}

func handleDoorToggle(ctx context.Context, session *Session, intent Intent) (IntentResult, error) {
	if err := requireStaff(session); err != nil {
		return IntentResult{}, err
	}

	var payload DoorTogglePayload
	if err := json.Unmarshal(intent.Payload, &payload); err != nil {
		return IntentResult{}, NewIntentError(CodeBadRequest, "core.intent.malformedPayload")
	}
	if payload.WallID == "" {
		return IntentResult{}, NewIntentError(CodeBadRequest, "core.intent.missingDocumentId")
	}

	var (
		state    DoorState
		perUser  map[storage.ID]Frame
		sceneKey storage.ID
	)

	err := session.Store().Tx(ctx, func(tx storage.Tx) error {
		wall, err := tx.GetDocument(ctx, session.WorldID(), storage.ID(payload.WallID))
		if err != nil {
			return err
		}

		var data map[string]any
		if err := json.Unmarshal(wall.Data, &data); err != nil {
			return err
		}
		if door, _ := data["door"].(bool); !door {
			return errNotADoor
		}

		open, _ := data["doorOpen"].(bool)
		data["doorOpen"] = !open

		encoded, err := json.Marshal(data)
		if err != nil {
			return err
		}
		wall.Data = encoded

		seq, err := tx.AppendEvent(ctx, storage.Event{
			WorldID:     session.WorldID(),
			ActorUserID: session.UserID(),
			Kind:        "scene.door.toggle",
			TargetKind:  "document",
			TargetID:    wall.ID,
			SceneID:     wall.ParentID,
			Payload:     encoded,
		})
		if err != nil {
			return err
		}

		wall.UpdatedSeq = seq
		if err := tx.PutDocument(ctx, wall); err != nil {
			return err
		}

		sceneKey = wall.ParentID
		state = DoorState{
			WallID:   string(wall.ID),
			SceneID:  string(wall.ParentID),
			DoorOpen: !open,
			Seq:      seq,
		}

		visibilitySeq, err := tx.AppendEvent(ctx, storage.Event{
			WorldID:     session.WorldID(),
			ActorUserID: session.UserID(),
			Kind:        "scene.visibility",
			TargetKind:  "document",
			TargetID:    wall.ParentID,
			SceneID:     wall.ParentID,
			Payload:     json.RawMessage(`{"sceneId":"` + string(wall.ParentID) + `"}`),
		})
		if err != nil {
			return err
		}

		perUser, err = visibilityFrames(ctx, tx, session, sceneKey, visibilitySeq)
		return err
	})

	switch {
	case errors.Is(err, errNotADoor):
		return IntentResult{}, NewIntentError(CodeBadRequest, "core.scene.notADoor")
	case errors.Is(err, storage.ErrNotFound):
		return IntentResult{}, NewIntentError(CodeNotFound, "core.scene.unknownWall")
	case err != nil:
		return IntentResult{}, err
	}

	encoded, err := json.Marshal(state)
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
				Seq:     state.Seq,
				Kind:    "scene.door.toggle",
				WorldID: string(session.WorldID()),
				SceneID: state.SceneID,
				Payload: encoded,
			},
		},
	})

	session.Hub().Publish(Publication{
		Channel: WorldChannel(session.WorldID()),
		Lane:    LaneDocument,
		PerUser: perUser,
	})

	return IntentResult{Seq: state.Seq, Result: encoded}, nil
}

var errNotADoor = errors.New("ws: that wall is not a door")

func visibilityFrames(
	ctx context.Context,
	tx storage.Tx,
	session *Session,
	sceneID storage.ID,
	seq int64,
) (map[storage.ID]Frame, error) {
	members, err := tx.ListMembers(ctx, session.WorldID())
	if err != nil {
		return nil, err
	}

	parent := sceneID
	tokens, err := tx.ListDocuments(ctx, session.WorldID(), storage.DocumentFilter{
		Kind:     "token",
		ParentID: &parent,
	})
	if err != nil {
		return nil, err
	}

	frames := make(map[storage.ID]Frame, len(members))

	for _, member := range members {
		role := perm.Role(member.Role)
		if !role.Valid() {
			continue
		}
		subject := perm.Subject{UserID: member.UserID, Role: role}

		visible, err := sight.VisibleTokens(
			ctx, tx, session.Access(), subject, session.WorldID(), sceneID, tokens)
		if err != nil {
			return nil, err
		}

		update := VisibilityUpdate{SceneID: string(sceneID), Tokens: make([]tokenBrief, 0, len(visible))}
		for _, token := range visible {
			grant, err := session.Access().Grant(ctx, tx, subject, token)
			if err != nil {
				return nil, err
			}
			redacted, send, err := perm.RedactDocument(token, subject, grant, session.Access().Policy())
			if err != nil {
				return nil, err
			}
			if !send {
				continue
			}
			update.Tokens = append(update.Tokens, tokenBrief{
				ID:   string(redacted.ID),
				Name: redacted.Name,
				Img:  redacted.Img,
				Data: redacted.Data,
			})
		}

		encoded, err := json.Marshal(update)
		if err != nil {
			return nil, err
		}

		frames[member.UserID] = Frame{
			Lane: LaneDocument,
			Type: TypeEvent,
			Event: &Event{
				Seq:     seq,
				Kind:    "scene.visibility",
				WorldID: string(session.WorldID()),
				SceneID: string(sceneID),
				Payload: encoded,
			},
		}
	}

	return frames, nil
}

package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/tavora-vtt/tavora-server/internal/core/auth"
	"github.com/tavora-vtt/tavora-server/internal/core/perm"
	"github.com/tavora-vtt/tavora-server/internal/core/sight"
	"github.com/tavora-vtt/tavora-server/internal/storage"
)

const (
	KindScene = "scene"
	KindToken = "token"
	KindActor = "actor"
	KindWall  = "wall"
)

type SceneData struct {
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	GridSize   int    `json:"gridSize"`
	GridType   string `json:"gridType"`
	Background string `json:"background,omitempty"`
}

type TokenData struct {
	X           float64 `json:"x"`
	Y           float64 `json:"y"`
	Width       float64 `json:"width"`
	Height      float64 `json:"height"`
	Disposition string  `json:"disposition"`
	Hidden      bool    `json:"hidden"`
}

type sceneView struct {
	ID     string    `json:"id"`
	Name   string    `json:"name"`
	Active bool      `json:"active"`
	Data   SceneData `json:"data"`
}

type tokenView struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Data json.RawMessage `json:"data"`
}

type createSceneRequest struct {
	Name     string `json:"name"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	GridSize int    `json:"gridSize"`
}

type createTokenRequest struct {
	Name        string  `json:"name"`
	X           float64 `json:"x"`
	Y           float64 `json:"y"`
	Disposition string  `json:"disposition"`
}

func defaultSceneData(body createSceneRequest) SceneData {
	data := SceneData{
		Width:    body.Width,
		Height:   body.Height,
		GridSize: body.GridSize,
		GridType: "square",
	}
	if data.Width <= 0 {
		data.Width = 2400
	}
	if data.Height <= 0 {
		data.Height = 1600
	}
	if data.GridSize <= 0 {
		data.GridSize = 100
	}
	return data
}

func (d AuthDeps) listScenesHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		worldID := storage.ID(r.PathValue("worldId"))
		if _, ok := d.requireRole(w, r, worldID, false); !ok {
			return
		}

		var views []sceneView
		err := d.Store.ReadOnly(r.Context(), func(q storage.Query) error {
			world, err := q.GetWorld(r.Context(), worldID)
			if err != nil {
				return err
			}

			documents, err := q.ListDocuments(r.Context(), worldID, storage.DocumentFilter{Kind: KindScene})
			if err != nil {
				return err
			}
			views = make([]sceneView, 0, len(documents))
			for _, document := range documents {
				var data SceneData
				_ = json.Unmarshal(document.Data, &data)
				views = append(views, sceneView{
					ID:     string(document.ID),
					Name:   document.Name,
					Active: document.ID == world.ActiveScene,
					Data:   data,
				})
			}
			return nil
		})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal", MessageKey: "core.api.internalError"})
			return
		}

		writeJSON(w, http.StatusOK, views)
	}
}

func (d AuthDeps) createSceneHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		worldID := storage.ID(r.PathValue("worldId"))
		if _, ok := d.requireRole(w, r, worldID, true); !ok {
			return
		}

		var body createSceneRequest
		if !decodeBody(w, r, &body) {
			return
		}
		if strings.TrimSpace(body.Name) == "" {
			body.Name = "New scene"
		}

		data, err := json.Marshal(defaultSceneData(body))
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal", MessageKey: "core.api.internalError"})
			return
		}

		document := &storage.Document{
			WorldID:   worldID,
			ID:        auth.GenerateID("scene"),
			Kind:      KindScene,
			Name:      strings.TrimSpace(body.Name),
			Data:      data,
			Ownership: json.RawMessage(`{"default":"observer"}`),
		}

		if err := d.Store.Tx(r.Context(), func(tx storage.Tx) error {
			return tx.PutDocument(r.Context(), document)
		}); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal", MessageKey: "core.api.internalError"})
			return
		}

		var view SceneData
		_ = json.Unmarshal(document.Data, &view)
		writeJSON(w, http.StatusCreated, sceneView{
			ID: string(document.ID), Name: document.Name, Data: view,
		})
	}
}

func (d AuthDeps) listTokensHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		worldID := storage.ID(r.PathValue("worldId"))
		sceneID := storage.ID(r.PathValue("sceneId"))

		user, ok := d.requireRole(w, r, worldID, false)
		if !ok {
			return
		}

		var views []tokenView
		err := d.Store.ReadOnly(r.Context(), func(q storage.Query) error {
			role, err := d.Access.RoleOf(r.Context(), q, worldID, user.ID)
			if err != nil {
				return err
			}
			subject := perm.Subject{UserID: user.ID, Role: role}

			parent := sceneID
			documents, err := q.ListDocuments(r.Context(), worldID, storage.DocumentFilter{
				Kind:     KindToken,
				ParentID: &parent,
			})
			if err != nil {
				return err
			}

			documents, err = sight.VisibleTokens(
				r.Context(), q, d.Access, subject, worldID, sceneID, documents)
			if err != nil {
				return err
			}

			views = make([]tokenView, 0, len(documents))
			for _, document := range documents {
				grant, err := d.Access.Grant(r.Context(), q, subject, document)
				if err != nil {
					return err
				}
				visible, send, err := perm.RedactDocument(document, subject, grant, d.Access.Policy())
				if err != nil {
					return err
				}
				if !send {
					continue
				}
				views = append(views, tokenView{
					ID: string(visible.ID), Name: visible.Name, Data: visible.Data,
				})
			}
			return nil
		})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal", MessageKey: "core.api.internalError"})
			return
		}

		writeJSON(w, http.StatusOK, views)
	}
}

func (d AuthDeps) createTokenHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		worldID := storage.ID(r.PathValue("worldId"))
		sceneID := storage.ID(r.PathValue("sceneId"))

		if _, ok := d.requireRole(w, r, worldID, true); !ok {
			return
		}

		var body createTokenRequest
		if !decodeBody(w, r, &body) {
			return
		}
		if strings.TrimSpace(body.Name) == "" {
			body.Name = "Token"
		}
		if body.Disposition == "" {
			body.Disposition = "neutral"
		}

		data, err := json.Marshal(TokenData{
			X: body.X, Y: body.Y, Width: 1, Height: 1,
			Disposition: body.Disposition,
		})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal", MessageKey: "core.api.internalError"})
			return
		}

		document := &storage.Document{
			WorldID:   worldID,
			ID:        auth.GenerateID("token"),
			Kind:      KindToken,
			ParentID:  sceneID,
			Name:      strings.TrimSpace(body.Name),
			Data:      data,
			Ownership: json.RawMessage(`{"default":"observer"}`),
		}

		err = d.Store.Tx(r.Context(), func(tx storage.Tx) error {
			if _, err := tx.GetDocument(r.Context(), worldID, sceneID); err != nil {
				return err
			}
			return tx.PutDocument(r.Context(), document)
		})
		if errors.Is(err, storage.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, apiError{Code: "not_found", MessageKey: "core.scene.unknown"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal", MessageKey: "core.api.internalError"})
			return
		}

		writeJSON(w, http.StatusCreated, tokenView{
			ID: string(document.ID), Name: document.Name, Data: document.Data,
		})
	}
}

type actorView struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Subtype   string          `json:"subtype"`
	Data      json.RawMessage `json:"data"`
	Ownership json.RawMessage `json:"ownership,omitempty"`
	CanEdit   bool            `json:"canEdit"`
}

type createActorRequest struct {
	Name    string `json:"name"`
	Subtype string `json:"subtype"`
}

func defaultVampireData() map[string]any {
	return map[string]any{
		"clan":   "",
		"hunger": 1,
		"attributes": map[string]any{
			"strength": 1, "dexterity": 1, "stamina": 1,
			"charisma": 1, "manipulation": 1, "composure": 1,
			"intelligence": 1, "wits": 1, "resolve": 1,
		},
		"health":    map[string]any{"superficial": 0, "aggravated": 0, "max": 4},
		"willpower": map[string]any{"superficial": 0, "aggravated": 0, "max": 2},
		"notes":     "",
	}
}

func (d AuthDeps) listActorsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		worldID := storage.ID(r.PathValue("worldId"))

		user, ok := d.requireRole(w, r, worldID, false)
		if !ok {
			return
		}

		var views []actorView
		err := d.Store.ReadOnly(r.Context(), func(q storage.Query) error {
			role, err := d.Access.RoleOf(r.Context(), q, worldID, user.ID)
			if err != nil {
				return err
			}
			subject := perm.Subject{UserID: user.ID, Role: role}

			documents, err := q.ListDocuments(r.Context(), worldID, storage.DocumentFilter{Kind: KindActor})
			if err != nil {
				return err
			}

			views = make([]actorView, 0, len(documents))
			for _, document := range documents {
				grant, err := d.Access.Grant(r.Context(), q, subject, document)
				if err != nil {
					return err
				}
				visible, send, err := perm.RedactDocument(document, subject, grant, d.Access.Policy())
				if err != nil {
					return err
				}
				if !send {
					continue
				}
				views = append(views, actorView{
					ID:        string(visible.ID),
					Name:      visible.Name,
					Subtype:   visible.Subtype,
					Data:      visible.Data,
					Ownership: visible.Ownership,
					CanEdit:   grant.CanEdit(),
				})
			}
			return nil
		})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal", MessageKey: "core.api.internalError"})
			return
		}

		writeJSON(w, http.StatusOK, views)
	}
}

func (d AuthDeps) createActorHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		worldID := storage.ID(r.PathValue("worldId"))

		actor, ok := d.requireRole(w, r, worldID, true)
		if !ok {
			return
		}

		var body createActorRequest
		if !decodeBody(w, r, &body) {
			return
		}
		if strings.TrimSpace(body.Name) == "" {
			body.Name = "New character"
		}
		if body.Subtype == "" {
			body.Subtype = "vampire"
		}

		data, err := json.Marshal(defaultVampireData())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal", MessageKey: "core.api.internalError"})
			return
		}

		ownership, err := json.Marshal(map[string]string{
			string(actor.ID): "owner",
			"default":        "limited",
		})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal", MessageKey: "core.api.internalError"})
			return
		}

		document := &storage.Document{
			WorldID:       worldID,
			ID:            auth.GenerateID("actor"),
			Kind:          KindActor,
			Subtype:       body.Subtype,
			Name:          strings.TrimSpace(body.Name),
			Data:          data,
			Ownership:     ownership,
			SchemaVersion: "0.1.0",
		}

		if err := d.Store.Tx(r.Context(), func(tx storage.Tx) error {
			return tx.PutDocument(r.Context(), document)
		}); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal", MessageKey: "core.api.internalError"})
			return
		}

		writeJSON(w, http.StatusCreated, actorView{
			ID: string(document.ID), Name: document.Name, Subtype: document.Subtype,
			Data: document.Data, Ownership: document.Ownership, CanEdit: true,
		})
	}
}

type documentAccessRequest struct {
	UserID string `json:"userId"`
	Level  string `json:"level"`
}

func (d AuthDeps) setDocumentAccessHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		worldID := storage.ID(r.PathValue("worldId"))
		documentID := storage.ID(r.PathValue("documentId"))

		if _, ok := d.requireRole(w, r, worldID, true); !ok {
			return
		}

		var body documentAccessRequest
		if !decodeBody(w, r, &body) {
			return
		}
		level, err := perm.ParseLevel(body.Level)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Code: "bad_request", MessageKey: "core.world.unknownLevel"})
			return
		}

		err = d.Store.Tx(r.Context(), func(tx storage.Tx) error {
			document, err := tx.GetDocument(r.Context(), worldID, documentID)
			if err != nil {
				return err
			}

			acl, err := perm.ParseACL(document.Ownership)
			if err != nil {
				return err
			}
			acl[body.UserID] = level

			encoded := make(map[string]string, len(acl))
			for key, value := range acl {
				encoded[key] = value.String()
			}

			ownership, err := json.Marshal(encoded)
			if err != nil {
				return err
			}
			document.Ownership = ownership
			return tx.PutDocument(r.Context(), document)
		})
		if errors.Is(err, storage.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, apiError{Code: "not_found", MessageKey: "core.document.unknown"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal", MessageKey: "core.api.internalError"})
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

type WallData struct {
	X1          float64 `json:"x1"`
	Y1          float64 `json:"y1"`
	X2          float64 `json:"x2"`
	Y2          float64 `json:"y2"`
	BlocksSight bool    `json:"blocksSight"`
	BlocksMove  bool    `json:"blocksMovement"`
	BlocksSound bool    `json:"blocksSound"`
	Door        bool    `json:"door"`
	DoorOpen    bool    `json:"doorOpen"`
}

type wallView struct {
	ID   string   `json:"id"`
	Data WallData `json:"data"`
}

type createWallRequest struct {
	X1          float64 `json:"x1"`
	Y1          float64 `json:"y1"`
	X2          float64 `json:"x2"`
	Y2          float64 `json:"y2"`
	Door        bool    `json:"door"`
	BlocksSight *bool   `json:"blocksSight,omitempty"`
}

func (d AuthDeps) listWallsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		worldID := storage.ID(r.PathValue("worldId"))
		sceneID := storage.ID(r.PathValue("sceneId"))

		if _, ok := d.requireRole(w, r, worldID, false); !ok {
			return
		}

		var views []wallView
		err := d.Store.ReadOnly(r.Context(), func(q storage.Query) error {
			parent := sceneID
			documents, err := q.ListDocuments(r.Context(), worldID, storage.DocumentFilter{
				Kind:     KindWall,
				ParentID: &parent,
			})
			if err != nil {
				return err
			}
			views = make([]wallView, 0, len(documents))
			for _, document := range documents {
				var data WallData
				_ = json.Unmarshal(document.Data, &data)
				views = append(views, wallView{ID: string(document.ID), Data: data})
			}
			return nil
		})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal", MessageKey: "core.api.internalError"})
			return
		}

		writeJSON(w, http.StatusOK, views)
	}
}

func (d AuthDeps) createWallHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		worldID := storage.ID(r.PathValue("worldId"))
		sceneID := storage.ID(r.PathValue("sceneId"))

		if _, ok := d.requireRole(w, r, worldID, true); !ok {
			return
		}

		var body createWallRequest
		if !decodeBody(w, r, &body) {
			return
		}
		if body.X1 == body.X2 && body.Y1 == body.Y2 {
			writeJSON(w, http.StatusBadRequest, apiError{
				Code: "bad_request", MessageKey: "core.scene.wallHasNoLength",
			})
			return
		}

		blocksSight := true
		if body.BlocksSight != nil {
			blocksSight = *body.BlocksSight
		}

		data, err := json.Marshal(WallData{
			X1: body.X1, Y1: body.Y1, X2: body.X2, Y2: body.Y2,
			BlocksSight: blocksSight, BlocksMove: true, BlocksSound: !body.Door,
			Door: body.Door,
		})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal", MessageKey: "core.api.internalError"})
			return
		}

		document := &storage.Document{
			WorldID:   worldID,
			ID:        auth.GenerateID("wall"),
			Kind:      KindWall,
			ParentID:  sceneID,
			Name:      "Wall",
			Data:      data,
			Ownership: json.RawMessage(`{"default":"observer"}`),
		}

		err = d.Store.Tx(r.Context(), func(tx storage.Tx) error {
			if _, err := tx.GetDocument(r.Context(), worldID, sceneID); err != nil {
				return err
			}
			return tx.PutDocument(r.Context(), document)
		})
		if errors.Is(err, storage.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, apiError{Code: "not_found", MessageKey: "core.scene.unknown"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal", MessageKey: "core.api.internalError"})
			return
		}

		var view WallData
		_ = json.Unmarshal(document.Data, &view)
		writeJSON(w, http.StatusCreated, wallView{ID: string(document.ID), Data: view})
	}
}

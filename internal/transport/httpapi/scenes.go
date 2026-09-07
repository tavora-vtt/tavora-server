package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/tavora-vtt/tavora-server/internal/core/auth"
	"github.com/tavora-vtt/tavora-server/internal/core/perm"
	"github.com/tavora-vtt/tavora-server/internal/storage"
)

const (
	KindScene = "scene"
	KindToken = "token"
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
	ID   string    `json:"id"`
	Name string    `json:"name"`
	Data SceneData `json:"data"`
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
			documents, err := q.ListDocuments(r.Context(), worldID, storage.DocumentFilter{Kind: KindScene})
			if err != nil {
				return err
			}
			views = make([]sceneView, 0, len(documents))
			for _, document := range documents {
				var data SceneData
				_ = json.Unmarshal(document.Data, &data)
				views = append(views, sceneView{ID: string(document.ID), Name: document.Name, Data: data})
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
		writeJSON(w, http.StatusCreated, sceneView{ID: string(document.ID), Name: document.Name, Data: view})
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

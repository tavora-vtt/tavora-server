package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/tavora-vtt/tavora-server/internal/core/asset"
	"github.com/tavora-vtt/tavora-server/internal/core/auth"
	"github.com/tavora-vtt/tavora-server/internal/core/blob"
	"github.com/tavora-vtt/tavora-server/internal/storage"
)

const (
	uploadField      = "file"
	uploadMemory     = 8 << 20
	assetCacheHeader = "private, max-age=31536000, immutable"
)

// AssetDeps carries what the asset routes need beyond the shared auth dependencies. The
// routes are only registered when a blob store is configured.
type AssetDeps struct {
	Blobs        blob.Store
	Limits       asset.Limits
	WorldQuota   int64
	AssetBaseURL string
}

type assetView struct {
	ID        string `json:"id"`
	Mime      string `json:"mime"`
	Bytes     int64  `json:"bytes"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	URL       string `json:"url"`
	Thumbnail string `json:"thumbnail,omitempty"`
	CreatedAt string `json:"createdAt"`
}

type assetVariants struct {
	Thumbnail *asset.Variant `json:"thumbnail,omitempty"`
}

func assetURL(base string, worldID, assetID storage.ID, variant string) string {
	path := "/assets/" + string(worldID) + "/" + string(assetID)
	if variant != "" {
		path += "/" + variant
	}
	return strings.TrimSuffix(base, "/") + path
}

func (d AuthDeps) viewAsset(record *storage.Asset) assetView {
	view := assetView{
		ID:        string(record.ID),
		Mime:      record.Mime,
		Bytes:     record.Bytes,
		Width:     record.Width,
		Height:    record.Height,
		URL:       assetURL(d.Assets.AssetBaseURL, record.WorldID, record.ID, ""),
		CreatedAt: record.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}

	var variants assetVariants
	if json.Unmarshal(record.Variants, &variants) == nil && variants.Thumbnail != nil {
		view.Thumbnail = assetURL(d.Assets.AssetBaseURL, record.WorldID, record.ID, "thumbnail")
	}
	return view
}

func (d AuthDeps) listAssetsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		worldID := storage.ID(r.PathValue("worldId"))
		if _, ok := d.requireRole(w, r, worldID, false); !ok {
			return
		}

		var records []storage.Asset
		err := d.Store.ReadOnly(r.Context(), func(q storage.Query) error {
			var readErr error
			records, readErr = q.ListAssets(r.Context(), worldID, 0)
			return readErr
		})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal", MessageKey: "core.api.internalError"})
			return
		}

		views := make([]assetView, 0, len(records))
		for index := range records {
			views = append(views, d.viewAsset(&records[index]))
		}
		writeJSON(w, http.StatusOK, views)
	}
}

// uploadAssetHandler reads one multipart file, re-encodes it from pixels and stores the
// result under the hash of its own bytes. Nothing the client claims about the file, its
// name or its content type takes part in any decision.
func (d AuthDeps) uploadAssetHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		worldID := storage.ID(r.PathValue("worldId"))
		user, ok := d.requireRole(w, r, worldID, true)
		if !ok {
			return
		}

		limits := d.Assets.Limits
		if limits.MaxBytes <= 0 {
			limits.MaxBytes = asset.DefaultMaxBytes
		}

		r.Body = http.MaxBytesReader(w, r.Body, limits.MaxBytes+uploadMemory)
		if err := r.ParseMultipartForm(uploadMemory); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Code: "bad_request", MessageKey: "core.asset.malformedUpload"})
			return
		}
		defer func() { _ = r.MultipartForm.RemoveAll() }()

		file, _, err := r.FormFile(uploadField)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Code: "bad_request", MessageKey: "core.asset.noFile"})
			return
		}
		defer func() { _ = file.Close() }()

		raw, err := io.ReadAll(io.LimitReader(file, limits.MaxBytes+1))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Code: "bad_request", MessageKey: "core.asset.malformedUpload"})
			return
		}

		normalized, err := asset.Normalize(raw, limits)
		switch {
		case errors.Is(err, asset.ErrUnsupportedFormat):
			writeJSON(w, http.StatusUnsupportedMediaType, apiError{Code: "unsupported", MessageKey: "core.asset.unsupportedFormat"})
			return
		case errors.Is(err, asset.ErrTooLarge):
			writeJSON(w, http.StatusRequestEntityTooLarge, apiError{Code: "too_large", MessageKey: "core.asset.tooLarge"})
			return
		case errors.Is(err, asset.ErrBroken):
			writeJSON(w, http.StatusBadRequest, apiError{Code: "bad_request", MessageKey: "core.asset.brokenImage"})
			return
		case err != nil:
			writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal", MessageKey: "core.api.internalError"})
			return
		}

		record, reused, err := d.storeAsset(r, worldID, user.ID, normalized)
		switch {
		case errors.Is(err, errQuotaExceeded):
			writeJSON(w, http.StatusRequestEntityTooLarge, apiError{Code: "quota", MessageKey: "core.asset.worldIsFull"})
			return
		case err != nil:
			writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal", MessageKey: "core.api.internalError"})
			return
		}

		status := http.StatusCreated
		if reused {
			status = http.StatusOK
		}
		writeJSON(w, status, d.viewAsset(record))
	}
}

var errQuotaExceeded = errors.New("httpapi: the world holds as much as it may")

// storeAsset writes the blobs first and the row second. A blob without a row is an orphan
// the next upload of the same content adopts, while a row without a blob would be a
// broken image, so this order is the safe one.
func (d AuthDeps) storeAsset(
	r *http.Request, worldID, userID storage.ID, normalized *asset.Normalized,
) (*storage.Asset, bool, error) {
	ctx := r.Context()

	var existing *storage.Asset
	err := d.Store.ReadOnly(ctx, func(q storage.Query) error {
		found, readErr := q.GetAssetBySHA256(ctx, worldID, normalized.SHA256)
		if errors.Is(readErr, storage.ErrNotFound) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
		existing = found
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		return existing, true, nil
	}

	if err := d.withinQuota(r, worldID, normalized.Bytes+normalized.Thumbnail.Bytes); err != nil {
		return nil, false, err
	}

	if err := d.Assets.Blobs.Put(ctx, normalized.Key, normalized.Content); err != nil {
		return nil, false, err
	}
	if err := d.Assets.Blobs.Put(ctx, normalized.Thumbnail.Key, normalized.Thumbnail.Content); err != nil {
		return nil, false, err
	}

	thumbnail := normalized.Thumbnail
	thumbnail.Content = nil
	variants, err := json.Marshal(assetVariants{Thumbnail: &thumbnail})
	if err != nil {
		return nil, false, err
	}

	record := &storage.Asset{
		ID:         auth.GenerateID("asset"),
		WorldID:    worldID,
		SHA256:     normalized.SHA256,
		Mime:       normalized.Mime,
		Bytes:      normalized.Bytes,
		Width:      normalized.Width,
		Height:     normalized.Height,
		Variants:   variants,
		UploadedBy: userID,
	}

	if err := d.Store.Tx(ctx, func(tx storage.Tx) error {
		return tx.PutAsset(ctx, record)
	}); err != nil {
		return nil, false, err
	}
	return record, false, nil
}

func (d AuthDeps) withinQuota(r *http.Request, worldID storage.ID, incoming int64) error {
	if d.Assets.WorldQuota <= 0 {
		return nil
	}

	var used int64
	err := d.Store.ReadOnly(r.Context(), func(q storage.Query) error {
		var readErr error
		used, readErr = q.SumAssetBytes(r.Context(), worldID)
		return readErr
	})
	if err != nil {
		return err
	}
	if used+incoming > d.Assets.WorldQuota {
		return errQuotaExceeded
	}
	return nil
}

// serveAssetHandler streams stored content. The headers are what keep an upload that
// somehow carries markup from ever running as a document.
func (d AuthDeps) serveAssetHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		worldID := storage.ID(r.PathValue("worldId"))
		if _, ok := d.requireRole(w, r, worldID, false); !ok {
			return
		}

		var record *storage.Asset
		err := d.Store.ReadOnly(r.Context(), func(q storage.Query) error {
			var readErr error
			record, readErr = q.GetAsset(r.Context(), worldID, storage.ID(r.PathValue("assetId")))
			return readErr
		})
		if errors.Is(err, storage.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, apiError{Code: "not_found", MessageKey: "core.asset.unknown"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal", MessageKey: "core.api.internalError"})
			return
		}

		key, mime := record.SHA256, record.Mime
		if r.PathValue("variant") == "thumbnail" {
			var variants assetVariants
			if json.Unmarshal(record.Variants, &variants) == nil && variants.Thumbnail != nil {
				key, mime = variants.Thumbnail.Key, variants.Thumbnail.Mime
			}
		}

		content, _, err := d.Assets.Blobs.Open(r.Context(), key)
		if errors.Is(err, blob.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, apiError{Code: "not_found", MessageKey: "core.asset.unknown"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal", MessageKey: "core.api.internalError"})
			return
		}
		defer func() { _ = content.Close() }()

		header := w.Header()
		header.Set("Content-Type", mime)
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("Content-Security-Policy", "default-src 'none'; sandbox")
		header.Set("Cross-Origin-Resource-Policy", "same-site")
		header.Set("Cache-Control", assetCacheHeader)
		header.Set("ETag", `"`+key+`"`)

		http.ServeContent(w, r, "", record.CreatedAt, content)
	}
}

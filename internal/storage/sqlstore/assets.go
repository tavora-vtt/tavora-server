package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/tavora-vtt/tavora-server/internal/storage"
)

const assetColumns = `id, world_id, sha256, mime, bytes, width, height, duration_ms, %s, uploaded_by, created_at`

const defaultAssetLimit = 200

func (c *conn) scanAsset(scan func(...any) error) (*storage.Asset, error) {
	var asset storage.Asset
	err := scan(
		&asset.ID, &asset.WorldID, &asset.SHA256, &asset.Mime, &asset.Bytes,
		&asset.Width, &asset.Height, &asset.DurationMS,
		jsonScanner{dst: &asset.Variants, fallback: "{}"},
		&asset.UploadedBy,
		timeScanner{dst: &asset.CreatedAt},
	)
	if err != nil {
		return nil, err
	}
	return &asset, nil
}

func (c *conn) selectAssets() string {
	return fmt.Sprintf(`SELECT `+assetColumns+` FROM assets`, c.dialect.JSONColumn("variants"))
}

func (c *conn) GetAsset(ctx context.Context, worldID, id storage.ID) (*storage.Asset, error) {
	asset, err := c.scanAsset(c.queryRow(ctx,
		c.selectAssets()+` WHERE world_id = ? AND id = ?`, worldID, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: asset", storage.ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	return asset, nil
}

func (c *conn) GetAssetBySHA256(ctx context.Context, worldID storage.ID, sha256 string) (*storage.Asset, error) {
	asset, err := c.scanAsset(c.queryRow(ctx,
		c.selectAssets()+` WHERE world_id = ? AND sha256 = ?`, worldID, sha256).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: asset", storage.ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	return asset, nil
}

func (c *conn) ListAssets(ctx context.Context, worldID storage.ID, limit int) ([]storage.Asset, error) {
	if limit <= 0 || limit > defaultAssetLimit {
		limit = defaultAssetLimit
	}

	rows, err := c.query(ctx,
		c.selectAssets()+` WHERE world_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`,
		worldID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	assets := make([]storage.Asset, 0, 16)
	for rows.Next() {
		asset, err := c.scanAsset(rows.Scan)
		if err != nil {
			return nil, err
		}
		assets = append(assets, *asset)
	}
	return assets, rows.Err()
}

func (c *conn) SumAssetBytes(ctx context.Context, worldID storage.ID) (int64, error) {
	var total int64
	err := c.queryRow(ctx,
		`SELECT COALESCE(SUM(bytes), 0) FROM assets WHERE world_id = ?`, worldID).Scan(&total)
	return total, err
}

func (c *conn) PutAsset(ctx context.Context, asset *storage.Asset) error {
	if err := c.requireWritable(); err != nil {
		return err
	}
	if asset.CreatedAt.IsZero() {
		asset.CreatedAt = time.Now().UTC()
	}

	_, err := c.exec(ctx,
		`INSERT INTO assets (id, world_id, sha256, mime, bytes, width, height, duration_ms, variants, uploaded_by, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (id) DO UPDATE SET
		   mime = excluded.mime, bytes = excluded.bytes,
		   width = excluded.width, height = excluded.height,
		   duration_ms = excluded.duration_ms, variants = excluded.variants`,
		asset.ID, asset.WorldID, asset.SHA256, asset.Mime, asset.Bytes,
		asset.Width, asset.Height, asset.DurationMS,
		jsonArg(asset.Variants, "{}"), asset.UploadedBy,
		c.dialect.TimeArg(asset.CreatedAt),
	)
	if c.dialect.IsUniqueViolation(err) {
		return fmt.Errorf("%w: asset", storage.ErrAlreadyExists)
	}
	return err
}

func (c *conn) DeleteAsset(ctx context.Context, worldID, id storage.ID) error {
	if err := c.requireWritable(); err != nil {
		return err
	}

	result, err := c.exec(ctx, `DELETE FROM assets WHERE world_id = ? AND id = ?`, worldID, id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("%w: asset", storage.ErrNotFound)
	}
	return nil
}

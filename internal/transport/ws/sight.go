package ws

import (
	"context"
	"encoding/json"

	"github.com/tavora-vtt/tavora-server/internal/core/access"
	"github.com/tavora-vtt/tavora-server/internal/core/perm"
	"github.com/tavora-vtt/tavora-server/internal/core/vision"
	"github.com/tavora-vtt/tavora-server/internal/storage"
)

type tokenGeometry struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type wallGeometry struct {
	X1          float64 `json:"x1"`
	Y1          float64 `json:"y1"`
	X2          float64 `json:"x2"`
	Y2          float64 `json:"y2"`
	BlocksSight bool    `json:"blocksSight"`
	Door        bool    `json:"door"`
	DoorOpen    bool    `json:"doorOpen"`
}

func blockersOf(ctx context.Context, q storage.Query, worldID, sceneID storage.ID) ([]vision.Segment, error) {
	parent := sceneID
	walls, err := q.ListDocuments(ctx, worldID, storage.DocumentFilter{
		Kind:     "wall",
		ParentID: &parent,
	})
	if err != nil {
		return nil, err
	}

	blockers := make([]vision.Segment, 0, len(walls))
	for _, wall := range walls {
		var data wallGeometry
		if err := json.Unmarshal(wall.Data, &data); err != nil {
			continue
		}
		if !data.BlocksSight || (data.Door && data.DoorOpen) {
			continue
		}
		blockers = append(blockers, vision.Segment{
			A: vision.Point{X: data.X1, Y: data.Y1},
			B: vision.Point{X: data.X2, Y: data.Y2},
		})
	}
	return blockers, nil
}

func viewpointsOf(
	ctx context.Context,
	q storage.Query,
	resolver *access.Resolver,
	subject perm.Subject,
	worldID, sceneID storage.ID,
) ([]vision.Point, error) {
	parent := sceneID
	tokens, err := q.ListDocuments(ctx, worldID, storage.DocumentFilter{
		Kind:     "token",
		ParentID: &parent,
	})
	if err != nil {
		return nil, err
	}

	points := make([]vision.Point, 0, 4)
	for _, token := range tokens {
		grant, err := resolver.Grant(ctx, q, subject, token)
		if err != nil {
			return nil, err
		}
		if grant.Level < perm.LevelOwner {
			continue
		}

		point, ok := pointOf(token)
		if ok {
			points = append(points, point)
		}
	}
	return points, nil
}

func pointOf(token *storage.Document) (vision.Point, bool) {
	var geometry tokenGeometry
	if err := json.Unmarshal(token.Data, &geometry); err != nil {
		return vision.Point{}, false
	}
	return vision.Point{X: geometry.X, Y: geometry.Y}, true
}

func withinSight(
	ctx context.Context,
	q storage.Query,
	resolver *access.Resolver,
	worldID storage.ID,
	token *storage.Document,
	views map[storage.ID]access.View,
) (map[storage.ID]access.View, error) {
	if token.Kind != "token" || token.ParentID == "" {
		return views, nil
	}

	target, ok := pointOf(token)
	if !ok {
		return views, nil
	}

	blockers, err := blockersOf(ctx, q, worldID, token.ParentID)
	if err != nil {
		return nil, err
	}
	if len(blockers) == 0 {
		return views, nil
	}

	filtered := make(map[storage.ID]access.View, len(views))
	for userID, view := range views {
		if view.Subject.Role.IsStaff() {
			filtered[userID] = view
			continue
		}

		points, err := viewpointsOf(ctx, q, resolver, view.Subject, worldID, token.ParentID)
		if err != nil {
			return nil, err
		}
		if vision.VisibleFromAny(points, target, blockers) {
			filtered[userID] = view
		}
	}
	return filtered, nil
}

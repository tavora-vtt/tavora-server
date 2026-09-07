package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/tavora-vtt/tavora-server/internal/storage"
)

const worldID = storage.ID("world-1")

type Factory func(t *testing.T) storage.Store

func Run(t *testing.T, newStore Factory) {
	tests := []struct {
		name string
		fn   func(t *testing.T, store storage.Store)
	}{
		{"WorldRoundTrip", testWorldRoundTrip},
		{"MemberRoundTrip", testMemberRoundTrip},
		{"UserRoundTrip", testUserRoundTrip},
		{"UserSessionLifecycle", testUserSessionLifecycle},
		{"DeletingAUserRemovesItsSessions", testUserCascade},
		{"WorldListings", testWorldListings},
		{"InviteLifecycle", testInviteLifecycle},
		{"MissingMemberIsNotFound", testMissingMemberIsNotFound},
		{"DocumentRoundTrip", testDocumentRoundTrip},
		{"UpsertPreservesCreatedAt", testUpsertPreservesCreatedAt},
		{"MissingDocumentIsNotFound", testMissingDocumentIsNotFound},
		{"PatchScalarFields", testPatchScalarFields},
		{"PatchSetsAndUnsetsJSONPaths", testPatchJSONPaths},
		{"PatchRejectsInvalidPath", testPatchRejectsInvalidPath},
		{"SoftDeleteHidesDocument", testSoftDelete},
		{"HardDeleteRemovesChildren", testHardDeleteRemovesChildren},
		{"ListFilters", testListFilters},
		{"QueryText", testQueryText},
		{"QueryNumber", testQueryNumber},
		{"QueryBool", testQueryBool},
		{"QueryExists", testQueryExists},
		{"QueryArrayContains", testQueryArrayContains},
		{"QueryIgnoresMistypedValues", testQueryIgnoresMistypedValues},
		{"EventSequenceIsMonotonic", testEventSequence},
		{"EventsSince", testEventsSince},
		{"TransactionRollsBack", testTransactionRollback},
		{"ReadOnlyRejectsWrites", testReadOnlyRejectsWrites},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newStore(t)
			ctx := context.Background()
			if err := store.Migrate(ctx); err != nil {
				t.Fatalf("migrate: %v", err)
			}
			seedWorld(t, store)
			test.fn(t, store)
		})
	}
}

func seedWorld(t *testing.T, store storage.Store) {
	t.Helper()
	err := store.Tx(context.Background(), func(tx storage.Tx) error {
		return tx.PutWorld(context.Background(), &storage.World{
			ID:            worldID,
			Slug:          "blood-and-rain",
			Title:         "Blood and Rain",
			SystemID:      "wod5e",
			SystemVersion: "0.0.0",
		})
	})
	if err != nil {
		t.Fatalf("seed world: %v", err)
	}
}

func put(t *testing.T, store storage.Store, docs ...*storage.Document) {
	t.Helper()
	ctx := context.Background()
	err := store.Tx(ctx, func(tx storage.Tx) error {
		for _, doc := range docs {
			if err := tx.PutDocument(ctx, doc); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("put documents: %v", err)
	}
}

func actor(id, name string, data string) *storage.Document {
	return &storage.Document{
		WorldID:       worldID,
		ID:            storage.ID(id),
		Kind:          "actor",
		Subtype:       "vampire",
		Name:          name,
		Data:          json.RawMessage(data),
		SchemaVersion: "1.0.0",
	}
}

func decode(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return out
}

func ids(documents []*storage.Document) []string {
	out := make([]string, 0, len(documents))
	for _, doc := range documents {
		out = append(out, string(doc.ID))
	}
	return out
}

func testWorldRoundTrip(t *testing.T, store storage.Store) {
	ctx := context.Background()

	var world *storage.World
	err := store.ReadOnly(ctx, func(q storage.Query) error {
		var err error
		world, err = q.GetWorld(ctx, worldID)
		return err
	})
	if err != nil {
		t.Fatalf("get world: %v", err)
	}
	if world.Slug != "blood-and-rain" || world.SystemID != "wod5e" {
		t.Errorf("world = %+v", world)
	}
	if world.DefaultLocale != "en" {
		t.Errorf("default locale = %q", world.DefaultLocale)
	}
	if world.CreatedAt.IsZero() {
		t.Error("created at not set")
	}
}

func testDocumentRoundTrip(t *testing.T, store storage.Store) {
	ctx := context.Background()

	source := actor("actor-1", "Nadia Kovac", `{"hunger":2,"attributes":{"strength":3}}`)
	source.Flags = json.RawMessage(`{"tavora-dice-tray":{"pinned":true}}`)
	source.Ownership = json.RawMessage(`{"user-1":"owner"}`)
	put(t, store, source)

	var loaded *storage.Document
	if err := store.ReadOnly(ctx, func(q storage.Query) error {
		var err error
		loaded, err = q.GetDocument(ctx, worldID, "actor-1")
		return err
	}); err != nil {
		t.Fatalf("get: %v", err)
	}

	if loaded.Name != "Nadia Kovac" || loaded.Kind != "actor" || loaded.Subtype != "vampire" {
		t.Errorf("document = %+v", loaded)
	}
	if got := decode(t, loaded.Data)["hunger"]; got != float64(2) {
		t.Errorf("hunger = %v", got)
	}
	if got := decode(t, loaded.Flags)["tavora-dice-tray"]; got == nil {
		t.Error("flags lost")
	}
	if got := decode(t, loaded.Ownership)["user-1"]; got != "owner" {
		t.Errorf("ownership = %v", got)
	}
	if loaded.CreatedAt.IsZero() || loaded.UpdatedAt.IsZero() {
		t.Error("timestamps not set")
	}
	if loaded.DeletedAt != nil {
		t.Error("deleted at should be nil")
	}
}

func testUpsertPreservesCreatedAt(t *testing.T, store storage.Store) {
	ctx := context.Background()

	put(t, store, actor("actor-1", "First", `{"hunger":1}`))

	var first *storage.Document
	_ = store.ReadOnly(ctx, func(q storage.Query) error {
		var err error
		first, err = q.GetDocument(ctx, worldID, "actor-1")
		return err
	})

	put(t, store, actor("actor-1", "Second", `{"hunger":4}`))

	var second *storage.Document
	_ = store.ReadOnly(ctx, func(q storage.Query) error {
		var err error
		second, err = q.GetDocument(ctx, worldID, "actor-1")
		return err
	})

	if second.Name != "Second" {
		t.Errorf("name = %q", second.Name)
	}
	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("created at changed: %v then %v", first.CreatedAt, second.CreatedAt)
	}
}

func testMissingDocumentIsNotFound(t *testing.T, store storage.Store) {
	ctx := context.Background()

	err := store.ReadOnly(ctx, func(q storage.Query) error {
		_, err := q.GetDocument(ctx, worldID, "nope")
		return err
	})
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func testPatchScalarFields(t *testing.T, store storage.Store) {
	ctx := context.Background()
	put(t, store, actor("actor-1", "Nadia", `{"hunger":2}`))

	name := "Nadia Kovac"
	sort := 42
	folder := storage.ID("folder-1")

	var patched *storage.Document
	err := store.Tx(ctx, func(tx storage.Tx) error {
		var err error
		patched, err = tx.PatchDocument(ctx, worldID, "actor-1", storage.Patch{
			Name:     &name,
			Sort:     &sort,
			FolderID: &folder,
			Seq:      7,
		})
		return err
	})
	if err != nil {
		t.Fatalf("patch: %v", err)
	}

	if patched.Name != name || patched.Sort != sort || patched.FolderID != folder {
		t.Errorf("patched = %+v", patched)
	}
	if patched.UpdatedSeq != 7 {
		t.Errorf("updated seq = %d", patched.UpdatedSeq)
	}
	if got := decode(t, patched.Data)["hunger"]; got != float64(2) {
		t.Errorf("data disturbed: %v", got)
	}
}

func testPatchJSONPaths(t *testing.T, store storage.Store) {
	ctx := context.Background()
	put(t, store, actor("actor-1", "Nadia", `{"hunger":2,"attributes":{"strength":3,"dexterity":2},"stale":true}`))

	var patched *storage.Document
	err := store.Tx(ctx, func(tx storage.Tx) error {
		var err error
		patched, err = tx.PatchDocument(ctx, worldID, "actor-1", storage.Patch{
			Set: map[string]any{
				"hunger":               4,
				"attributes.strength":  5,
				"attributes.composure": 1,
				"conditions":           []string{"hungry", "wounded"},
			},
			Unset: []string{"stale", "attributes.dexterity"},
			Seq:   3,
		})
		return err
	})
	if err != nil {
		t.Fatalf("patch: %v", err)
	}

	data := decode(t, patched.Data)
	if data["hunger"] != float64(4) {
		t.Errorf("hunger = %v", data["hunger"])
	}
	if _, present := data["stale"]; present {
		t.Error("stale not removed")
	}

	attributes, _ := data["attributes"].(map[string]any)
	if attributes["strength"] != float64(5) {
		t.Errorf("strength = %v", attributes["strength"])
	}
	if attributes["composure"] != float64(1) {
		t.Errorf("composure = %v", attributes["composure"])
	}
	if _, present := attributes["dexterity"]; present {
		t.Error("dexterity not removed")
	}

	conditions, _ := data["conditions"].([]any)
	if !reflect.DeepEqual(conditions, []any{"hungry", "wounded"}) {
		t.Errorf("conditions = %v", conditions)
	}
}

func testPatchRejectsInvalidPath(t *testing.T, store storage.Store) {
	ctx := context.Background()
	put(t, store, actor("actor-1", "Nadia", `{"hunger":2}`))

	err := store.Tx(ctx, func(tx storage.Tx) error {
		_, err := tx.PatchDocument(ctx, worldID, "actor-1", storage.Patch{
			Set: map[string]any{"hunger'); DROP TABLE documents; --": 1},
		})
		return err
	})
	if !errors.Is(err, storage.ErrInvalidPath) {
		t.Errorf("err = %v, want ErrInvalidPath", err)
	}
}

func testSoftDelete(t *testing.T, store storage.Store) {
	ctx := context.Background()
	put(t, store, actor("actor-1", "Nadia", `{}`), actor("actor-2", "Tomas", `{}`))

	if err := store.Tx(ctx, func(tx storage.Tx) error {
		return tx.DeleteDocument(ctx, worldID, "actor-1", storage.DeleteSoft)
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}

	var visible, all []*storage.Document
	_ = store.ReadOnly(ctx, func(q storage.Query) error {
		visible, _ = q.ListDocuments(ctx, worldID, storage.DocumentFilter{Kind: "actor"})
		all, _ = q.ListDocuments(ctx, worldID, storage.DocumentFilter{Kind: "actor", IncludeDeleted: true})
		return nil
	})

	if !reflect.DeepEqual(ids(visible), []string{"actor-2"}) {
		t.Errorf("visible = %v", ids(visible))
	}
	if len(all) != 2 {
		t.Errorf("with deleted = %v", ids(all))
	}

	var deleted *storage.Document
	_ = store.ReadOnly(ctx, func(q storage.Query) error {
		var err error
		deleted, err = q.GetDocument(ctx, worldID, "actor-1")
		return err
	})
	if deleted.DeletedAt == nil {
		t.Error("deleted at not set")
	}
}

func testHardDeleteRemovesChildren(t *testing.T, store storage.Store) {
	ctx := context.Background()

	parent := actor("actor-1", "Nadia", `{}`)
	child := &storage.Document{
		WorldID: worldID, ID: "item-1", Kind: "item", Subtype: "discipline",
		ParentID: "actor-1", Name: "Presence", Data: json.RawMessage(`{}`),
	}
	put(t, store, parent, child)

	if err := store.Tx(ctx, func(tx storage.Tx) error {
		return tx.DeleteDocument(ctx, worldID, "actor-1", storage.DeleteHard)
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}

	err := store.ReadOnly(ctx, func(q storage.Query) error {
		_, err := q.GetDocument(ctx, worldID, "item-1")
		return err
	})
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("child survived hard delete: %v", err)
	}
}

func testListFilters(t *testing.T, store storage.Store) {
	ctx := context.Background()

	first := actor("actor-1", "Nadia", `{}`)
	first.Sort = 20
	second := actor("actor-2", "Tomas", `{}`)
	second.Sort = 10
	item := &storage.Document{
		WorldID: worldID, ID: "item-1", Kind: "item", Subtype: "weapon",
		ParentID: "actor-1", FolderID: "folder-1", Name: "Dagger", Data: json.RawMessage(`{}`),
	}
	put(t, store, first, second, item)

	var byKind, byParent, byFolder, bySubtype []*storage.Document
	_ = store.ReadOnly(ctx, func(q storage.Query) error {
		byKind, _ = q.ListDocuments(ctx, worldID, storage.DocumentFilter{Kind: "actor"})
		parent := storage.ID("actor-1")
		byParent, _ = q.ListDocuments(ctx, worldID, storage.DocumentFilter{ParentID: &parent})
		folder := storage.ID("folder-1")
		byFolder, _ = q.ListDocuments(ctx, worldID, storage.DocumentFilter{FolderID: &folder})
		bySubtype, _ = q.ListDocuments(ctx, worldID, storage.DocumentFilter{Subtype: "weapon"})
		return nil
	})

	if !reflect.DeepEqual(ids(byKind), []string{"actor-2", "actor-1"}) {
		t.Errorf("kind filter, sort order = %v", ids(byKind))
	}
	if !reflect.DeepEqual(ids(byParent), []string{"item-1"}) {
		t.Errorf("parent filter = %v", ids(byParent))
	}
	if !reflect.DeepEqual(ids(byFolder), []string{"item-1"}) {
		t.Errorf("folder filter = %v", ids(byFolder))
	}
	if !reflect.DeepEqual(ids(bySubtype), []string{"item-1"}) {
		t.Errorf("subtype filter = %v", ids(bySubtype))
	}
}

func queryIDs(t *testing.T, store storage.Store, spec storage.JSONQuery) []string {
	t.Helper()
	ctx := context.Background()

	var found []*storage.Document
	if err := store.ReadOnly(ctx, func(q storage.Query) error {
		var err error
		found, err = q.QuerySystemData(ctx, worldID, spec)
		return err
	}); err != nil {
		t.Fatalf("query: %v", err)
	}
	return ids(found)
}

func seedQueryFixtures(t *testing.T, store storage.Store) {
	put(t, store,
		actor("actor-1", "Nadia", `{"clan":"Ventrue","hunger":2,"embraced":true,"tags":["kindred","elder"]}`),
		actor("actor-2", "Tomas", `{"clan":"Brujah","hunger":5,"embraced":false,"tags":["kindred"]}`),
		actor("actor-3", "Mira", `{"clan":"Ventrue","hunger":"unknown","tags":[]}`),
	)
}

func testQueryText(t *testing.T, store storage.Store) {
	seedQueryFixtures(t, store)

	got := queryIDs(t, store, storage.JSONQuery{
		Kind:       "actor",
		Conditions: []storage.JSONCondition{{Path: "clan", Op: storage.OpEqual, Value: "Ventrue"}},
	})
	if !reflect.DeepEqual(got, []string{"actor-1", "actor-3"}) {
		t.Errorf("equality = %v", got)
	}

	got = queryIDs(t, store, storage.JSONQuery{
		Kind:       "actor",
		Conditions: []storage.JSONCondition{{Path: "clan", Op: storage.OpNotEqual, Value: "Ventrue"}},
	})
	if !reflect.DeepEqual(got, []string{"actor-2"}) {
		t.Errorf("inequality = %v", got)
	}
}

func testQueryNumber(t *testing.T, store storage.Store) {
	seedQueryFixtures(t, store)

	got := queryIDs(t, store, storage.JSONQuery{
		Kind:       "actor",
		Conditions: []storage.JSONCondition{{Path: "hunger", Op: storage.OpGreaterEqual, Value: 3}},
	})
	if !reflect.DeepEqual(got, []string{"actor-2"}) {
		t.Errorf("gte = %v", got)
	}

	got = queryIDs(t, store, storage.JSONQuery{
		Kind: "actor",
		Conditions: []storage.JSONCondition{
			{Path: "hunger", Op: storage.OpLess, Value: 3},
			{Path: "clan", Op: storage.OpEqual, Value: "Ventrue"},
		},
	})
	if !reflect.DeepEqual(got, []string{"actor-1"}) {
		t.Errorf("conjunction = %v", got)
	}
}

func testQueryBool(t *testing.T, store storage.Store) {
	seedQueryFixtures(t, store)

	got := queryIDs(t, store, storage.JSONQuery{
		Kind:       "actor",
		Conditions: []storage.JSONCondition{{Path: "embraced", Op: storage.OpEqual, Value: true}},
	})
	if !reflect.DeepEqual(got, []string{"actor-1"}) {
		t.Errorf("true = %v", got)
	}

	got = queryIDs(t, store, storage.JSONQuery{
		Kind:       "actor",
		Conditions: []storage.JSONCondition{{Path: "embraced", Op: storage.OpEqual, Value: false}},
	})
	if !reflect.DeepEqual(got, []string{"actor-2"}) {
		t.Errorf("false = %v", got)
	}
}

func testQueryExists(t *testing.T, store storage.Store) {
	seedQueryFixtures(t, store)

	got := queryIDs(t, store, storage.JSONQuery{
		Kind:       "actor",
		Conditions: []storage.JSONCondition{{Path: "embraced", Op: storage.OpExists}},
	})
	if !reflect.DeepEqual(got, []string{"actor-1", "actor-2"}) {
		t.Errorf("exists = %v", got)
	}
}

func testQueryArrayContains(t *testing.T, store storage.Store) {
	seedQueryFixtures(t, store)

	got := queryIDs(t, store, storage.JSONQuery{
		Kind:       "actor",
		Conditions: []storage.JSONCondition{{Path: "tags", Op: storage.OpContains, Value: "elder"}},
	})
	if !reflect.DeepEqual(got, []string{"actor-1"}) {
		t.Errorf("contains elder = %v", got)
	}

	got = queryIDs(t, store, storage.JSONQuery{
		Kind:       "actor",
		Conditions: []storage.JSONCondition{{Path: "tags", Op: storage.OpContains, Value: "kindred"}},
	})
	if !reflect.DeepEqual(got, []string{"actor-1", "actor-2"}) {
		t.Errorf("contains kindred = %v", got)
	}
}

func testQueryIgnoresMistypedValues(t *testing.T, store storage.Store) {
	seedQueryFixtures(t, store)

	got := queryIDs(t, store, storage.JSONQuery{
		Kind:       "actor",
		Conditions: []storage.JSONCondition{{Path: "hunger", Op: storage.OpGreaterEqual, Value: 0}},
	})
	if !reflect.DeepEqual(got, []string{"actor-1", "actor-2"}) {
		t.Errorf("string hunger should not compare as a number: %v", got)
	}
}

func testEventSequence(t *testing.T, store storage.Store) {
	ctx := context.Background()

	var sequences []int64
	err := store.Tx(ctx, func(tx storage.Tx) error {
		for i := 0; i < 3; i++ {
			seq, err := tx.AppendEvent(ctx, storage.Event{
				WorldID: worldID,
				Kind:    "scene.token.move",
				Payload: json.RawMessage(`{"x":1}`),
			})
			if err != nil {
				return err
			}
			sequences = append(sequences, seq)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if !reflect.DeepEqual(sequences, []int64{1, 2, 3}) {
		t.Errorf("sequences = %v", sequences)
	}

	var world *storage.World
	_ = store.ReadOnly(ctx, func(q storage.Query) error {
		var err error
		world, err = q.GetWorld(ctx, worldID)
		return err
	})
	if world.EventSeq != 3 {
		t.Errorf("world event seq = %d", world.EventSeq)
	}
}

func testEventsSince(t *testing.T, store storage.Store) {
	ctx := context.Background()

	err := store.Tx(ctx, func(tx storage.Tx) error {
		for i := 0; i < 5; i++ {
			if _, err := tx.AppendEvent(ctx, storage.Event{
				WorldID:     worldID,
				ActorUserID: "user-1",
				Kind:        "scene.token.move",
				TargetKind:  "token",
				TargetID:    storage.ID("token-1"),
				SceneID:     "scene-1",
				Payload:     json.RawMessage(`{"x":1,"y":2}`),
				Audience:    json.RawMessage(`["user-1","gm"]`),
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	var events []storage.Event
	if err := store.ReadOnly(ctx, func(q storage.Query) error {
		var err error
		events, err = q.EventsSince(ctx, worldID, 2, 10)
		return err
	}); err != nil {
		t.Fatalf("events since: %v", err)
	}

	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}
	if events[0].Seq != 3 || events[2].Seq != 5 {
		t.Errorf("sequences = %d..%d", events[0].Seq, events[2].Seq)
	}
	if events[0].ActorUserID != "user-1" || events[0].SceneID != "scene-1" {
		t.Errorf("event = %+v", events[0])
	}
	if events[0].Timestamp.IsZero() {
		t.Error("timestamp not set")
	}
	if got := decode(t, events[0].Payload)["y"]; got != float64(2) {
		t.Errorf("payload = %v", got)
	}

	var limited []storage.Event
	_ = store.ReadOnly(ctx, func(q storage.Query) error {
		var err error
		limited, err = q.EventsSince(ctx, worldID, 0, 2)
		return err
	})
	if len(limited) != 2 {
		t.Errorf("limit ignored, got %d", len(limited))
	}
}

func testTransactionRollback(t *testing.T, store storage.Store) {
	ctx := context.Background()
	sentinel := errors.New("deliberate")

	err := store.Tx(ctx, func(tx storage.Tx) error {
		if err := tx.PutDocument(ctx, actor("actor-1", "Nadia", `{}`)); err != nil {
			return err
		}
		if _, err := tx.AppendEvent(ctx, storage.Event{WorldID: worldID, Kind: "actor.create"}); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v", err)
	}

	err = store.ReadOnly(ctx, func(q storage.Query) error {
		_, err := q.GetDocument(ctx, worldID, "actor-1")
		return err
	})
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("document survived rollback: %v", err)
	}

	var world *storage.World
	_ = store.ReadOnly(ctx, func(q storage.Query) error {
		var err error
		world, err = q.GetWorld(ctx, worldID)
		return err
	})
	if world.EventSeq != 0 {
		t.Errorf("event sequence survived rollback: %d", world.EventSeq)
	}
}

func testReadOnlyRejectsWrites(t *testing.T, store storage.Store) {
	ctx := context.Background()

	err := store.ReadOnly(ctx, func(q storage.Query) error {
		tx, ok := q.(storage.Tx)
		if !ok {
			return nil
		}
		return tx.PutDocument(ctx, actor("actor-1", "Nadia", `{}`))
	})
	if err != nil && !errors.Is(err, storage.ErrReadOnly) {
		t.Errorf("err = %v, want ErrReadOnly or nil", err)
	}

	err = store.ReadOnly(ctx, func(q storage.Query) error {
		_, getErr := q.GetDocument(ctx, worldID, "actor-1")
		return getErr
	})
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("read only transaction wrote a document: %v", err)
	}
}

func testMemberRoundTrip(t *testing.T, store storage.Store) {
	ctx := context.Background()

	err := store.Tx(ctx, func(tx storage.Tx) error {
		for _, member := range []storage.Member{
			{WorldID: worldID, UserID: "user-1", Role: "gm"},
			{WorldID: worldID, UserID: "user-2", Role: "player"},
		} {
			if err := tx.PutMember(ctx, &member); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("put members: %v", err)
	}

	var member *storage.Member
	var members []storage.Member
	err = store.ReadOnly(ctx, func(q storage.Query) error {
		var readErr error
		if member, readErr = q.GetMember(ctx, worldID, "user-1"); readErr != nil {
			return readErr
		}
		members, readErr = q.ListMembers(ctx, worldID)
		return readErr
	})
	if err != nil {
		t.Fatalf("read members: %v", err)
	}

	if member.Role != "gm" {
		t.Errorf("role = %q", member.Role)
	}
	if member.JoinedAt.IsZero() {
		t.Error("joined at not set")
	}
	if len(members) != 2 || members[0].UserID != "user-1" || members[1].UserID != "user-2" {
		t.Errorf("members = %+v", members)
	}

	err = store.Tx(ctx, func(tx storage.Tx) error {
		return tx.PutMember(ctx, &storage.Member{WorldID: worldID, UserID: "user-2", Role: "assistant"})
	})
	if err != nil {
		t.Fatalf("promote member: %v", err)
	}

	_ = store.ReadOnly(ctx, func(q storage.Query) error {
		var readErr error
		member, readErr = q.GetMember(ctx, worldID, "user-2")
		return readErr
	})
	if member.Role != "assistant" {
		t.Errorf("role after promotion = %q", member.Role)
	}
}

func testMissingMemberIsNotFound(t *testing.T, store storage.Store) {
	ctx := context.Background()

	err := store.ReadOnly(ctx, func(q storage.Query) error {
		_, err := q.GetMember(ctx, worldID, "stranger")
		return err
	})
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func testUserRoundTrip(t *testing.T, store storage.Store) {
	ctx := context.Background()

	err := store.Tx(ctx, func(tx storage.Tx) error {
		return tx.PutUser(ctx, &storage.User{
			ID: "user-1", Username: "nadia", Email: "nadia@example.org",
			PasswordHash: "$argon2id$fake", Locale: "de", IsAdmin: true,
		})
	})
	if err != nil {
		t.Fatalf("put user: %v", err)
	}

	var byID, byName *storage.User
	var count int
	err = store.ReadOnly(ctx, func(q storage.Query) error {
		var readErr error
		if byID, readErr = q.GetUser(ctx, "user-1"); readErr != nil {
			return readErr
		}
		if byName, readErr = q.GetUserByUsername(ctx, "nadia"); readErr != nil {
			return readErr
		}
		count, readErr = q.CountUsers(ctx)
		return readErr
	})
	if err != nil {
		t.Fatalf("read user: %v", err)
	}

	if byID.Username != "nadia" || byID.Email != "nadia@example.org" || !byID.IsAdmin {
		t.Errorf("user = %+v", byID)
	}
	if byName.ID != byID.ID {
		t.Errorf("lookup by name returned %s", byName.ID)
	}
	if byID.CreatedAt.IsZero() {
		t.Error("created at not set")
	}
	if byID.Disabled() {
		t.Error("user should not be disabled")
	}
	if count != 1 {
		t.Errorf("count = %d", count)
	}

	disabled := byID.CreatedAt
	byID.DisabledAt = &disabled
	byID.IsAdmin = false
	if err := store.Tx(ctx, func(tx storage.Tx) error { return tx.PutUser(ctx, byID) }); err != nil {
		t.Fatalf("disable: %v", err)
	}

	_ = store.ReadOnly(ctx, func(q storage.Query) error {
		var readErr error
		byID, readErr = q.GetUser(ctx, "user-1")
		return readErr
	})
	if !byID.Disabled() || byID.IsAdmin {
		t.Errorf("update lost: %+v", byID)
	}
}

func testUserSessionLifecycle(t *testing.T, store storage.Store) {
	ctx := context.Background()

	err := store.Tx(ctx, func(tx storage.Tx) error {
		if err := tx.PutUser(ctx, &storage.User{ID: "user-1", Username: "nadia"}); err != nil {
			return err
		}
		return tx.PutUserSession(ctx, &storage.UserSession{
			TokenHash: "hash-1", UserID: "user-1",
			ExpiresAt: time.Now().UTC().Add(time.Hour), UserAgent: "go-test",
		})
	})
	if err != nil {
		t.Fatalf("put session: %v", err)
	}

	var session *storage.UserSession
	if err := store.ReadOnly(ctx, func(q storage.Query) error {
		var readErr error
		session, readErr = q.GetUserSession(ctx, "hash-1")
		return readErr
	}); err != nil {
		t.Fatalf("get session: %v", err)
	}
	if session.UserID != "user-1" || session.UserAgent != "go-test" {
		t.Errorf("session = %+v", session)
	}
	if session.ExpiresAt.Before(session.CreatedAt) {
		t.Error("expiry is before creation")
	}

	seen := time.Now().UTC().Add(time.Minute)
	if err := store.Tx(ctx, func(tx storage.Tx) error {
		return tx.TouchUserSession(ctx, "hash-1", seen)
	}); err != nil {
		t.Fatalf("touch: %v", err)
	}

	_ = store.ReadOnly(ctx, func(q storage.Query) error {
		var readErr error
		session, readErr = q.GetUserSession(ctx, "hash-1")
		return readErr
	})
	if !session.LastSeenAt.After(session.CreatedAt) {
		t.Errorf("last seen not advanced: %v", session.LastSeenAt)
	}

	if err := store.Tx(ctx, func(tx storage.Tx) error {
		return tx.DeleteUserSession(ctx, "hash-1")
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}

	err = store.ReadOnly(ctx, func(q storage.Query) error {
		_, readErr := q.GetUserSession(ctx, "hash-1")
		return readErr
	})
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("session survived deletion: %v", err)
	}
}

func testUserCascade(t *testing.T, store storage.Store) {
	ctx := context.Background()

	err := store.Tx(ctx, func(tx storage.Tx) error {
		if err := tx.PutUser(ctx, &storage.User{ID: "user-1", Username: "nadia"}); err != nil {
			return err
		}
		for _, hash := range []string{"hash-1", "hash-2"} {
			if err := tx.PutUserSession(ctx, &storage.UserSession{
				TokenHash: hash, UserID: "user-1",
				ExpiresAt: time.Now().UTC().Add(time.Hour),
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := store.Tx(ctx, func(tx storage.Tx) error {
		return tx.DeleteUserSessionsOf(ctx, "user-1")
	}); err != nil {
		t.Fatalf("delete sessions: %v", err)
	}

	for _, hash := range []string{"hash-1", "hash-2"} {
		err := store.ReadOnly(ctx, func(q storage.Query) error {
			_, readErr := q.GetUserSession(ctx, hash)
			return readErr
		})
		if !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("%s survived: %v", hash, err)
		}
	}
}

func testWorldListings(t *testing.T, store storage.Store) {
	ctx := context.Background()

	err := store.Tx(ctx, func(tx storage.Tx) error {
		if err := tx.PutWorld(ctx, &storage.World{
			ID: "world-2", Slug: "second", Title: "Another Table",
			SystemID: "dnd5e", SystemVersion: "0.0.0",
		}); err != nil {
			return err
		}
		return tx.PutMember(ctx, &storage.Member{
			WorldID: worldID, UserID: "user-1", Role: "gm",
		})
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	var all, mine []storage.World
	if err := store.ReadOnly(ctx, func(q storage.Query) error {
		var readErr error
		if all, readErr = q.ListWorlds(ctx); readErr != nil {
			return readErr
		}
		mine, readErr = q.ListWorldsForUser(ctx, "user-1")
		return readErr
	}); err != nil {
		t.Fatalf("list: %v", err)
	}

	if len(all) != 2 {
		t.Errorf("all worlds = %d, want 2", len(all))
	}
	if len(mine) != 1 || mine[0].ID != worldID {
		t.Errorf("worlds for user-1 = %+v", mine)
	}

	if err := store.Tx(ctx, func(tx storage.Tx) error {
		return tx.DeleteMember(ctx, worldID, "user-1")
	}); err != nil {
		t.Fatalf("delete member: %v", err)
	}

	_ = store.ReadOnly(ctx, func(q storage.Query) error {
		var readErr error
		mine, readErr = q.ListWorldsForUser(ctx, "user-1")
		return readErr
	})
	if len(mine) != 0 {
		t.Errorf("removed member still sees %d worlds", len(mine))
	}

	err = store.Tx(ctx, func(tx storage.Tx) error {
		return tx.DeleteMember(ctx, worldID, "user-1")
	})
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("removing a missing member returned %v, want ErrNotFound", err)
	}
}

func testInviteLifecycle(t *testing.T, store storage.Store) {
	ctx := context.Background()
	expires := time.Now().UTC().Add(time.Hour)

	invite := &storage.Invite{
		ID: "invite-1", TokenHash: "hash-1", WorldID: worldID, Role: "player",
		CreatedBy: "user-1", ExpiresAt: expires, MaxUses: 2,
	}
	if err := store.Tx(ctx, func(tx storage.Tx) error {
		return tx.PutInvite(ctx, invite)
	}); err != nil {
		t.Fatalf("put invite: %v", err)
	}

	var loaded *storage.Invite
	if err := store.ReadOnly(ctx, func(q storage.Query) error {
		var readErr error
		loaded, readErr = q.GetInviteByTokenHash(ctx, "hash-1")
		return readErr
	}); err != nil {
		t.Fatalf("get invite: %v", err)
	}
	if loaded.Role != "player" || loaded.MaxUses != 2 || loaded.Uses != 0 {
		t.Errorf("invite = %+v", loaded)
	}
	if !loaded.Usable(time.Now().UTC()) {
		t.Error("a fresh invite is not usable")
	}

	for attempt := 1; attempt <= 2; attempt++ {
		if err := store.Tx(ctx, func(tx storage.Tx) error {
			return tx.ConsumeInvite(ctx, worldID, "invite-1")
		}); err != nil {
			t.Fatalf("consume %d: %v", attempt, err)
		}
	}

	err := store.Tx(ctx, func(tx storage.Tx) error {
		return tx.ConsumeInvite(ctx, worldID, "invite-1")
	})
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("a spent invite was consumed again: %v", err)
	}

	unlimited := &storage.Invite{
		ID: "invite-2", TokenHash: "hash-2", WorldID: worldID, Role: "observer",
		CreatedBy: "user-1", ExpiresAt: expires,
	}
	if err := store.Tx(ctx, func(tx storage.Tx) error {
		return tx.PutInvite(ctx, unlimited)
	}); err != nil {
		t.Fatalf("put unlimited invite: %v", err)
	}

	for attempt := 0; attempt < 3; attempt++ {
		if err := store.Tx(ctx, func(tx storage.Tx) error {
			return tx.ConsumeInvite(ctx, worldID, "invite-2")
		}); err != nil {
			t.Fatalf("unlimited consume %d: %v", attempt, err)
		}
	}

	if err := store.Tx(ctx, func(tx storage.Tx) error {
		return tx.RevokeInvite(ctx, worldID, "invite-2")
	}); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	err = store.Tx(ctx, func(tx storage.Tx) error {
		return tx.ConsumeInvite(ctx, worldID, "invite-2")
	})
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("a revoked invite was consumed: %v", err)
	}

	var invites []storage.Invite
	_ = store.ReadOnly(ctx, func(q storage.Query) error {
		var readErr error
		invites, readErr = q.ListInvites(ctx, worldID)
		return readErr
	})
	if len(invites) != 2 {
		t.Errorf("listed %d invites, want 2", len(invites))
	}
	for _, entry := range invites {
		if entry.ID == "invite-2" && entry.RevokedAt == nil {
			t.Error("revocation not recorded")
		}
	}
}

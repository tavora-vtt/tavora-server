package perm

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/tavora-vtt/tavora-server/internal/storage"
)

const player = storage.ID("user-1")

func acl(t *testing.T, raw string) ACL {
	t.Helper()
	parsed, err := ParseACL(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("parse acl %s: %v", raw, err)
	}
	return parsed
}

func TestEffectiveResolutionOrder(t *testing.T) {
	tests := []struct {
		name       string
		role       Role
		document   string
		folder     string
		world      Level
		wantLevel  Level
		wantSource Source
	}{
		{"gm ignores every acl", RoleGM, `{"user-1":"none"}`, `{"default":"none"}`, LevelNone, LevelOwner, SourceRole},
		{"assistant ignores every acl", RoleAssistant, `{}`, `{}`, LevelNone, LevelOwner, SourceRole},

		{"explicit beats document default", RolePlayer, `{"user-1":"owner","default":"limited"}`, `{}`, LevelNone, LevelOwner, SourceExplicit},
		{"explicit none beats document default", RolePlayer, `{"user-1":"none","default":"owner"}`, `{}`, LevelNone, LevelNone, SourceNone},
		{"document default beats folder", RolePlayer, `{"default":"observer"}`, `{"default":"owner"}`, LevelNone, LevelObserver, SourceDocument},
		{"folder explicit beats folder default", RolePlayer, `{}`, `{"user-1":"owner","default":"limited"}`, LevelNone, LevelOwner, SourceFolder},
		{"folder default beats world", RolePlayer, `{}`, `{"default":"limited"}`, LevelObserver, LevelLimited, SourceFolder},
		{"world default is the last resort", RolePlayer, `{}`, `{}`, LevelObserver, LevelObserver, SourceWorld},
		{"nothing grants nothing", RolePlayer, `{}`, `{}`, LevelNone, LevelNone, SourceNone},

		{"observer is capped at observer", RoleObserver, `{"user-1":"owner"}`, `{}`, LevelNone, LevelObserver, SourceExplicit},
		{"observer keeps a lower grant", RoleObserver, `{"user-1":"limited"}`, `{}`, LevelNone, LevelLimited, SourceExplicit},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			grant := Effective(
				Subject{UserID: player, Role: test.role},
				Inputs{
					Document: acl(t, test.document),
					Folder:   acl(t, test.folder),
					World:    test.world,
				},
			)

			if grant.Level != test.wantLevel {
				t.Errorf("level = %s, want %s", grant.Level, test.wantLevel)
			}
			if grant.Source != test.wantSource {
				t.Errorf("source = %s, want %s", grant.Source, test.wantSource)
			}
		})
	}
}

func TestGrantCapabilities(t *testing.T) {
	tests := []struct {
		role     Role
		level    Level
		read     bool
		edit     bool
		gmFields bool
	}{
		{RoleGM, LevelOwner, true, true, true},
		{RoleAssistant, LevelOwner, true, true, true},
		{RolePlayer, LevelOwner, true, true, false},
		{RolePlayer, LevelObserver, true, false, false},
		{RolePlayer, LevelLimited, true, false, false},
		{RolePlayer, LevelNone, false, false, false},
		{RoleObserver, LevelOwner, true, false, false},
	}

	for _, test := range tests {
		grant := Grant{Role: test.role, Level: test.level}
		if grant.CanRead() != test.read {
			t.Errorf("%s/%s CanRead = %v", test.role, test.level, grant.CanRead())
		}
		if grant.CanEdit() != test.edit {
			t.Errorf("%s/%s CanEdit = %v", test.role, test.level, grant.CanEdit())
		}
		if grant.Allows(VisibilityGM) != test.gmFields {
			t.Errorf("%s/%s sees gm fields = %v", test.role, test.level, grant.Allows(VisibilityGM))
		}
	}
}

func TestVisibilityMatrix(t *testing.T) {
	visibilities := []Visibility{VisibilityPublic, VisibilityObserver, VisibilityOwner, VisibilityGM}

	expected := map[Level][]bool{
		LevelLimited:  {true, false, false, false},
		LevelObserver: {true, true, false, false},
		LevelOwner:    {true, true, true, false},
	}

	for level, wants := range expected {
		grant := Grant{Role: RolePlayer, Level: level}
		for index, visibility := range visibilities {
			if got := grant.Allows(visibility); got != wants[index] {
				t.Errorf("player at %s sees %s = %v, want %v", level, visibility, got, wants[index])
			}
		}
	}

	gm := Grant{Role: RoleGM, Level: LevelOwner}
	for _, visibility := range visibilities {
		if !gm.Allows(visibility) {
			t.Errorf("gm cannot see %s", visibility)
		}
	}
}

func vampirePolicy() FieldPolicy {
	return NewStaticPolicy().
		Set("actor", "vampire", "hunger", VisibilityObserver).
		Set("actor", "vampire", "notes", VisibilityOwner).
		Set("actor", "vampire", "secrets", VisibilityGM).
		Set("actor", "vampire", "attributes.willpower", VisibilityOwner)
}

func vampireDocument() *storage.Document {
	return &storage.Document{
		WorldID: "world-1",
		ID:      "actor-1",
		Kind:    "actor",
		Subtype: "vampire",
		Name:    "Nadia Kovac",
		Img:     "nadia.webp",
		Data: json.RawMessage(`{
			"clan": "Ventrue",
			"hunger": 2,
			"notes": "owes a boon to the Sheriff",
			"secrets": {"sire": "unknown", "diablerie": true},
			"attributes": {"strength": 3, "willpower": 5}
		}`),
		Flags:     json.RawMessage(`{"tavora-dice-tray":{"pinned":true}}`),
		Ownership: json.RawMessage(`{"user-1":"owner","user-2":"observer"}`),
	}
}

func redactAs(t *testing.T, role Role, level Level) map[string]any {
	t.Helper()

	visible, send, err := RedactDocument(
		vampireDocument(),
		Subject{UserID: player, Role: role},
		Grant{Role: role, Level: level},
		vampirePolicy(),
	)
	if err != nil {
		t.Fatalf("redact: %v", err)
	}
	if !send {
		t.Fatal("document was withheld entirely")
	}

	var data map[string]any
	if err := json.Unmarshal(visible.Data, &data); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	return data
}

func TestRedactionByLevel(t *testing.T) {
	tests := []struct {
		name    string
		role    Role
		level   Level
		present []string
		absent  []string
	}{
		{
			name: "owner sees everything but gm fields",
			role: RolePlayer, level: LevelOwner,
			present: []string{"clan", "hunger", "notes", "attributes.willpower"},
			absent:  []string{"secrets"},
		},
		{
			name: "observer loses owner and gm fields",
			role: RolePlayer, level: LevelObserver,
			present: []string{"clan", "hunger", "attributes.strength"},
			absent:  []string{"notes", "secrets", "attributes.willpower"},
		},
		{
			name: "gm sees everything",
			role: RoleGM, level: LevelOwner,
			present: []string{"clan", "hunger", "notes", "secrets", "attributes.willpower"},
			absent:  nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := redactAs(t, test.role, test.level)
			paths := pathSet(data)

			for _, path := range test.present {
				if !paths[path] {
					t.Errorf("%q is missing, it should be visible", path)
				}
			}
			for _, path := range test.absent {
				if paths[path] {
					t.Errorf("%q leaked to a %s at %s", path, test.role, test.level)
				}
			}
		})
	}
}

func TestLimitedSeesNameAndImageOnly(t *testing.T) {
	visible, send, err := RedactDocument(
		vampireDocument(),
		Subject{UserID: player, Role: RolePlayer},
		Grant{Role: RolePlayer, Level: LevelLimited},
		vampirePolicy(),
	)
	if err != nil {
		t.Fatalf("redact: %v", err)
	}
	if !send {
		t.Fatal("limited access should still send name and image")
	}

	if visible.Name != "Nadia Kovac" || visible.Img != "nadia.webp" {
		t.Errorf("name or image missing: %+v", visible)
	}
	if string(visible.Data) != "{}" {
		t.Errorf("data = %s, want an empty object", visible.Data)
	}
	if string(visible.Flags) != "{}" {
		t.Errorf("flags = %s, want an empty object", visible.Flags)
	}
}

func TestNoAccessWithholdsTheDocument(t *testing.T) {
	_, send, err := RedactDocument(
		vampireDocument(),
		Subject{UserID: player, Role: RolePlayer},
		Grant{Role: RolePlayer, Level: LevelNone},
		vampirePolicy(),
	)
	if err != nil {
		t.Fatalf("redact: %v", err)
	}
	if send {
		t.Error("a document with no access must not be sent at all")
	}
}

func TestRedactionRemovesRatherThanNulls(t *testing.T) {
	visible, _, err := RedactDocument(
		vampireDocument(),
		Subject{UserID: player, Role: RolePlayer},
		Grant{Role: RolePlayer, Level: LevelObserver},
		vampirePolicy(),
	)
	if err != nil {
		t.Fatalf("redact: %v", err)
	}

	serialised := string(visible.Data)
	for _, forbidden := range []string{"secrets", "notes", "willpower", "diablerie", "null"} {
		if strings.Contains(serialised, forbidden) {
			t.Errorf("payload still mentions %q: %s", forbidden, serialised)
		}
	}
}

func TestOwnershipOfOthersIsNotDisclosed(t *testing.T) {
	visible, _, err := RedactDocument(
		vampireDocument(),
		Subject{UserID: player, Role: RolePlayer},
		Grant{Role: RolePlayer, Level: LevelOwner},
		vampirePolicy(),
	)
	if err != nil {
		t.Fatalf("redact: %v", err)
	}

	var ownership map[string]string
	if err := json.Unmarshal(visible.Ownership, &ownership); err != nil {
		t.Fatalf("decode ownership: %v", err)
	}
	if _, present := ownership["user-2"]; present {
		t.Errorf("a player learned about another user's access: %v", ownership)
	}
	if ownership["user-1"] != "owner" {
		t.Errorf("own access missing: %v", ownership)
	}
}

func TestGMKeepsTheWholeOwnershipMap(t *testing.T) {
	visible, _, err := RedactDocument(
		vampireDocument(),
		Subject{UserID: "gm-1", Role: RoleGM},
		Grant{Role: RoleGM, Level: LevelOwner},
		vampirePolicy(),
	)
	if err != nil {
		t.Fatalf("redact: %v", err)
	}

	var ownership map[string]string
	if err := json.Unmarshal(visible.Ownership, &ownership); err != nil {
		t.Fatalf("decode ownership: %v", err)
	}
	if len(ownership) != 2 {
		t.Errorf("gm ownership = %v, want both entries", ownership)
	}
}

func TestHiddenPathsReportsWhatWasRemoved(t *testing.T) {
	hidden := HiddenPaths(
		vampireDocument().Data, "actor", "vampire",
		Grant{Role: RolePlayer, Level: LevelObserver},
		vampirePolicy(),
	)
	sort.Strings(hidden)

	want := []string{"attributes.willpower", "notes", "secrets"}
	if !reflect.DeepEqual(hidden, want) {
		t.Errorf("hidden = %v, want %v", hidden, want)
	}
}

func TestParseACLRejectsUnknownLevel(t *testing.T) {
	if _, err := ParseACL(json.RawMessage(`{"user-1":"superuser"}`)); err == nil {
		t.Error("an unknown ownership level should be rejected")
	}
}

func pathSet(data map[string]any) map[string]bool {
	result := make(map[string]bool)

	var walk func(map[string]any, string)
	walk = func(source map[string]any, prefix string) {
		for key, value := range source {
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			result[path] = true
			if nested, ok := value.(map[string]any); ok {
				walk(nested, path)
			}
		}
	}
	walk(data, "")
	return result
}

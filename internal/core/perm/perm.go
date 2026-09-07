package perm

import (
	"encoding/json"
	"fmt"

	"github.com/tavora-vtt/tavora-server/internal/storage"
)

type Role string

const (
	RoleGM        Role = "gm"
	RoleAssistant Role = "assistant"
	RolePlayer    Role = "player"
	RoleObserver  Role = "observer"
)

func (r Role) Valid() bool {
	switch r {
	case RoleGM, RoleAssistant, RolePlayer, RoleObserver:
		return true
	default:
		return false
	}
}

func (r Role) IsStaff() bool {
	return r == RoleGM || r == RoleAssistant
}

type Level int

const (
	LevelNone Level = iota
	LevelLimited
	LevelObserver
	LevelOwner
)

func (l Level) String() string {
	switch l {
	case LevelLimited:
		return "limited"
	case LevelObserver:
		return "observer"
	case LevelOwner:
		return "owner"
	default:
		return "none"
	}
}

func ParseLevel(raw string) (Level, error) {
	switch raw {
	case "none", "":
		return LevelNone, nil
	case "limited":
		return LevelLimited, nil
	case "observer":
		return LevelObserver, nil
	case "owner":
		return LevelOwner, nil
	default:
		return LevelNone, fmt.Errorf("perm: unknown ownership level %q", raw)
	}
}

type Visibility string

const (
	VisibilityPublic   Visibility = "public"
	VisibilityObserver Visibility = "observer"
	VisibilityOwner    Visibility = "owner"
	VisibilityGM       Visibility = "gm"
)

func (v Visibility) required() Level {
	switch v {
	case VisibilityObserver:
		return LevelObserver
	case VisibilityOwner, VisibilityGM:
		return LevelOwner
	default:
		return LevelLimited
	}
}

type Source string

const (
	SourceRole     Source = "role"
	SourceExplicit Source = "explicit"
	SourceDocument Source = "document"
	SourceFolder   Source = "folder"
	SourceWorld    Source = "world"
	SourceNone     Source = "none"
)

type Grant struct {
	Role   Role
	Level  Level
	Source Source
}

func (g Grant) CanRead() bool {
	return g.Level > LevelNone
}

func (g Grant) CanEdit() bool {
	return g.Role != RoleObserver && g.Level >= LevelOwner
}

func (g Grant) CanSeeGMOnly() bool {
	return g.Role.IsStaff()
}

func (g Grant) Allows(visibility Visibility) bool {
	if visibility == VisibilityGM {
		return g.Role.IsStaff()
	}
	if g.Role.IsStaff() {
		return true
	}
	return g.Level >= visibility.required()
}

const DefaultKey = "default"

type ACL map[string]Level

func ParseACL(raw json.RawMessage) (ACL, error) {
	if len(raw) == 0 {
		return ACL{}, nil
	}

	var entries map[string]string
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("perm: parse ownership: %w", err)
	}

	acl := make(ACL, len(entries))
	for key, value := range entries {
		level, err := ParseLevel(value)
		if err != nil {
			return nil, err
		}
		acl[key] = level
	}
	return acl, nil
}

type Subject struct {
	UserID storage.ID
	Role   Role
}

type Inputs struct {
	Document ACL
	Folder   ACL
	World    Level
}

func Effective(subject Subject, inputs Inputs) Grant {
	if subject.Role.IsStaff() {
		return Grant{Role: subject.Role, Level: LevelOwner, Source: SourceRole}
	}

	user := string(subject.UserID)

	grant := Grant{Role: subject.Role, Level: LevelNone, Source: SourceNone}

	switch {
	case has(inputs.Document, user):
		grant.Level, grant.Source = inputs.Document[user], SourceExplicit
	case has(inputs.Document, DefaultKey):
		grant.Level, grant.Source = inputs.Document[DefaultKey], SourceDocument
	case has(inputs.Folder, user):
		grant.Level, grant.Source = inputs.Folder[user], SourceFolder
	case has(inputs.Folder, DefaultKey):
		grant.Level, grant.Source = inputs.Folder[DefaultKey], SourceFolder
	case inputs.World > LevelNone:
		grant.Level, grant.Source = inputs.World, SourceWorld
	}

	if subject.Role == RoleObserver && grant.Level > LevelObserver {
		grant.Level = LevelObserver
	}
	if grant.Level == LevelNone {
		grant.Source = SourceNone
	}

	return grant
}

func has(acl ACL, key string) bool {
	_, present := acl[key]
	return present
}

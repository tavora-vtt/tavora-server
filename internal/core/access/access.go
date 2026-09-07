package access

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/tavora-vtt/tavora-server/internal/core/perm"
	"github.com/tavora-vtt/tavora-server/internal/storage"
)

type worldSettings struct {
	DefaultOwnership map[string]string `json:"defaultOwnership"`
}

type Resolver struct {
	store  storage.Store
	policy perm.FieldPolicy
}

func NewResolver(store storage.Store, policy perm.FieldPolicy) *Resolver {
	if policy == nil {
		policy = perm.OpenPolicy{}
	}
	return &Resolver{store: store, policy: policy}
}

func (r *Resolver) Policy() perm.FieldPolicy { return r.policy }

func (r *Resolver) RoleOf(ctx context.Context, q storage.Query, worldID, userID storage.ID) (perm.Role, error) {
	member, err := q.GetMember(ctx, worldID, userID)
	if errors.Is(err, storage.ErrNotFound) {
		return "", fmt.Errorf("%w: %s is not a member of %s", storage.ErrNotFound, userID, worldID)
	}
	if err != nil {
		return "", err
	}

	role := perm.Role(member.Role)
	if !role.Valid() {
		return "", fmt.Errorf("access: world %s stores an unknown role %q", worldID, member.Role)
	}
	return role, nil
}

func (r *Resolver) Grant(ctx context.Context, q storage.Query, subject perm.Subject, doc *storage.Document) (perm.Grant, error) {
	documentACL, err := perm.ParseACL(doc.Ownership)
	if err != nil {
		return perm.Grant{}, err
	}

	folderACL := perm.ACL{}
	if doc.FolderID != "" {
		folder, err := q.GetDocument(ctx, doc.WorldID, doc.FolderID)
		switch {
		case errors.Is(err, storage.ErrNotFound):
		case err != nil:
			return perm.Grant{}, err
		default:
			if folderACL, err = perm.ParseACL(folder.Ownership); err != nil {
				return perm.Grant{}, err
			}
		}
	}

	worldDefault, err := r.worldDefault(ctx, q, doc)
	if err != nil {
		return perm.Grant{}, err
	}

	return perm.Effective(subject, perm.Inputs{
		Document: documentACL,
		Folder:   folderACL,
		World:    worldDefault,
	}), nil
}

func (r *Resolver) worldDefault(ctx context.Context, q storage.Query, doc *storage.Document) (perm.Level, error) {
	world, err := q.GetWorld(ctx, doc.WorldID)
	if err != nil {
		return perm.LevelNone, err
	}
	if len(world.Settings) == 0 {
		return perm.LevelNone, nil
	}

	var settings worldSettings
	if err := json.Unmarshal(world.Settings, &settings); err != nil {
		return perm.LevelNone, nil
	}

	for _, key := range []string{doc.Kind + "/" + doc.Subtype, doc.Kind} {
		if raw, present := settings.DefaultOwnership[key]; present {
			level, err := perm.ParseLevel(raw)
			if err != nil {
				return perm.LevelNone, err
			}
			return level, nil
		}
	}
	return perm.LevelNone, nil
}

type View struct {
	Subject  perm.Subject
	Grant    perm.Grant
	Document *storage.Document
}

func (r *Resolver) ProjectForMembers(ctx context.Context, q storage.Query, doc *storage.Document) (map[storage.ID]View, error) {
	members, err := q.ListMembers(ctx, doc.WorldID)
	if err != nil {
		return nil, err
	}

	views := make(map[storage.ID]View, len(members))

	for _, member := range members {
		role := perm.Role(member.Role)
		if !role.Valid() {
			continue
		}

		subject := perm.Subject{UserID: member.UserID, Role: role}

		grant, err := r.Grant(ctx, q, subject, doc)
		if err != nil {
			return nil, err
		}

		visible, send, err := perm.RedactDocument(doc, subject, grant, r.policy)
		if err != nil {
			return nil, err
		}
		if !send {
			continue
		}

		views[member.UserID] = View{Subject: subject, Grant: grant, Document: visible}
	}

	return views, nil
}

func (r *Resolver) Authorize(ctx context.Context, q storage.Query, worldID, userID storage.ID, doc *storage.Document, write bool) (perm.Grant, error) {
	role, err := r.RoleOf(ctx, q, worldID, userID)
	if err != nil {
		return perm.Grant{}, err
	}

	subject := perm.Subject{UserID: userID, Role: role}

	grant, err := r.Grant(ctx, q, subject, doc)
	if err != nil {
		return perm.Grant{}, err
	}

	if write && !grant.CanEdit() {
		return grant, ErrForbidden
	}
	if !write && !grant.CanRead() {
		return grant, ErrForbidden
	}
	return grant, nil
}

var ErrForbidden = errors.New("access: forbidden")

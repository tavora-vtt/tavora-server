package perm

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tavora-vtt/tavora-server/internal/storage"
)

type FieldPolicy interface {
	VisibilityOf(kind, subtype, path string) Visibility
}

type StaticPolicy struct {
	rules map[string]map[string]Visibility
}

func NewStaticPolicy() *StaticPolicy {
	return &StaticPolicy{rules: make(map[string]map[string]Visibility)}
}

func (p *StaticPolicy) Set(kind, subtype, path string, visibility Visibility) *StaticPolicy {
	key := kind + "/" + subtype
	if p.rules[key] == nil {
		p.rules[key] = make(map[string]Visibility)
	}
	p.rules[key][path] = visibility
	return p
}

func (p *StaticPolicy) VisibilityOf(kind, subtype, path string) Visibility {
	if rules, present := p.rules[kind+"/"+subtype]; present {
		if visibility, present := rules[path]; present {
			return visibility
		}
	}
	if rules, present := p.rules[kind+"/"]; present {
		if visibility, present := rules[path]; present {
			return visibility
		}
	}
	return VisibilityPublic
}

type OpenPolicy struct{}

func (OpenPolicy) VisibilityOf(string, string, string) Visibility { return VisibilityPublic }

func RedactDocument(doc *storage.Document, subject Subject, grant Grant, policy FieldPolicy) (*storage.Document, bool, error) {
	if !grant.CanRead() {
		return nil, false, nil
	}

	visible := &storage.Document{
		WorldID:       doc.WorldID,
		ID:            doc.ID,
		Kind:          doc.Kind,
		Subtype:       doc.Subtype,
		ParentID:      doc.ParentID,
		FolderID:      doc.FolderID,
		Name:          doc.Name,
		Sort:          doc.Sort,
		Img:           doc.Img,
		SchemaVersion: doc.SchemaVersion,
		SourcePack:    doc.SourcePack,
		CreatedAt:     doc.CreatedAt,
		UpdatedAt:     doc.UpdatedAt,
		UpdatedSeq:    doc.UpdatedSeq,
		DeletedAt:     doc.DeletedAt,
		Data:          json.RawMessage("{}"),
		Flags:         json.RawMessage("{}"),
		Ownership:     ownershipFor(subject, grant, doc.Ownership),
	}

	if grant.Level == LevelLimited {
		return visible, true, nil
	}

	data, err := RedactPayload(doc.Data, doc.Kind, doc.Subtype, "", grant, policy)
	if err != nil {
		return nil, false, err
	}
	visible.Data = data

	if grant.Level >= LevelObserver {
		flags, err := RedactPayload(doc.Flags, doc.Kind, doc.Subtype, "flags", grant, policy)
		if err != nil {
			return nil, false, err
		}
		visible.Flags = flags
	}

	return visible, true, nil
}

func RedactPayload(raw json.RawMessage, kind, subtype, prefix string, grant Grant, policy FieldPolicy) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.RawMessage("{}"), nil
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("perm: decode payload: %w", err)
	}

	pruned := pruneObject(decoded, kind, subtype, prefix, grant, policy)

	encoded, err := json.Marshal(pruned)
	if err != nil {
		return nil, fmt.Errorf("perm: encode payload: %w", err)
	}
	return encoded, nil
}

func pruneObject(source map[string]any, kind, subtype, prefix string, grant Grant, policy FieldPolicy) map[string]any {
	result := make(map[string]any, len(source))

	for key, value := range source {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}

		if !grant.Allows(policy.VisibilityOf(kind, subtype, path)) {
			continue
		}

		if nested, ok := value.(map[string]any); ok {
			result[key] = pruneObject(nested, kind, subtype, path, grant, policy)
			continue
		}
		result[key] = value
	}

	return result
}

func ownershipFor(subject Subject, grant Grant, raw json.RawMessage) json.RawMessage {
	if grant.Role.IsStaff() {
		if len(raw) == 0 {
			return json.RawMessage("{}")
		}
		return raw
	}

	acl, err := ParseACL(raw)
	if err != nil {
		return json.RawMessage("{}")
	}

	own := make(map[string]string, 1)
	if level, present := acl[string(subject.UserID)]; present {
		own[string(subject.UserID)] = level.String()
	}

	encoded, err := json.Marshal(own)
	if err != nil {
		return json.RawMessage("{}")
	}
	return encoded
}

func HiddenPaths(raw json.RawMessage, kind, subtype string, grant Grant, policy FieldPolicy) []string {
	if len(raw) == 0 {
		return nil
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil
	}

	var hidden []string
	collectHidden(decoded, kind, subtype, "", grant, policy, &hidden)
	return hidden
}

func collectHidden(source map[string]any, kind, subtype, prefix string, grant Grant, policy FieldPolicy, out *[]string) {
	for key, value := range source {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}

		if !grant.Allows(policy.VisibilityOf(kind, subtype, path)) {
			*out = append(*out, path)
			continue
		}
		if nested, ok := value.(map[string]any); ok {
			collectHidden(nested, kind, subtype, path, grant, policy, out)
		}
	}
}

func PathsOf(raw json.RawMessage) []string {
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil
	}

	var paths []string
	var walk func(map[string]any, string)
	walk = func(source map[string]any, prefix string) {
		for key, value := range source {
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			paths = append(paths, path)
			if nested, ok := value.(map[string]any); ok {
				walk(nested, path)
			}
		}
	}
	walk(decoded, "")
	return paths
}

func JoinPath(segments ...string) string {
	return strings.Join(segments, ".")
}

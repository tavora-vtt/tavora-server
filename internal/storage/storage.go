package storage

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrNotFound      = errors.New("storage: not found")
	ErrAlreadyExists = errors.New("storage: already exists")
	ErrReadOnly      = errors.New("storage: read only transaction")
	ErrInvalidPath   = errors.New("storage: invalid json path")
)

type ID string

type World struct {
	ID            ID
	Slug          string
	Title         string
	SystemID      string
	SystemVersion string
	DefaultLocale string
	Settings      json.RawMessage
	EventSeq      int64
	CreatedAt     time.Time
	ArchivedAt    *time.Time
}

type User struct {
	ID           ID
	Username     string
	Email        string
	PasswordHash string
	Locale       string
	IsAdmin      bool
	CreatedAt    time.Time
	DisabledAt   *time.Time
}

func (u *User) Disabled() bool { return u.DisabledAt != nil }

type UserSession struct {
	TokenHash  string
	UserID     ID
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastSeenAt time.Time
	UserAgent  string
}

type Member struct {
	WorldID  ID
	UserID   ID
	Role     string
	JoinedAt time.Time
}

type Document struct {
	WorldID       ID
	ID            ID
	Kind          string
	Subtype       string
	ParentID      ID
	FolderID      ID
	Name          string
	Sort          int
	Img           string
	Data          json.RawMessage
	Flags         json.RawMessage
	Ownership     json.RawMessage
	SourcePack    string
	SchemaVersion string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	UpdatedSeq    int64
	DeletedAt     *time.Time
}

type Event struct {
	WorldID     ID
	Seq         int64
	Timestamp   time.Time
	ActorUserID ID
	Kind        string
	TargetKind  string
	TargetID    ID
	SceneID     ID
	Payload     json.RawMessage
	Audience    json.RawMessage
	Undo        json.RawMessage
}

type DeleteMode int

const (
	DeleteSoft DeleteMode = iota
	DeleteHard
)

type DocumentFilter struct {
	Kind           string
	Subtype        string
	ParentID       *ID
	FolderID       *ID
	IncludeDeleted bool
	UpdatedSince   int64
	Limit          int
}

type Patch struct {
	Name      *string
	Sort      *int
	Img       *string
	FolderID  *ID
	Set       map[string]any
	Unset     []string
	Ownership json.RawMessage
	Seq       int64
}

func (p Patch) IsEmpty() bool {
	return p.Name == nil && p.Sort == nil && p.Img == nil && p.FolderID == nil &&
		len(p.Set) == 0 && len(p.Unset) == 0 && p.Ownership == nil
}

type JSONOp string

const (
	OpEqual        JSONOp = "eq"
	OpNotEqual     JSONOp = "ne"
	OpLess         JSONOp = "lt"
	OpLessEqual    JSONOp = "lte"
	OpGreater      JSONOp = "gt"
	OpGreaterEqual JSONOp = "gte"
	OpContains     JSONOp = "contains"
	OpExists       JSONOp = "exists"
)

type JSONCondition struct {
	Path  string
	Op    JSONOp
	Value any
}

type JSONQuery struct {
	Kind       string
	Subtype    string
	Conditions []JSONCondition
	Limit      int
}

type Query interface {
	GetWorld(ctx context.Context, id ID) (*World, error)
	GetUser(ctx context.Context, id ID) (*User, error)
	GetUserByUsername(ctx context.Context, username string) (*User, error)
	CountUsers(ctx context.Context) (int, error)
	GetUserSession(ctx context.Context, tokenHash string) (*UserSession, error)
	GetMember(ctx context.Context, worldID, userID ID) (*Member, error)
	ListMembers(ctx context.Context, worldID ID) ([]Member, error)
	GetDocument(ctx context.Context, worldID, id ID) (*Document, error)
	ListDocuments(ctx context.Context, worldID ID, filter DocumentFilter) ([]*Document, error)
	QuerySystemData(ctx context.Context, worldID ID, query JSONQuery) ([]*Document, error)
	EventsSince(ctx context.Context, worldID ID, seq int64, limit int) ([]Event, error)
}

type Tx interface {
	Query

	PutWorld(ctx context.Context, world *World) error
	PutUser(ctx context.Context, user *User) error
	PutUserSession(ctx context.Context, session *UserSession) error
	TouchUserSession(ctx context.Context, tokenHash string, seenAt time.Time) error
	DeleteUserSession(ctx context.Context, tokenHash string) error
	DeleteUserSessionsOf(ctx context.Context, userID ID) error
	PutMember(ctx context.Context, member *Member) error
	PutDocument(ctx context.Context, doc *Document) error
	PatchDocument(ctx context.Context, worldID, id ID, patch Patch) (*Document, error)
	DeleteDocument(ctx context.Context, worldID, id ID, mode DeleteMode) error
	AppendEvent(ctx context.Context, event Event) (int64, error)
}

type Store interface {
	Tx(ctx context.Context, fn func(Tx) error) error
	ReadOnly(ctx context.Context, fn func(Query) error) error
	Migrate(ctx context.Context) error
	Ping(ctx context.Context) error
	Close() error
	Backend() string
}

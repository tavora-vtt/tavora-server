package ws

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/tavora-vtt/tavora-server/internal/core/dice"
	"github.com/tavora-vtt/tavora-server/internal/storage"
)

const (
	KindCombat        = "combat"
	CombatDocumentID  = storage.ID("combat")
	DefaultInitiative = "1d20"
	maxCombatants     = 60
)

type Combatant struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Initiative  int    `json:"initiative"`
	Disposition string `json:"disposition"`
}

type Combat struct {
	Active     bool        `json:"active"`
	Round      int         `json:"round"`
	Turn       int         `json:"turn"`
	Formula    string      `json:"formula"`
	SceneID    string      `json:"sceneId"`
	Combatants []Combatant `json:"combatants"`
}

type CombatStartPayload struct {
	SceneID string `json:"sceneId"`
	Formula string `json:"formula,omitempty"`
}

func RegisterCombatIntents(router *Router) {
	router.Handle("combat.start", handleCombatStart)
	router.Handle("combat.next", handleCombatNext)
	router.Handle("combat.end", handleCombatEnd)
}

func requireStaff(session *Session) error {
	if !session.Subject().Role.IsStaff() {
		return NewIntentError(CodeForbidden, "core.intent.gameMasterOnly")
	}
	return nil
}

func handleCombatStart(ctx context.Context, session *Session, intent Intent) (IntentResult, error) {
	if err := requireStaff(session); err != nil {
		return IntentResult{}, err
	}

	var payload CombatStartPayload
	if err := json.Unmarshal(intent.Payload, &payload); err != nil {
		return IntentResult{}, NewIntentError(CodeBadRequest, "core.intent.malformedPayload")
	}
	if payload.SceneID == "" {
		return IntentResult{}, NewIntentError(CodeBadRequest, "core.intent.missingSceneId")
	}

	formula := strings.TrimSpace(payload.Formula)
	if formula == "" {
		formula = DefaultInitiative
	}
	if _, err := dice.Roll(formula, dice.CryptoSource()); err != nil {
		return IntentResult{}, NewIntentError(CodeBadRequest, "core.dice.malformedExpression")
	}

	combat := Combat{
		Active:  true,
		Round:   1,
		Turn:    0,
		Formula: formula,
		SceneID: payload.SceneID,
	}
	var seq int64

	err := session.Store().Tx(ctx, func(tx storage.Tx) error {
		parent := storage.ID(payload.SceneID)
		tokens, err := tx.ListDocuments(ctx, session.WorldID(), storage.DocumentFilter{
			Kind:     "token",
			ParentID: &parent,
			Limit:    maxCombatants,
		})
		if err != nil {
			return err
		}
		if len(tokens) == 0 {
			return errNoCombatants
		}

		for _, token := range tokens {
			var data struct {
				Disposition string `json:"disposition"`
			}
			_ = json.Unmarshal(token.Data, &data)

			result, err := dice.Roll(formula, dice.CryptoSource())
			if err != nil {
				return err
			}

			combat.Combatants = append(combat.Combatants, Combatant{
				ID:          string(token.ID),
				Name:        token.Name,
				Initiative:  result.Total,
				Disposition: data.Disposition,
			})
		}

		sortCombatants(combat.Combatants)

		seq, err = commitCombat(ctx, tx, session, &combat)
		return err
	})

	return finishCombat(session, combat, seq, err)
}

func handleCombatNext(ctx context.Context, session *Session, _ Intent) (IntentResult, error) {
	if err := requireStaff(session); err != nil {
		return IntentResult{}, err
	}

	var (
		combat Combat
		seq    int64
	)

	err := session.Store().Tx(ctx, func(tx storage.Tx) error {
		loaded, err := readCombat(ctx, tx, session)
		if err != nil {
			return err
		}
		if !loaded.Active || len(loaded.Combatants) == 0 {
			return errNoCombat
		}

		loaded.Turn++
		if loaded.Turn >= len(loaded.Combatants) {
			loaded.Turn = 0
			loaded.Round++
		}

		combat = *loaded
		seq, err = commitCombat(ctx, tx, session, &combat)
		return err
	})

	return finishCombat(session, combat, seq, err)
}

func handleCombatEnd(ctx context.Context, session *Session, _ Intent) (IntentResult, error) {
	if err := requireStaff(session); err != nil {
		return IntentResult{}, err
	}

	var (
		combat Combat
		seq    int64
	)

	err := session.Store().Tx(ctx, func(tx storage.Tx) error {
		loaded, err := readCombat(ctx, tx, session)
		if err != nil {
			return err
		}

		loaded.Active = false
		loaded.Combatants = nil
		combat = *loaded

		seq, err = commitCombat(ctx, tx, session, &combat)
		return err
	})

	return finishCombat(session, combat, seq, err)
}

var (
	errNoCombat      = errors.New("ws: no combat is running")
	errNoCombatants  = errors.New("ws: the scene has no tokens to fight")
	combatOwnership  = json.RawMessage(`{"default":"observer"}`)
	combatEventKinds = map[bool]string{true: "combat.update", false: "combat.end"}
)

func readCombat(ctx context.Context, tx storage.Tx, session *Session) (*Combat, error) {
	document, err := tx.GetDocument(ctx, session.WorldID(), CombatDocumentID)
	if err != nil {
		return nil, err
	}

	var combat Combat
	if err := json.Unmarshal(document.Data, &combat); err != nil {
		return nil, err
	}
	return &combat, nil
}

func commitCombat(ctx context.Context, tx storage.Tx, session *Session, combat *Combat) (int64, error) {
	encoded, err := json.Marshal(combat)
	if err != nil {
		return 0, err
	}

	seq, err := tx.AppendEvent(ctx, storage.Event{
		WorldID:     session.WorldID(),
		ActorUserID: session.UserID(),
		Kind:        combatEventKinds[combat.Active],
		TargetKind:  "document",
		TargetID:    CombatDocumentID,
		SceneID:     storage.ID(combat.SceneID),
		Payload:     encoded,
	})
	if err != nil {
		return 0, err
	}

	return seq, tx.PutDocument(ctx, &storage.Document{
		WorldID:    session.WorldID(),
		ID:         CombatDocumentID,
		Kind:       KindCombat,
		Name:       "Combat",
		Data:       encoded,
		Ownership:  combatOwnership,
		UpdatedSeq: seq,
	})
}

func finishCombat(session *Session, combat Combat, seq int64, err error) (IntentResult, error) {
	switch {
	case errors.Is(err, errNoCombatants):
		return IntentResult{}, NewIntentError(CodeBadRequest, "core.combat.noCombatants")
	case errors.Is(err, errNoCombat), errors.Is(err, storage.ErrNotFound):
		return IntentResult{}, NewIntentError(CodeNotFound, "core.combat.notRunning")
	case err != nil:
		return IntentResult{}, err
	}

	encoded, marshalErr := json.Marshal(combat)
	if marshalErr != nil {
		return IntentResult{}, marshalErr
	}

	session.Hub().Publish(Publication{
		Channel: WorldChannel(session.WorldID()),
		Lane:    LaneDocument,
		Frame: Frame{
			Lane: LaneDocument,
			Type: TypeEvent,
			Event: &Event{
				Seq:     seq,
				Kind:    combatEventKinds[combat.Active],
				WorldID: string(session.WorldID()),
				SceneID: combat.SceneID,
				Payload: encoded,
			},
		},
	})

	return IntentResult{Seq: seq, Result: encoded}, nil
}

func sortCombatants(combatants []Combatant) {
	sort.SliceStable(combatants, func(left, right int) bool {
		if combatants[left].Initiative != combatants[right].Initiative {
			return combatants[left].Initiative > combatants[right].Initiative
		}
		return combatants[left].Name < combatants[right].Name
	})
}

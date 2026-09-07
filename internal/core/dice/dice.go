package dice

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

var (
	ErrEmptyExpression = errors.New("dice: empty expression")
	ErrTooManyDice     = errors.New("dice: too many dice")
	ErrUnsupportedDie  = errors.New("dice: unsupported die")
	ErrMalformed       = errors.New("dice: malformed expression")
)

const (
	MaxDice     = 200
	MaxFaces    = 1000
	MaxTermsLen = 200
)

type Die struct {
	Faces int  `json:"faces"`
	Value int  `json:"value"`
	Kept  bool `json:"kept"`
}

type Term struct {
	Sign       int    `json:"sign"`
	Expression string `json:"expression"`
	Dice       []Die  `json:"dice,omitempty"`
	Constant   *int   `json:"constant,omitempty"`
	Subtotal   int    `json:"subtotal"`
}

type Result struct {
	Expression string `json:"expression"`
	Terms      []Term `json:"terms"`
	Total      int    `json:"total"`
}

type Source interface {
	Roll(faces int) int
}

type cryptoSource struct{}

func (cryptoSource) Roll(faces int) int {
	limit := big.NewInt(int64(faces))
	value, err := rand.Int(rand.Reader, limit)
	if err != nil {
		var fallback [8]byte
		if _, readErr := rand.Read(fallback[:]); readErr != nil {
			panic(readErr)
		}
		return int(binary.BigEndian.Uint64(fallback[:])%uint64(faces)) + 1
	}
	return int(value.Int64()) + 1
}

func CryptoSource() Source { return cryptoSource{} }

type FixedSource struct {
	Values []int
	index  int
}

func (s *FixedSource) Roll(faces int) int {
	if s.index >= len(s.Values) {
		return 1
	}
	value := s.Values[s.index]
	s.index++
	if value < 1 {
		return 1
	}
	if value > faces {
		return faces
	}
	return value
}

var termPattern = regexp.MustCompile(`^(\d*)d(\d+)(?:(kh|kl|dh|dl)(\d+))?$`)

func Roll(expression string, source Source) (Result, error) {
	if source == nil {
		source = CryptoSource()
	}

	normalised := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(expression)), " ", "")
	if normalised == "" {
		return Result{}, ErrEmptyExpression
	}
	if len(normalised) > MaxTermsLen {
		return Result{}, ErrMalformed
	}

	result := Result{Expression: strings.TrimSpace(expression)}
	sign := 1
	buffer := strings.Builder{}
	total := 0
	budget := MaxDice

	flush := func() error {
		piece := buffer.String()
		buffer.Reset()
		if piece == "" {
			return ErrMalformed
		}

		term, err := evaluate(piece, sign, source, &budget)
		if err != nil {
			return err
		}
		result.Terms = append(result.Terms, term)
		total += sign * term.Subtotal
		return nil
	}

	for index := 0; index < len(normalised); index++ {
		character := normalised[index]
		if (character == '+' || character == '-') && index > 0 {
			if err := flush(); err != nil {
				return Result{}, err
			}
			sign = 1
			if character == '-' {
				sign = -1
			}
			continue
		}
		if character == '+' && index == 0 {
			continue
		}
		if character == '-' && index == 0 {
			sign = -1
			continue
		}
		buffer.WriteByte(character)
	}

	if err := flush(); err != nil {
		return Result{}, err
	}

	result.Total = total
	return result, nil
}

func evaluate(piece string, sign int, source Source, budget *int) (Term, error) {
	term := Term{Sign: sign, Expression: piece}

	if constant, err := strconv.Atoi(piece); err == nil {
		term.Constant = &constant
		term.Subtotal = constant
		return term, nil
	}

	match := termPattern.FindStringSubmatch(piece)
	if match == nil {
		return Term{}, fmt.Errorf("%w: %q", ErrMalformed, piece)
	}

	count := 1
	if match[1] != "" {
		parsed, err := strconv.Atoi(match[1])
		if err != nil {
			return Term{}, ErrMalformed
		}
		count = parsed
	}

	faces, err := strconv.Atoi(match[2])
	if err != nil || faces < 2 || faces > MaxFaces {
		return Term{}, fmt.Errorf("%w: d%s", ErrUnsupportedDie, match[2])
	}
	if count < 1 {
		return Term{}, fmt.Errorf("%w: %q", ErrMalformed, piece)
	}
	if count > *budget {
		return Term{}, ErrTooManyDice
	}
	*budget -= count

	dice := make([]Die, 0, count)
	for i := 0; i < count; i++ {
		dice = append(dice, Die{Faces: faces, Value: source.Roll(faces), Kept: true})
	}

	if match[3] != "" {
		keep, err := strconv.Atoi(match[4])
		if err != nil || keep < 0 {
			return Term{}, ErrMalformed
		}
		applySelection(dice, match[3], keep)
	}

	subtotal := 0
	for _, die := range dice {
		if die.Kept {
			subtotal += die.Value
		}
	}

	term.Dice = dice
	term.Subtotal = subtotal
	return term, nil
}

func applySelection(dice []Die, mode string, amount int) {
	order := make([]int, len(dice))
	for index := range order {
		order[index] = index
	}

	descending := mode == "kh" || mode == "dh"
	for i := 1; i < len(order); i++ {
		for j := i; j > 0; j-- {
			left, right := dice[order[j-1]].Value, dice[order[j]].Value
			if (descending && right > left) || (!descending && right < left) {
				order[j], order[j-1] = order[j-1], order[j]
				continue
			}
			break
		}
	}

	keeping := mode == "kh" || mode == "kl"
	for position, index := range order {
		if keeping {
			dice[index].Kept = position < amount
		} else if position < amount {
			dice[index].Kept = false
		}
	}
}

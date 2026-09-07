package dice

import (
	"errors"
	"strings"
	"testing"
)

func fixed(values ...int) Source {
	return &FixedSource{Values: values}
}

func TestConstantsAndSimpleRolls(t *testing.T) {
	tests := []struct {
		expression string
		source     Source
		total      int
	}{
		{"7", fixed(), 7},
		{"1d6", fixed(4), 4},
		{"3d6", fixed(2, 5, 6), 13},
		{"2d8+3", fixed(7, 1), 11},
		{"1d20-2", fixed(15), 13},
		{"1d4+1d6+2", fixed(3, 5), 10},
		{"-1d6+10", fixed(4), 6},
	}

	for _, test := range tests {
		result, err := Roll(test.expression, test.source)
		if err != nil {
			t.Fatalf("%s: %v", test.expression, err)
		}
		if result.Total != test.total {
			t.Errorf("%s = %d, want %d", test.expression, result.Total, test.total)
		}
	}
}

func TestKeepAndDrop(t *testing.T) {
	tests := []struct {
		expression string
		values     []int
		total      int
		kept       []int
	}{
		{"4d6kh3", []int{1, 4, 5, 6}, 15, []int{4, 5, 6}},
		{"2d20kh1", []int{7, 18}, 18, []int{18}},
		{"2d20kl1", []int{7, 18}, 7, []int{7}},
		{"4d6dl1", []int{1, 4, 5, 6}, 15, []int{4, 5, 6}},
	}

	for _, test := range tests {
		result, err := Roll(test.expression, fixed(test.values...))
		if err != nil {
			t.Fatalf("%s: %v", test.expression, err)
		}
		if result.Total != test.total {
			t.Errorf("%s = %d, want %d", test.expression, result.Total, test.total)
		}

		var kept []int
		for _, die := range result.Terms[0].Dice {
			if die.Kept {
				kept = append(kept, die.Value)
			}
		}
		if len(kept) != len(test.kept) {
			t.Errorf("%s kept %v, want %v", test.expression, kept, test.kept)
		}
	}
}

func TestEveryDieIsReported(t *testing.T) {
	result, err := Roll("4d6kh3", fixed(1, 4, 5, 6))
	if err != nil {
		t.Fatalf("roll: %v", err)
	}
	if len(result.Terms[0].Dice) != 4 {
		t.Errorf("reported %d dice, a dropped die must still be shown", len(result.Terms[0].Dice))
	}
}

func TestMalformedExpressionsAreRejected(t *testing.T) {
	for _, expression := range []string{
		"", "   ", "d", "1d", "d0", "1d1", "2d6kh", "abc", "1d6++2", "0d6",
		"1d99999", strings.Repeat("1d6+", 80) + "1d6",
	} {
		if _, err := Roll(expression, fixed(3)); err == nil {
			t.Errorf("accepted %q", expression)
		}
	}
}

func TestDiceBudgetIsEnforced(t *testing.T) {
	if _, err := Roll("500d6", fixed()); !errors.Is(err, ErrTooManyDice) {
		t.Errorf("err = %v, want ErrTooManyDice", err)
	}
	if _, err := Roll("150d6+150d6", fixed()); !errors.Is(err, ErrTooManyDice) {
		t.Errorf("budget is per expression, not per term: %v", err)
	}
}

func TestCryptoSourceStaysInRange(t *testing.T) {
	source := CryptoSource()
	for i := 0; i < 500; i++ {
		value := source.Roll(20)
		if value < 1 || value > 20 {
			t.Fatalf("d20 produced %d", value)
		}
	}
}

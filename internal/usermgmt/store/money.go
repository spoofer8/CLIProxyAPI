package store

import (
	"errors"
	"math/big"
	"strings"
)

// USD values have 18 decimal places. A price with 12 decimal places per million
// tokens therefore remains exact even for one token. Never round through float64.
const MoneyScale = 18

var ErrInvalidMoney = errors.New("money must be a non-negative decimal with at most 18 decimal places and 20 integer digits")

func ParseMoney(value string) (*big.Int, error) {
	if value == "" || strings.TrimSpace(value) != value {
		return nil, ErrInvalidMoney
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 || len(parts[0]) == 0 || len(parts[0]) > 20 {
		return nil, ErrInvalidMoney
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
		if len(fraction) == 0 || len(fraction) > MoneyScale {
			return nil, ErrInvalidMoney
		}
	}
	for _, c := range parts[0] + fraction {
		if c < '0' || c > '9' {
			return nil, ErrInvalidMoney
		}
	}
	n, ok := new(big.Int).SetString(parts[0]+fraction+strings.Repeat("0", MoneyScale-len(fraction)), 10)
	if !ok {
		return nil, ErrInvalidMoney
	}
	return n, nil
}
func FormatMoney(n *big.Int) string {
	if n == nil || n.Sign() == 0 {
		return "0"
	}
	s := n.String()
	if len(s) <= MoneyScale {
		s = strings.Repeat("0", MoneyScale+1-len(s)) + s
	}
	s = s[:len(s)-MoneyScale] + "." + s[len(s)-MoneyScale:]
	return strings.TrimRight(strings.TrimRight(s, "0"), ".")
}
func NormalizeMoney(v string) (string, error) {
	n, e := ParseMoney(v)
	if e != nil {
		return "", e
	}
	return FormatMoney(n), nil
}
func NormalizeRate(v string) (string, error) {
	if p := strings.Split(v, "."); len(p) > 1 && len(p[1]) > 12 {
		return "", ErrInvalidMoney
	}
	return NormalizeMoney(v)
}

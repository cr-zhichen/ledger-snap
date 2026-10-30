package recognize

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var moneyPattern = regexp.MustCompile(`^\d+(?:\.\d{1,2})?$`)

func amountIn(s string) (string, error) {
	s = strings.TrimSpace(s)
	if !moneyPattern.MatchString(s) {
		return "", fmt.Errorf("金额格式不正确：%s", s)
	}
	return normalizeMoney(s)
}

func normalizeMoney(s string) (string, error) {
	whole, frac, _ := strings.Cut(s, ".")
	if _, err := strconv.ParseUint(whole, 10, 64); err != nil {
		return "", err
	}
	frac = (frac + "00")[:2]
	whole = strings.TrimLeft(whole, "0")
	if whole == "" {
		whole = "0"
	}
	return whole + "." + frac, nil
}

// toCents reads a normalized amount such as "12.50"; 0 when it is not one.
func toCents(s string) int64 {
	whole, frac, _ := strings.Cut(s, ".")
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || len(frac) > 2 {
		return 0
	}
	f, _ := strconv.ParseInt((frac + "00")[:2], 10, 64)
	return w*100 + f
}

func fromCents(c int64) string { return fmt.Sprintf("%d.%02d", c/100, c%100) }

package recognize

import (
	"regexp"
	"strings"
)

// Dollar marks a bare "$", which can be any dollar. finishCurrency resolves
// it against the account: USD unless the account is in another dollar.
const Dollar = "$"

// Currency codes follow ISO 4217, as Qianji does. These are the ones a
// screenshot may show as a bare three-letter code.
var currencyCodes = map[string]bool{}

func init() {
	for _, c := range strings.Fields("CNY HKD USD EUR GBP JPY KRW SGD TWD MOP AUD CAD CHF THB MYR NZD PHP IDR VND INR AED SEK NOK DKK RUB") {
		currencyCodes[c] = true
	}
}

var currencyWords = map[string]string{
	"人民币": "CNY", "元": "CNY", "RMB": "CNY", "¥": "CNY", "￥": "CNY",
	"港元": "HKD", "港币": "HKD", "HK$": "HKD",
	"美元": "USD", "US$": "USD", "$": Dollar,
	"日元": "JPY", "円": "JPY", "J¥": "JPY", "JP¥": "JPY",
	"欧元": "EUR", "€": "EUR", "英镑": "GBP", "£": "GBP",
	"澳元": "AUD", "A$": "AUD", "加元": "CAD", "C$": "CAD",
	"新加坡元": "SGD", "新币": "SGD", "S$": "SGD", "新台币": "TWD", "NT$": "TWD",
	"澳门元": "MOP", "澳门币": "MOP", "MOP$": "MOP",
	"韩元": "KRW", "₩": "KRW", "泰铢": "THB", "฿": "THB", "瑞士法郎": "CHF",
}

// dollars are the currencies a bare "$" may stand for.
var dollars = map[string]bool{"USD": true, "HKD": true, "TWD": true, "SGD": true, "AUD": true, "CAD": true, "NZD": true, "MOP": true}

var (
	// Longer markers come first so "HK$" wins over "$" and "港元" over "元".
	currencyBefore = regexp.MustCompile(`(?:^|[^A-Za-z])(MOP\$|US\$|HK\$|NT\$|JP¥|J¥|S\$|A\$|C\$|\$|¥|￥|€|£|₩|฿|RMB|[A-Z]{3})\s*$`)
	currencyAfter  = regexp.MustCompile(`^\s*(新加坡元|瑞士法郎|新台币|澳门元|澳门币|人民币|港元|港币|美元|日元|欧元|英镑|澳元|加元|新币|韩元|泰铢|円|元|RMB|[A-Z]{3})(?:$|[^A-Za-z])`)
	// currencyInRow finds currency names anywhere in a row, e.g. "币种：美元"
	// or "入账金额(人民币)"; a bare "元" is too common to count here.
	currencyInRow = regexp.MustCompile(`(新加坡元|瑞士法郎|新台币|澳门元|澳门币|人民币|港元|港币|美元|日元|欧元|英镑|澳元|加元|韩元|泰铢)|(?:^|[^A-Za-z])(RMB|[A-Z]{3})(?:$|[^A-Za-z])`)
)

// CurrencyCode maps a marker or name ("HK$", "美元", "usd") to its code, or
// "" when it is not one.
func CurrencyCode(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	if c, ok := currencyWords[s]; ok {
		return c
	}
	if currencyCodes[s] {
		return s
	}
	return ""
}

// amountCurrency reads the currency written right before or after an amount
// in its line (adjacent), then falls back to the only currency named in the
// row.
func amountCurrency(before, after, row string) (code string, adjacent bool) {
	if m := currencyBefore.FindStringSubmatch(before); m != nil {
		if c := CurrencyCode(m[1]); c != "" {
			return c, true
		}
	}
	if m := currencyAfter.FindStringSubmatch(after); m != nil {
		if c := CurrencyCode(m[1]); c != "" {
			return c, true
		}
	}
	for _, m := range currencyInRow.FindAllStringSubmatch(row, -1) {
		c := CurrencyCode(m[1] + m[2])
		if c == "" {
			continue
		}
		if code != "" && code != c {
			return "", false
		}
		code = c
	}
	return code, false
}

// resolveDollar picks the dollar a bare "$" means, given the currencies of
// the accounts involved.
func resolveDollar(code string, accounts ...string) string {
	if code != Dollar {
		return code
	}
	for _, a := range accounts {
		if dollars[a] {
			return a
		}
	}
	return "USD"
}

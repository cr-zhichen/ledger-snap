package recognize

import (
	"strings"

	"ledger-snap/internal/qianji"
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

// finishCurrency resolves a bare "$" against the accounts' currencies. A
// detected currency is kept even when it matches the account, so switching
// to another account later does not change what was paid.
func finishCurrency(d *Draft, c qianji.Catalog) {
	if d.Status != "ok" {
		return
	}
	var accounts []string
	for _, id := range []string{d.AssetID, d.TargetID} {
		if cur, ok := c.AssetCurrency(id); ok {
			accounts = append(accounts, cur)
		}
	}
	d.Currency = resolveDollar(d.Currency, accounts...)
	d.ConvertedCurrency = resolveDollar(d.ConvertedCurrency, accounts...)
	if d.Converted == "" {
		d.ConvertedCurrency = ""
	}
}

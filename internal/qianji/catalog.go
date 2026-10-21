package qianji

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
)

type Category struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Path     string `json:"path"` // "父分类/子分类"
	Type     int    `json:"type"` // TypeExpense or TypeIncome
	BookID   string `json:"bookid"`
	ParentID string `json:"parentid"`
	Leaf     bool   `json:"leaf"`
}

type Asset struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Remark   string `json:"remark"`
	Type     int    `json:"type"` // AssetTypeCredit for credit accounts
	SubType  int    `json:"stype"`
	Currency string `json:"currency"`
}

// AssetTypeCredit is the asset type of credit cards, Huabei and the like.
const AssetTypeCredit = 2

type Catalog struct {
	Categories []Category `json:"categories"`
	Assets     []Asset    `json:"assets"`
	// BaseCurrency is the user's base currency; empty means DefaultCurrency.
	BaseCurrency string `json:"base_currency,omitempty"`
	// Prices maps a currency code to the price of one unit in CNY, as
	// currency/listv2 reports it.
	Prices map[string]float64 `json:"prices,omitempty"`
}

// DefaultCurrency is the base currency of users who never picked one, and
// the currency of assets that do not name one.
const DefaultCurrency = "CNY"

func (c Catalog) Base() string {
	if c.BaseCurrency == "" {
		return DefaultCurrency
	}
	return c.BaseCurrency
}

// AssetCurrency returns the asset's currency; ok is false when the asset is
// not in the catalog.
func (c Catalog) AssetCurrency(id string) (string, bool) {
	a, ok := c.Asset(id)
	if !ok {
		return "", false
	}
	if a.Currency == "" {
		return DefaultCurrency, true
	}
	return a.Currency, true
}

// Convert converts an amount with Qianji's prices, the same ones the app
// suggests; ok is false when a price is missing.
func (c Catalog) Convert(amount float64, from, to string) (float64, bool) {
	if from == to {
		return amount, true
	}
	price := func(code string) float64 {
		if p := c.Prices[code]; p > 0 {
			return p
		}
		if code == "CNY" {
			return 1
		}
		return 0
	}
	pf, pt := price(from), price(to)
	if pf == 0 || pt == 0 {
		return 0, false
	}
	return amount * pf / pt, true
}

// ParsePrices reads currency/listv2 items, whose baseprice is a string.
func ParsePrices(items []json.RawMessage) (map[string]float64, error) {
	prices := map[string]float64{}
	for _, raw := range items {
		var item struct {
			Symbol string      `json:"symbol"`
			Price  json.Number `json:"baseprice"`
		}
		if err := decodeNumbers(raw, &item); err != nil {
			return nil, err
		}
		if p, err := item.Price.Float64(); err == nil && p > 0 && item.Symbol != "" {
			prices[item.Symbol] = p
		}
	}
	return prices, nil
}

type rawCategory struct {
	ID       json.Number   `json:"id"`
	Name     string        `json:"name"`
	Type     int           `json:"type"`
	BookID   json.Number   `json:"bookid"`
	ParentID json.Number   `json:"parentid"`
	Sub      []rawCategory `json:"sublist"`
}

type rawAsset struct {
	ID       json.Number `json:"id"`
	Name     string      `json:"name"`
	Remark   string      `json:"remark"`
	Type     int         `json:"type"`
	SubType  int         `json:"stype"`
	Currency string      `json:"currency"`
	Status   int         `json:"status"`
	// Some responses group assets; children then sit under one of these keys.
	List   []rawAsset `json:"list"`
	Assets []rawAsset `json:"assets"`
}

// ParseCatalog flattens the category tree and asset groups.
func ParseCatalog(categories, assets []json.RawMessage) (Catalog, error) {
	var c Catalog
	var walk func(rc rawCategory, parent string)
	walk = func(rc rawCategory, parent string) {
		path := rc.Name
		if parent != "" {
			path = parent + "/" + rc.Name
		}
		c.Categories = append(c.Categories, Category{
			ID: rc.ID.String(), Name: rc.Name, Path: path, Type: rc.Type,
			BookID: rc.BookID.String(), ParentID: rc.ParentID.String(), Leaf: len(rc.Sub) == 0,
		})
		for _, sub := range rc.Sub {
			walk(sub, path)
		}
	}
	for _, raw := range categories {
		var rc rawCategory
		if err := decodeNumbers(raw, &rc); err != nil {
			return Catalog{}, err
		}
		walk(rc, "")
	}
	var flatten func(ra rawAsset)
	flatten = func(ra rawAsset) {
		children := append(ra.List, ra.Assets...)
		if ra.ID != "" && len(children) == 0 {
			c.Assets = append(c.Assets, Asset{
				ID: ra.ID.String(), Name: strings.TrimSpace(ra.Name), Remark: ra.Remark,
				Type: ra.Type, SubType: ra.SubType, Currency: ra.Currency,
			})
		}
		for _, child := range children {
			flatten(child)
		}
	}
	for _, raw := range assets {
		var ra rawAsset
		if err := decodeNumbers(raw, &ra); err != nil {
			return Catalog{}, err
		}
		flatten(ra)
	}
	return c, nil
}

func (c Catalog) Category(id string) (Category, bool) {
	for _, cat := range c.Categories {
		if cat.ID == id {
			return cat, true
		}
	}
	return Category{}, false
}

func (c Catalog) Asset(id string) (Asset, bool) {
	for _, a := range c.Assets {
		if a.ID == id {
			return a, true
		}
	}
	return Asset{}, false
}

// Leaves returns the selectable categories of one type: leaves, plus parents
// that have no children.
func (c Catalog) Leaves(kind int) []Category {
	var out []Category
	for _, cat := range c.Categories {
		if cat.Type == kind && cat.Leaf {
			out = append(out, cat)
		}
	}
	return out
}

// AssetSpec describes a new asset account. Type and SubType follow the app:
// type 1 money (stype 11 cash, 12 debit card, 13 Alipay, 14 WeChat, 103
// Yu'e Bao) and type 2 credit (stype 21 credit card, 22 Huabei).
type AssetSpec struct {
	Name     string
	Remark   string
	Type     int
	SubType  int
	Icon     string
	Currency string // DefaultCurrency when empty
}

// AssetIcons maps asset subtype to the app's icon URL via /asset/typelist.
func (c *Client) AssetIcons(ctx context.Context, s Session) (map[int]string, error) {
	data, err := c.post(ctx, "/asset/typelist", s, url.Values{})
	if err != nil {
		return nil, err
	}
	icons := map[int]string{}
	var walk func(raw json.RawMessage)
	walk = func(raw json.RawMessage) {
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) == nil {
			var t struct {
				SubType int    `json:"stype"`
				Icon    string `json:"icon"`
			}
			if json.Unmarshal(raw, &t) == nil && t.Icon != "" && t.SubType != 0 {
				if _, ok := icons[t.SubType]; !ok {
					icons[t.SubType] = t.Icon
				}
			}
			for _, v := range obj {
				walk(v)
			}
			return
		}
		var list []json.RawMessage
		if json.Unmarshal(raw, &list) == nil {
			for _, v := range list {
				walk(v)
			}
		}
	}
	walk(data)
	return icons, nil
}

// CreateAsset adds an asset account through /asset/submit and returns its ID.
func (c *Client) CreateAsset(ctx context.Context, s Session, spec AssetSpec) (string, error) {
	currency := spec.Currency
	if currency == "" {
		currency = DefaultCurrency
	}
	asset := map[string]any{
		"name": spec.Name, "remark": spec.Remark, "type": spec.Type, "stype": spec.SubType,
		"money": 0, "currency": currency, "incount": 1, "status": 0, "userid": s.UID, "icon": spec.Icon,
	}
	if spec.Type == AssetTypeCredit {
		// Credit accounts are rejected without billing-cycle info.
		asset["credit"] = map[string]any{"statedate": 5, "paydate": 23, "limit": 50000, "statein": 0}
	}
	v, err := json.Marshal(asset)
	if err != nil {
		return "", err
	}
	data, err := c.post(ctx, "/asset/submit", s, url.Values{"v": {string(v)}})
	if err != nil {
		return "", err
	}
	var result struct {
		Asset struct {
			ID json.Number `json:"id"`
		} `json:"asset"`
	}
	if err := decodeNumbers(data, &result); err != nil || result.Asset.ID == "" {
		return "", &Error{Kind: ErrInvalidResponse, Path: "/asset/submit", Message: "响应缺少 asset.id"}
	}
	return result.Asset.ID.String(), nil
}

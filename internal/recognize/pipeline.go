package recognize

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"ledger-snap/internal/config"
	"ledger-snap/internal/imgutil"
	"ledger-snap/internal/openrouter"
	"ledger-snap/internal/qianji"
)

type Trace struct {
	ExtractionModel string                        `json:"extraction_model"`
	ExtractionMs    int64                         `json:"extraction_ms"`
	Extraction      *Extraction                   `json:"extraction,omitempty"`
	DecisionModel   string                        `json:"decision_model"`
	DecisionMs      int64                         `json:"decision_ms"`
	Decision        *openrouter.DecisionsResponse `json:"decision,omitempty"`
	History         map[string][]PastBill         `json:"history,omitempty"`
	HistoryError    string                        `json:"history_error,omitempty"`
	CatalogRefresh  string                        `json:"catalog_refresh,omitempty"`
	TotalMs         int64                         `json:"total_ms"`
	Cost            float64                       `json:"cost"`
}

type Decider interface {
	Decide(context.Context, openrouter.DecisionsRequest) (*openrouter.DecisionsResponse, error)
}
type ChatClient interface {
	ChatJSON(context.Context, openrouter.ChatRequest, any) (*openrouter.ChatResponse, error)
}
type Recognizer struct {
	Config  *config.Config
	Decider Decider
	Router  ChatClient
}
type Input struct {
	Image      []byte
	UploadTime time.Time
	Catalog    qianji.Catalog
	Memory     Memory
	History    History
}

func (r *Recognizer) Recognize(ctx context.Context, in Input) ([]*Draft, *Trace, error) {
	start := time.Now()
	trace := &Trace{ExtractionModel: r.Config.Extraction.Model}
	defer func() { trace.TotalMs = time.Since(start).Milliseconds() }()
	cfg := r.Config.Extraction
	image, err := imgutil.Fit(in.Image, cfg.ImageMaxSide, cfg.ImageMinSide, cfg.JPEGQuality)
	if err != nil {
		return nil, trace, err
	}
	var extracted Extraction
	req := extractionRequest(cfg, "transactions", extractionSchema, extractionInstructions, image)
	begin := time.Now()
	response, err := r.Router.ChatJSON(ctx, req, &extracted)
	trace.ExtractionMs = time.Since(begin).Milliseconds()
	if response != nil {
		trace.Cost += response.Usage.Cost
	}
	if err != nil {
		return nil, trace, err
	}
	trace.Extraction = &extracted
	if extracted.Transactions == nil {
		return nil, trace, fmt.Errorf("没有收到交易列表，请重新识别")
	}
	drafts := make([]*Draft, 0, len(extracted.Transactions))
	for i, f := range extracted.Transactions {
		d, err := r.draft(f, in.UploadTime)
		if err != nil {
			return nil, trace, fmt.Errorf("第 %d 笔识别结果无法使用：%w", i+1, err)
		}
		drafts = append(drafts, d)
	}
	if err := r.Match(ctx, drafts, in, trace); err != nil {
		return nil, trace, err
	}
	return drafts, trace, nil
}

const MaxChoiceOptions = 254

// Match never changes extracted amounts, currencies or times. All bills share
// one decision request; question names identify the bill they belong to.
func (r *Recognizer) Match(ctx context.Context, drafts []*Draft, in Input, trace *Trace) error {
	var names []string
	for _, d := range drafts {
		if d.Merchant != "" && !slices.Contains(names, d.Merchant) {
			names = append(names, d.Merchant)
		}
	}
	past := r.lookupHistory(ctx, in, names, trace)
	questions := map[string]openrouter.Question{}
	categories := map[string]string{}
	assets := map[string]string{}
	var assetOptions []openrouter.Option
	if len(in.Catalog.Assets) > MaxChoiceOptions {
		return fmt.Errorf("账户数量超过支持范围，请减少账户后重试")
	}
	for _, a := range in.Catalog.Assets {
		key := "a" + a.ID
		assets[key] = a.ID
		assetOptions = append(assetOptions, openrouter.Option{Key: key, Description: AssetLabel(a)})
	}
	state := []any{"为交易匹配用户的钱迹账户和分类。各笔彼此独立，只回答指定笔的问题。付款平台不等于余额账户，但明确的方式可按同义名称匹配：零钱对应微信零钱；银行名称和卡号尾号相同可对应具体银行卡，不要求名称逐字相同。没有对应账户时选 none。"}
	for i, d := range drafts {
		d.CategoryID, d.AssetID, d.TargetID = "", "", ""
		d.Uncertain = remove(remove(remove(d.Uncertain, "category"), "asset"), "target_asset")
		if d.Status != "ok" || d.Kind == KindRefund {
			continue
		}
		prefix := fmt.Sprintf("b%d_", i)
		facts, _ := json.Marshal(d)
		state = append(state, fmt.Sprintf("第 %d 笔（问题前缀 %s）：%s", i+1, prefix, facts))
		if d.Kind == KindExpense || d.Kind == KindIncome {
			leaves := in.Catalog.Leaves(kindType(d.Kind))
			if len(leaves) > MaxChoiceOptions {
				return fmt.Errorf("分类数量超过支持范围，请减少分类后重试")
			}
			opts := []openrouter.Option{{Key: "none", Description: "没有合适的分类"}}
			for _, cat := range leaves {
				key := "c" + cat.ID
				categories[key] = cat.ID
				opts = append(opts, openrouter.Option{Key: key, Description: cat.Path})
			}
			prompt := fmt.Sprintf("第 %d 笔应该记到哪个分类？用户明确指定的分类优先。", i+1)
			if len(past[d.Merchant]) > 0 {
				prompt += "以用户的习惯为准，优先于一般常识，参考同商户中时间段和金额最相近的历史账单。"
			}
			questions[prefix+"category"] = openrouter.ChoiceQuestion(prompt, opts)
		}
		if len(assetOptions) > 0 {
			opts := append([]openrouter.Option{{Key: "none", Description: "截图未明确账户，或所有账户都不对应"}}, assetOptions...)
			questions[prefix+"asset"] = openrouter.ChoiceQuestion(fmt.Sprintf("第 %d 笔的付款或收款账户是哪个？转账、还款时选择转出账户。优先按 payment 和卡号尾号匹配。转出账户不能使用 target_payment 对应的到账账户；零钱提现从微信零钱转出，已领取的微信红包收入到微信零钱。", i+1), opts)
			if d.Kind.IsTransfer() {
				questions[prefix+"target_asset"] = openrouter.ChoiceQuestion(fmt.Sprintf("第 %d 笔的转入账户是哪个？按 target_payment 匹配。", i+1), opts)
			}
		}
	}
	if len(questions) == 0 {
		return nil
	}
	if h := r.historyText(past, in.Catalog); h != "" {
		state = append(state, h)
	}
	trace.DecisionModel = r.Config.Decision.Provider + ":" + r.Config.Decision.Model
	start := time.Now()
	resp, err := r.Decider.Decide(ctx, openrouter.DecisionsRequest{Model: r.Config.Decision.Model, State: state, Questions: questions})
	trace.DecisionMs += time.Since(start).Milliseconds()
	if err != nil {
		return err
	}
	trace.Decision = resp
	trace.Cost += resp.Usage.Cost
	for i, d := range drafts {
		if d.Status != "ok" || d.Kind == KindRefund {
			continue
		}
		prefix := fmt.Sprintf("b%d_", i)
		for _, field := range []string{"category", "asset", "target_asset"} {
			q := prefix + field
			if _, asked := questions[q]; !asked {
				continue
			}
			answer, ok := resp.Answers[q]
			if !ok {
				return fmt.Errorf("账户或分类匹配不完整，请重新识别")
			}
			if _, valid := questions[q].Criteria.(map[string]any)[answer.Choice]; !valid {
				return fmt.Errorf("返回了不可选的账户或分类，请重新识别")
			}
			id := assets[answer.Choice]
			threshold := r.Config.Decision.Thresholds.Asset
			if field == "category" {
				id = categories[answer.Choice]
				threshold = r.Config.Decision.Thresholds.Category
				d.CategoryID = id
			} else if field == "asset" {
				d.AssetID = id
			} else {
				d.TargetID = id
			}
			if answer.Choice != "none" && id == "" {
				return fmt.Errorf("返回了不存在的账户或分类，请重新识别")
			}
			if answer.Prob() < threshold || field == "category" && id == "" {
				d.mark(field)
			}
		}
		if d.Kind == KindExpense || d.Kind == KindIncome {
			// Explicit user corrections to payment labels override model matching.
			if id := in.Memory.AssetAliases[d.Payment]; id != "" {
				if _, ok := in.Catalog.Asset(id); ok {
					d.AssetID = id
					d.Uncertain = remove(d.Uncertain, "asset")
				}
			}
			if d.CategoryHint == "" {
				if id, ok := historyCategory(past[d.Merchant], d.Kind); ok {
					if c, ok := in.Catalog.Category(id); ok && c.Type == kindType(d.Kind) {
						d.CategoryID = id
						d.Uncertain = remove(d.Uncertain, "category")
					}
				}
			}
		}
		if (d.Kind == KindExpense || d.Kind == KindIncome) && d.CategoryID == "" {
			d.mark("category")
		}
		finishCurrency(d, in.Catalog)
	}
	return nil
}

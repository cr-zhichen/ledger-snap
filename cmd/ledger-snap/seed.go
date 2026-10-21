package main

import (
	"context"
	"flag"
	"fmt"

	"ledger-snap/internal/config"
	"ledger-snap/internal/qianji"
)

// testAssets are deliberately easy to confuse: shared card tails across
// banks, tails kept in the remark instead of the name, and several wallets.
var testAssets = []qianji.AssetSpec{
	{Name: "招商银行信用卡(1234)", Type: 2, SubType: 21},
	{Name: "招行经典白", Remark: "信用卡 尾号8888", Type: 2, SubType: 21},
	{Name: "招商银行储蓄卡(5678)", Type: 1, SubType: 12},
	{Name: "中信信用卡", Remark: "尾号1234", Type: 2, SubType: 21},
	{Name: "工行卡", Remark: "储蓄卡 尾号5678", Type: 1, SubType: 12},
	{Name: "建设银行储蓄卡(0921)", Type: 1, SubType: 12},
	{Name: "支付宝余额", Type: 1, SubType: 13},
	{Name: "余额宝", Type: 1, SubType: 103},
	{Name: "花呗", Type: 2, SubType: 22},
	{Name: "微信零钱", Type: 1, SubType: 14},
	{Name: "零钱通", Type: 1, SubType: 14},
}

// runSeedAssets creates the test assets that do not exist yet.
func runSeedAssets(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("seed-assets", flag.ExitOnError)
	cfgPath := fs.String("config", "config.toml", "配置文件")
	_ = fs.Parse(args)
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	account, err := config.Secret("QIANJI_TEST_ACCOUNT")
	if err != nil {
		return err
	}
	digest, err := config.Secret("QIANJI_TEST_PASSWORD_MD5")
	if err != nil {
		return err
	}
	c := newQianji(cfg)
	s, err := c.Login(ctx, account, digest, randomDevice())
	if err != nil {
		return err
	}
	raw, err := c.Assets(ctx, s, 0)
	if err != nil {
		return err
	}
	catalog, err := qianji.ParseCatalog(nil, raw)
	if err != nil {
		return err
	}
	icons, err := c.AssetIcons(ctx, s)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, a := range catalog.Assets {
		have[a.Name] = true
	}
	for _, spec := range testAssets {
		if have[spec.Name] {
			fmt.Printf("已存在 %s\n", spec.Name)
			continue
		}
		spec.Icon = icons[spec.SubType]
		if spec.Icon == "" {
			spec.Icon = "https://res.qianjiapp.com/asset/cash.png"
		}
		id, err := c.CreateAsset(ctx, s, spec)
		if err != nil {
			return fmt.Errorf("新建 %s: %w", spec.Name, err)
		}
		fmt.Printf("已新建 %s（%s）\n", spec.Name, id)
	}
	return nil
}

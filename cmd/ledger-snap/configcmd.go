package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"ledger-snap/internal/config"
)

// runConfig prints every key with the variable that overrides it, so the
// names need not be looked up.
func runConfig(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("config", flag.ExitOnError)
	cfgPath := fs.String("config", "config.toml", "配置文件")
	_ = fs.Parse(args)
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "配置项\t环境变量\t当前值")
	for _, f := range config.Fields(cfg) {
		value := f.Value
		if f.FromEnv {
			value += "（来自环境变量）"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", f.Key, f.Env, value)
	}
	return w.Flush()
}

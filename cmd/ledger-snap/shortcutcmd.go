package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"ledger-snap/internal/shortcut"
)

// runShortcut writes the shortcut and, unless --unsigned, signs it with the
// macOS shortcuts tool: iOS only imports signed shortcut files.
func runShortcut(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("shortcut", flag.ExitOnError)
	out := fs.String("out", "dist/截图记账.shortcut", "输出文件；导入后快捷指令的名称就是文件名")
	server := fs.String("server", "", "第一次登录时“服务器地址”的默认答案")
	invite := fs.String("invite", "", "第一次登录时“邀请码”的默认答案")
	unsigned := fs.Bool("unsigned", false, "只生成未签名的 plist，不调用 shortcuts sign")
	_ = fs.Parse(args)

	data, err := shortcut.Build(shortcut.Options{Server: *server, Invite: *invite})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		return err
	}
	if *unsigned {
		return os.WriteFile(*out, data, 0o644)
	}
	if _, err := exec.LookPath("shortcuts"); err != nil {
		return fmt.Errorf("签名需要 macOS 自带的 shortcuts 命令；在其他系统上可以加 --unsigned 只生成文件")
	}
	// shortcuts sign only reads files named *.shortcut.
	plain, err := os.CreateTemp(filepath.Dir(*out), "unsigned-*.shortcut")
	if err != nil {
		return err
	}
	defer os.Remove(plain.Name())
	if _, err := plain.Write(data); err != nil {
		plain.Close()
		return err
	}
	if err := plain.Close(); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "shortcuts", "sign", "--mode", "anyone", "--input", plain.Name(), "--output", *out)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("shortcuts sign: %w", err)
	}
	fmt.Println("已生成", *out)
	return nil
}

package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// defaultID is distroless's nonroot user, used when .env sets no UID or GID.
const defaultID = 65532

// dropRoot runs when the container starts as root: it hands the data
// directory to LEDGER_SNAP_UID:LEDGER_SNAP_GID and continues as that user, so
// a bind-mounted ./data works whoever created it. LEDGER_SNAP_UID=0 keeps
// running as root.
func dropRoot(dataDir string) error {
	if os.Getuid() != 0 {
		return nil
	}
	uid, err := idFromEnv("LEDGER_SNAP_UID")
	if err != nil {
		return err
	}
	gid, err := idFromEnv("LEDGER_SNAP_GID")
	if err != nil {
		return err
	}
	if uid == 0 {
		return nil
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	if err := chownTree(dataDir, uid, gid); err != nil {
		return fmt.Errorf("把数据目录交给 %d:%d: %w", uid, gid, err)
	}
	if err := syscall.Setgroups(nil); err != nil {
		return err
	}
	if err := syscall.Setgid(gid); err != nil {
		return err
	}
	return syscall.Setuid(uid)
}

func idFromEnv(name string) (int, error) {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return defaultID, nil
	}
	id, err := strconv.Atoi(v)
	if err != nil || id < 0 {
		return 0, fmt.Errorf("%s 应为非负整数，现在是 %q", name, v)
	}
	return id, nil
}

// chownTree changes only entries with another owner, so restarts stay quick.
func chownTree(root string, uid, gid int) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) == uid && int(st.Gid) == gid {
			return nil
		}
		return os.Lchown(path, uid, gid)
	})
}

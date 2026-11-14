//go:build !windows

package keyring

import (
	"os"
	"syscall"
)

// inheritFileAttrs gives the temp file the mode and (best effort, needs privilege) the owner of the
// file it will replace, so an admin's atomic write keeps the permissions install.sh set
// (keyring.json 0640 root:radar must stay readable by the service user).
func inheritFileAttrs(tmp *os.File, target string) {
	fi, err := os.Stat(target)
	if err != nil || !fi.Mode().IsRegular() {
		return
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		_ = tmp.Chown(int(st.Uid), int(st.Gid)) // fails without privilege: ignored
	}
	_ = tmp.Chmod(fi.Mode().Perm())
}

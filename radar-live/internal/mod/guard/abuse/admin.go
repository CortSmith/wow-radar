package abuse

import (
	"errors"
	"fmt"
	"io"
	"net/netip"
	"path/filepath"
	"strings"
	"time"
)

const (
	// BanFileName is the default name of the ban file inside the data directory.
	BanFileName = "abuse-bans.json"
	maxAdminBan = 365 * 24 * time.Hour
	// AdminUsage is printed for bad `guard` commands.
	AdminUsage = "usage: radar-db guard list | unban <ip> | ban <ip> <duration>"
)

// ResolveBanFile picks the ban file: the explicit path (RADAR_ABUSE_BAN_FILE), else BanFileName
// beside the database file, else "" (bans are then kept in memory only).
func ResolveBanFile(explicit, dbPath string) string {
	switch {
	case explicit != "":
		return explicit
	case dbPath != "":
		return filepath.Join(filepath.Dir(dbPath), BanFileName)
	}
	return ""
}

// Admin runs one `guard` subcommand against the ban file and writes its report to out. A running
// server notices the changed file within a few seconds. A corrupt file is reported, never overwritten.
func Admin(args []string, path string, now func() time.Time, out io.Writer) error {
	if now == nil {
		now = time.Now
	}
	if path == "" {
		return errors.New("no ban file: set RADAR_ABUSE_BAN_FILE or RADAR_DB_PATH")
	}
	if len(args) == 0 {
		return errors.New(AdminUsage)
	}
	bans, err := LoadBans(path, now())
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	t := NewTable(Config{MaxFailures: 1, Window: time.Hour, BaseBan: time.Second, MaxBan: maxAdminBan,
		MaxEntries: len(bans) + 16, BanLoopback: true}, now)
	t.Replace(bans)
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return errors.New(AdminUsage)
		}
		for _, b := range t.Bans() {
			fmt.Fprintf(out, "%s\tuntil %s\tstrikes %d\n", b.IP, b.Until.UTC().Format(time.RFC3339), b.Strikes)
		}
		if len(bans) == 0 {
			fmt.Fprintln(out, "no active bans")
		}
		return nil
	case "unban":
		ip, err := adminIP(args, 2)
		if err != nil {
			return err
		}
		if !t.Unban(ip) {
			fmt.Fprintf(out, "%s was not banned\n", ip)
			return nil
		}
		if err := SaveBans(path, t.Bans()); err != nil {
			return err
		}
		fmt.Fprintf(out, "unbanned %s\n", ip)
		return nil
	case "ban":
		ip, err := adminIP(args, 3)
		if err != nil {
			return err
		}
		d, err := time.ParseDuration(args[2])
		if err != nil || d <= 0 || d > maxAdminBan {
			return fmt.Errorf("duration %q must be a positive Go duration up to %v (for example 24h)", args[2], maxAdminBan)
		}
		if err := t.Ban(ip, d); err != nil {
			return err
		}
		if err := SaveBans(path, t.Bans()); err != nil {
			return err
		}
		fmt.Fprintf(out, "banned %s for %v\n", ip, d)
		return nil
	}
	return errors.New(AdminUsage)
}

func adminIP(args []string, want int) (netip.Addr, error) {
	if len(args) != want {
		return netip.Addr{}, errors.New(AdminUsage)
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(args[1]))
	if err != nil {
		return netip.Addr{}, fmt.Errorf("%q is not an IP address", args[1])
	}
	return ip.Unmap(), nil
}

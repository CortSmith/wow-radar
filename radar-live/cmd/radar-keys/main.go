// Command radar-keys manages radar-ingest API keys: a local web page (default) and CLI subcommands
// (list, add, block, unblock, delete, regen, reveal) over the keyring and the sealed key vault.
// No flag takes a key or secret value.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"radarlive/internal/keyring"
	"radarlive/internal/keyvault"
)

var version = "dev"

const usageText = `radar-keys manages radar-ingest API keys.

Usage:
  radar-keys [serve] [--no-browser] [--listen ADDR]
  radar-keys list
  radar-keys add --email E [--player P] [--tag T ...] [--notes N] [--expires YYYY-MM-DD]
                 [--line-rate N] [--line-burst N] [--byte-rate N] [--byte-burst N] [--max-streams N] [--no-vault]
  radar-keys block ID [--reason R]
  radar-keys unblock ID
  radar-keys delete ID
  radar-keys regen ID [--no-vault]
  radar-keys reveal ID
  radar-keys version

ID is a key id or a unique prefix of at least 6 characters.
Paths: --keyring (RADAR_KEYRING_FILE), --vault (RADAR_KEYVAULT_FILE), --vault-key (RADAR_KEYVAULT_KEY_FILE),
--usage (RADAR_KEYUSAGE_FILE).
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

type config struct {
	keyring, vault, vaultKey, usage string
}

func defaultKeyringPath() string {
	if runtime.GOOS == "windows" {
		base := os.Getenv("PROGRAMDATA")
		if base == "" {
			base = `C:\ProgramData`
		}
		return filepath.Join(base, "radar-live", "keyring.json")
	}
	return keysRoot + "/keyring/keyring.json"
}

// The Linux layout install.sh creates (docs/keys.md): the keyring is root-written and group-readable for
// the service user, the usage file is written by the service user, the vault and its sealing key are root only.
const keysRoot = "/var/lib/radar-keys"

func layoutDefault(name string) string {
	if runtime.GOOS == "windows" {
		return ""
	}
	switch name {
	case "vault":
		return keysRoot + "/vault/keyvault.bin"
	case "vaultKey":
		return keysRoot + "/vault/keyvault.key"
	default:
		return keysRoot + "/usage/keyusage.json"
	}
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func envInt(name string, def int64) int64 {
	if v, err := strconv.ParseInt(os.Getenv(name), 10, 64); err == nil && v > 0 {
		return v
	}
	return def
}

func (c *config) register(fs *flag.FlagSet) {
	fs.StringVar(&c.keyring, "keyring", envOr("RADAR_KEYRING_FILE", defaultKeyringPath()), "keyring.json path")
	fs.StringVar(&c.vault, "vault", os.Getenv("RADAR_KEYVAULT_FILE"), "key vault file (default: /var/lib/radar-keys/vault/keyvault.bin, else next to the keyring)")
	fs.StringVar(&c.vaultKey, "vault-key", os.Getenv("RADAR_KEYVAULT_KEY_FILE"), "vault sealing key file (default: /var/lib/radar-keys/vault/keyvault.key, else next to the keyring)")
	fs.StringVar(&c.usage, "usage", os.Getenv("RADAR_KEYUSAGE_FILE"), "keyusage.json path (default: /var/lib/radar-keys/usage/keyusage.json, else next to the keyring)")
}

func (c *config) fill() {
	dir := filepath.Dir(c.keyring)
	standard := c.keyring == defaultKeyringPath() // the installed layout: the three files live in different directories
	pick := func(cur *string, name, file string) {
		if *cur != "" {
			return
		}
		if d := layoutDefault(name); standard && d != "" {
			*cur = d
		} else {
			*cur = filepath.Join(dir, file)
		}
	}
	pick(&c.vault, "vault", "keyvault.bin")
	pick(&c.vaultKey, "vaultKey", "keyvault.key")
	pick(&c.usage, "usage", "keyusage.json")
}

func newApp(c config, lg *log.Logger) (*app, error) {
	c.fill()
	if err := os.MkdirAll(filepath.Dir(c.keyring), 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(c.vault), 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(c.vaultKey), 0o700); err != nil {
		return nil, err
	}
	v, err := keyvault.Open(c.vault, c.vaultKey)
	if err != nil {
		return nil, err
	}
	return &app{
		keyringPath: c.keyring, usagePath: c.usage, vault: v, defaults: defaultLimits,
		log: lg, now: time.Now,
	}, nil
}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// parseWithID parses flags that may come before or after one positional id.
func parseWithID(fs *flag.FlagSet, args []string) (string, error) {
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	id := ""
	if fs.NArg() > 0 {
		id = fs.Arg(0)
		if err := fs.Parse(fs.Args()[1:]); err != nil {
			return "", err
		}
	}
	if id == "" || fs.NArg() > 0 {
		return "", errors.New("expected exactly one key id")
	}
	return id, nil
}

func run(args []string, stdout, stderr io.Writer) int {
	lg := log.New(stderr, "radar-keys: ", log.LstdFlags)
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "serve":
		err = cmdServe(args, stdout, stderr, lg)
	case "list":
		err = cmdList(args, stdout, lg)
	case "add":
		err = cmdAdd(args, stdout, lg)
	case "block":
		err = cmdBlock(args, stdout, lg)
	case "unblock":
		err = cmdSimple("unblock", args, stdout, lg)
	case "delete":
		err = cmdSimple("delete", args, stdout, lg)
	case "regen":
		err = cmdRegen(args, stdout, lg)
	case "reveal":
		err = cmdSimple("reveal", args, stdout, lg)
	case "version":
		fmt.Fprintln(stdout, "radar-keys", version)
	case "help":
		fmt.Fprint(stdout, usageText)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", cmd, usageText)
		return 2
	}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	return 0
}

func newFlags(name string, stderr io.Writer) (*flag.FlagSet, *config) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c config
	c.register(fs)
	return fs, &c
}

// openBrowser is replaceable in tests.
var openBrowser = func(url string) error {
	if runtime.GOOS == "windows" {
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	}
	return exec.Command("xdg-open", url).Start()
}

func cmdServe(args []string, stdout, stderr io.Writer, lg *log.Logger) error {
	fs, c := newFlags("serve", stderr)
	listen := fs.String("listen", "127.0.0.1:0", "listen address (loopback only)")
	noBrowser := fs.Bool("no-browser", false, "do not open the browser")
	d := defaultLimits
	fs.Int64Var(&d.LineRate, "default-line-rate", envInt("RADAR_STREAM_KEY_LINE_RATE", d.LineRate), "server default per-key lines/s (shown in the UI)")
	fs.Int64Var(&d.LineBurst, "default-line-burst", envInt("RADAR_STREAM_KEY_LINE_BURST", d.LineBurst), "server default per-key line burst")
	fs.Int64Var(&d.ByteRate, "default-byte-rate", envInt("RADAR_STREAM_KEY_BYTE_RATE", d.ByteRate), "server default per-key bytes/s")
	fs.Int64Var(&d.ByteBurst, "default-byte-burst", envInt("RADAR_STREAM_KEY_BYTE_BURST", d.ByteBurst), "server default per-key byte burst")
	fs.Int64Var(&d.MaxStreams, "default-max-streams", envInt("RADAR_STREAM_MAX_PER_KEY", d.MaxStreams), "server default streams per key")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return errors.New("unexpected arguments")
	}
	a, err := newApp(*c, lg)
	if err != nil {
		return err
	}
	a.defaults = d
	a.token = newToken()
	ln, err := listenLocal(*listen)
	if err != nil {
		return err
	}
	u := pageURL(ln, a.token)
	fmt.Fprintln(stdout, "radar-keys", version, "is running. Open this address (it contains this run's token):")
	fmt.Fprintln(stdout, u)
	if !*noBrowser {
		if err := openBrowser(u); err != nil {
			lg.Printf("could not open the browser: %v", err)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return a.serve(ctx, ln)
}

func cmdList(args []string, stdout io.Writer, lg *log.Logger) error {
	fs, c := newFlags("list", io.Discard)
	if err := fs.Parse(args); err != nil {
		return err
	}
	a, err := newApp(*c, lg)
	if err != nil {
		return err
	}
	kr, err := a.load()
	if err != nil {
		return err
	}
	usage := readUsage(a.usagePath)
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATUS\tEMAIL\tPLAYER\tTAGS\tKEY")
	for _, e := range kr.Keys {
		v := a.view(e, usage)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", e.ID[:12], v.Status, e.Email, e.Player, strings.Join(e.Tags, ","), v.Masked)
	}
	return tw.Flush()
}

func cmdAdd(args []string, stdout io.Writer, lg *log.Logger) error {
	fs, c := newFlags("add", io.Discard)
	email := fs.String("email", "", "player e-mail (required)")
	player := fs.String("player", "", "player name")
	var tags stringList
	fs.Var(&tags, "tag", "tag (repeatable)")
	notes := fs.String("notes", "", "notes")
	expires := fs.String("expires", "", "expiry date YYYY-MM-DD (or RFC 3339)")
	noVault := fs.Bool("no-vault", false, "do not keep a recoverable copy of the key")
	lim := map[string]*int64{}
	for _, n := range []string{"line-rate", "line-burst", "byte-rate", "byte-burst", "max-streams"} {
		lim[n] = fs.Int64(n, 0, "per-key limit (unset = server default)")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return errors.New("unexpected arguments")
	}
	var l keyring.Limits
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	pick := func(name string) *int64 {
		if !set[name] {
			return nil
		}
		v := *lim[name]
		return &v
	}
	l.LineRate, l.LineBurst, l.ByteRate, l.ByteBurst, l.MaxStreams =
		pick("line-rate"), pick("line-burst"), pick("byte-rate"), pick("byte-burst"), pick("max-streams")
	req := keyReq{Email: *email, Player: *player, Tags: tags, Notes: *notes, Limits: l}
	if *expires != "" {
		s := *expires
		if len(s) == len("2006-01-02") {
			s += "T23:59:59Z"
		}
		req.ExpiresAt = &s
	}
	a, err := newApp(*c, lg)
	if err != nil {
		return err
	}
	raw, e, err := a.createKey(&req, !*noVault)
	if err != nil {
		return cliErr(err)
	}
	fmt.Fprintln(stdout, raw)
	lg.Printf("created key id=%s", e.ID)
	return nil
}

func cliErr(err error) error {
	var ae *apiError
	if errors.As(err, &ae) {
		return errors.New(ae.msg)
	}
	return err
}

func cmdBlock(args []string, stdout io.Writer, lg *log.Logger) error {
	fs, c := newFlags("block", io.Discard)
	reason := fs.String("reason", "", "why the key is blocked")
	prefix, err := parseWithID(fs, args)
	if err != nil {
		return err
	}
	a, err := newApp(*c, lg)
	if err != nil {
		return err
	}
	id, err := a.resolveID(prefix)
	if err != nil {
		return err
	}
	if _, err := a.disable(id, *reason); err != nil {
		return cliErr(err)
	}
	fmt.Fprintln(stdout, "blocked", id)
	return nil
}

func cmdRegen(args []string, stdout io.Writer, lg *log.Logger) error {
	fs, c := newFlags("regen", io.Discard)
	noVault := fs.Bool("no-vault", false, "do not keep a recoverable copy of the new key")
	prefix, err := parseWithID(fs, args)
	if err != nil {
		return err
	}
	a, err := newApp(*c, lg)
	if err != nil {
		return err
	}
	id, err := a.resolveID(prefix)
	if err != nil {
		return err
	}
	raw, e, err := a.regenerate(id, !*noVault)
	if err != nil {
		return cliErr(err)
	}
	fmt.Fprintln(stdout, raw)
	lg.Printf("regenerated key old id=%s new id=%s", id, e.ID)
	return nil
}

// cmdSimple runs unblock, delete and reveal, which take only an id.
func cmdSimple(name string, args []string, stdout io.Writer, lg *log.Logger) error {
	fs, c := newFlags(name, io.Discard)
	prefix, err := parseWithID(fs, args)
	if err != nil {
		return err
	}
	a, err := newApp(*c, lg)
	if err != nil {
		return err
	}
	id, err := a.resolveID(prefix)
	if err != nil {
		return err
	}
	switch name {
	case "unblock":
		if _, err = a.enable(id); err == nil {
			fmt.Fprintln(stdout, "unblocked", id)
		}
	case "delete":
		if err = a.deleteKey(id); err == nil {
			fmt.Fprintln(stdout, "deleted", id)
		}
	case "reveal":
		var raw string
		if raw, err = a.reveal(id); err == nil {
			fmt.Fprintln(stdout, raw)
		}
	}
	return cliErr(err)
}

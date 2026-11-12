package sqlstore

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"radarlive/internal/domain"
)

// sqlMethods are the database/sql calls whose query argument is checked, with the index of that argument.
var sqlMethods = map[string]int{
	"Exec": 0, "Query": 0, "QueryRow": 0, "Prepare": 0,
	"ExecContext": 1, "QueryContext": 1, "QueryRowContext": 1, "PrepareContext": 1,
}

// Fragments that are built once from fixed lists (not from input) and may appear in SQL positions.
var allowedIdents = map[string]string{
	"selectContacts": "joined from the fixed contactColumns list at init",
	"upsertContact":  "built by buildUpsert from the fixed column list at init",
}

// allowedSelectors are field reads allowed in SQL positions, with the reason.
var allowedSelectors = map[string]string{
	"table.name": "range variable over a literal list of two table names in prune.go",
}

type sqlSite struct {
	file string
	fn   *ast.FuncDecl
	call *ast.CallExpr
}

// parsePackage parses the non-test Go files of the package directory.
func parsePackage(t *testing.T) (*token.FileSet, map[string]*ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Clean(name), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = f
	}
	if len(files) < 5 {
		t.Fatalf("parsed only %d source files", len(files))
	}
	return fset, files
}

// sqlScanner decides whether an SQL expression is built only from constants and whitelisted fragments.
type sqlScanner struct {
	consts map[string]bool
	funcs  map[string]*ast.FuncDecl
	files  map[string]*ast.File
	// visiting breaks cycles such as q = q + "..."; an identifier under evaluation is safe if its other assignments are.
	visiting map[string]bool
}

func newSQLScanner(files map[string]*ast.File) *sqlScanner {
	s := &sqlScanner{consts: map[string]bool{}, funcs: map[string]*ast.FuncDecl{}, files: files, visiting: map[string]bool{}}
	for _, f := range files {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.GenDecl:
				if d.Tok == token.CONST {
					for _, sp := range d.Specs {
						for _, n := range sp.(*ast.ValueSpec).Names {
							s.consts[n.Name] = true
						}
					}
				}
			case *ast.FuncDecl:
				if d.Recv == nil {
					s.funcs[d.Name.Name] = d
				} else {
					s.funcs[d.Name.Name] = d // method names are unique enough in this package
				}
			}
		}
	}
	return s
}

// safe reports whether e is constant SQL text; why explains a rejection.
func (s *sqlScanner) safe(e ast.Expr, fn *ast.FuncDecl, depth int) (bool, string) {
	if depth > 6 {
		return false, "too deeply nested to prove"
	}
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind == token.STRING {
			return true, ""
		}
	case *ast.ParenExpr:
		return s.safe(e.X, fn, depth)
	case *ast.BinaryExpr:
		if e.Op == token.ADD {
			if ok, why := s.safe(e.X, fn, depth); !ok {
				return false, why
			}
			return s.safe(e.Y, fn, depth)
		}
	case *ast.SelectorExpr:
		if x, ok := e.X.(*ast.Ident); ok {
			if _, ok := allowedSelectors[x.Name+"."+e.Sel.Name]; ok {
				return true, ""
			}
		}
	case *ast.CallExpr:
		// f.where() joins the fixed clauses added through filter.add (checked separately).
		if sel, ok := e.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "where" && len(e.Args) == 0 {
			return true, ""
		}
		// fmt.Sprintf with only %d verbs and a literal format: integers can not carry SQL.
		if exprName(e.Fun) == "fmt.Sprintf" && len(e.Args) > 0 {
			if lit, ok := e.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING && intOnlyFormat.MatchString(lit.Value) {
				return true, ""
			}
		}
	case *ast.IndexExpr:
		if id, ok := e.X.(*ast.Ident); ok && id.Name == "migrations" {
			return true, "" // migrations is a package var of schema constants, append-only
		}
	case *ast.Ident:
		return s.safeIdent(e, fn, depth)
	}
	return false, fmt.Sprintf("%T is not constant SQL text", e)
}

func (s *sqlScanner) safeIdent(id *ast.Ident, fn *ast.FuncDecl, depth int) (bool, string) {
	if s.consts[id.Name] {
		return true, ""
	}
	if _, ok := allowedIdents[id.Name]; ok {
		return true, ""
	}
	if fn == nil {
		return false, "unknown identifier " + id.Name
	}
	// A parameter is safe when every caller passes constant SQL text.
	for i, p := range paramNames(fn) {
		if p == id.Name {
			return s.safeCallers(fn, i, depth)
		}
	}
	// A local is safe when every assignment to it is.
	key := fn.Name.Name + "." + id.Name
	if s.visiting[key] {
		return true, ""
	}
	s.visiting[key] = true
	defer delete(s.visiting, key)
	found := false
	var bad string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range as.Lhs {
			l, ok := lhs.(*ast.Ident)
			if !ok || l.Name != id.Name {
				continue
			}
			found = true
			rhs := as.Rhs
			if len(as.Lhs) == len(as.Rhs) {
				rhs = as.Rhs[i : i+1]
			} else {
				bad = "assigned from a multi-value expression"
				continue
			}
			if ok, why := s.safe(rhs[0], fn, depth+1); !ok && bad == "" {
				bad = why
			}
		}
		return true
	})
	switch {
	case !found:
		return false, "cannot trace identifier " + id.Name
	case bad != "":
		return false, bad
	}
	return true, ""
}

func paramNames(fn *ast.FuncDecl) []string {
	var names []string
	for _, f := range fn.Type.Params.List {
		for _, n := range f.Names {
			names = append(names, n.Name)
		}
	}
	return names
}

// safeCallers checks argument idx at every call of fn in the package. The receiver is not a parameter.
func (s *sqlScanner) safeCallers(fn *ast.FuncDecl, idx, depth int) (bool, string) {
	calls := 0
	ok, why := true, ""
	for _, f := range s.files {
		for _, d := range f.Decls {
			caller, isFn := d.(*ast.FuncDecl)
			if !isFn || caller.Body == nil {
				continue
			}
			ast.Inspect(caller.Body, func(n ast.Node) bool {
				c, isCall := n.(*ast.CallExpr)
				if !isCall || callee(c) != fn.Name.Name || idx >= len(c.Args) {
					return true
				}
				calls++
				if good, reason := s.safe(c.Args[idx], caller, depth+1); !good {
					ok, why = false, reason
				}
				return true
			})
		}
	}
	if calls == 0 {
		return false, "parameter of a function with no visible callers"
	}
	return ok, why
}

func callee(c *ast.CallExpr) string {
	switch f := c.Fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

func findSQLSites(files map[string]*ast.File) []sqlSite {
	var sites []sqlSite
	for name, f := range files {
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				c, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := c.Fun.(*ast.SelectorExpr); ok {
					if _, isSQL := sqlMethods[sel.Sel.Name]; isSQL {
						sites = append(sites, sqlSite{file: name, fn: fn, call: c})
					}
				}
				return true
			})
		}
	}
	return sites
}

// TestAllSQLIsParameterised scans every non-test source file: each database/sql call must take SQL
// text built only from string constants and whitelisted fixed fragments, every filter clause must be
// a literal, and fmt.Sprintf may only format integers (%d).
func TestAllSQLIsParameterised(t *testing.T) {
	fset, files := parsePackage(t)
	sc := newSQLScanner(files)
	sites := findSQLSites(files)
	if len(sites) < 15 {
		t.Fatalf("sqlScanner found only %d SQL call sites; the scan is broken", len(sites))
	}
	for _, s := range sites {
		sel := s.call.Fun.(*ast.SelectorExpr)
		idx := sqlMethods[sel.Sel.Name]
		if idx >= len(s.call.Args) {
			continue // not a database/sql call shape (e.g. a Query method with other arguments)
		}
		// Calls like st.getContact.QueryRowContext(ctx, args...) run a prepared statement: no SQL text.
		if _, isRecv := sel.X.(*ast.SelectorExpr); isRecv && strings.HasPrefix(exprName(sel.X), "st.") {
			continue
		}
		if ok, why := sc.safe(s.call.Args[idx], s.fn, 0); !ok {
			t.Errorf("%s: SQL passed to %s is not constant: %s", fset.Position(s.call.Pos()), sel.Sel.Name, why)
		}
	}
	checkFilterClauses(t, fset, files)
	checkSprintf(t, fset, files)
}

func exprName(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return exprName(e.X) + "." + e.Sel.Name
	}
	return ""
}

var intOnlyFormat = regexp.MustCompile(`^[^%]*(%d[^%]*)*$`)

func checkSprintf(t *testing.T, fset *token.FileSet, files map[string]*ast.File) {
	t.Helper()
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok || exprName(c.Fun) != "fmt.Sprintf" || len(c.Args) == 0 {
				return true
			}
			lit, ok := c.Args[0].(*ast.BasicLit)
			if !ok || !intOnlyFormat.MatchString(lit.Value) {
				t.Errorf("%s: fmt.Sprintf may only format integers with %%d", fset.Position(c.Pos()))
			}
			return true
		})
	}
}

func checkFilterClauses(t *testing.T, fset *token.FileSet, files map[string]*ast.File) {
	t.Helper()
	n := 0
	for _, f := range files {
		ast.Inspect(f, func(node ast.Node) bool {
			c, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := c.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "add" || exprName(sel.X) != "f" || len(c.Args) == 0 {
				return true
			}
			n++
			if lit, ok := c.Args[0].(*ast.BasicLit); !ok || lit.Kind != token.STRING {
				t.Errorf("%s: filter clause must be a string literal", fset.Position(c.Pos()))
			}
			return true
		})
	}
	if n < 4 {
		t.Fatalf("found only %d filter.add calls; the scan is broken", n)
	}
}

// TestScannerRejectsStringBuiltSQL proves the sqlScanner would catch the bugs it guards against.
func TestScannerRejectsStringBuiltSQL(t *testing.T) {
	src := `package x
import "fmt"
func a(db D, name string) { db.Exec("DELETE FROM t WHERE n = '" + name + "'") }
func b(db D, name string) { db.Query(fmt.Sprintf("SELECT * FROM t WHERE n = '%s'", name)) }
func c(db D, name string) { q := "SELECT " + name; db.QueryContext(ctx, q) }
func d(db D) { db.Exec("DELETE FROM t") }
func e(db D, q string) { db.Exec(q) }
func g(db D, name string) { db.Exec(` + "`SELECT 1 WHERE x = ?`" + `, name) }
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]*ast.File{"x.go": f}
	sc := newSQLScanner(files)
	want := map[string]bool{"a": false, "b": false, "c": false, "d": true, "e": false, "g": true}
	for _, s := range findSQLSites(files) {
		idx := sqlMethods[s.call.Fun.(*ast.SelectorExpr).Sel.Name]
		ok, _ := sc.safe(s.call.Args[idx], s.fn, 0)
		if ok != want[s.fn.Name.Name] {
			t.Errorf("%s: safe = %v, want %v", s.fn.Name.Name, ok, want[s.fn.Name.Name])
		}
	}
}

// Payloads that would break out of string-built SQL or abuse LIKE; all must be inert.
var injectionPayloads = []string{
	`' OR '1'='1`, `" OR ""="`, `'; DROP TABLE contacts;--`, `'); DELETE FROM contacts;--`,
	`1; SELECT * FROM meta`, `' UNION SELECT guid,guid,guid,guid FROM contacts--`, `\' OR 1=1 --`,
	`%`, `%%`, `_`, `___`, `\`, `\%`, `a%`, `%a`, `[a-z]`, `*`, `.*`, `' OR instr(search,'')>0 --`,
	"a\x00b", "a\nb", "'\x00'", `/*`, `*/`, `-- x`, `;`, `||`, `0x41`, `{{.}}`, `${x}`, `<script>`, `Rob'); --`,
	`ünï`, `名`, `?`, `?1`, `:name`, `@p`, `$1`, strings64("a"), strings64("%"),
}

func strings64(s string) string { return strings.Repeat(s, 64) }

func seedStore(t *testing.T) *Store {
	t.Helper()
	s, _ := tempStore(t, Options{})
	ctx := context.Background()
	for i, name := range []string{"Alice", "Bob", "Cara_x", "100%", "O'Neil"} {
		if _, err := s.ApplyPing(ctx, contactPing(fmt.Sprint("seed", i), i+1, name)); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func countContacts(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.writer.QueryRow(`SELECT count(*) FROM contacts`).Scan(&n); err != nil {
		t.Fatalf("contacts table damaged: %v", err)
	}
	return n
}

func TestSearchSQLiCorpus(t *testing.T) {
	s := seedStore(t)
	ctx := context.Background()
	before := countContacts(t, s)
	for _, p := range injectionPayloads {
		for _, g := range []domain.Group{domain.GroupAny, domain.GroupHostile} {
			res, err := s.Search(ctx, domain.SearchQuery{RS: domain.RulesetPvP, Q: p, Group: g, Limit: 500})
			if err != nil {
				t.Errorf("search %q: %v", p, err)
				continue
			}
			// only a contact whose name really contains the payload may match
			for _, c := range res.Contacts {
				if !c.MatchesQuery(p) {
					t.Errorf("search %q leaked %q", p, c.Name)
				}
			}
			if res.Total != len(res.Contacts) {
				t.Errorf("search %q: total %d != %d rows", p, res.Total, len(res.Contacts))
			}
		}
	}
	if got := countContacts(t, s); got != before {
		t.Fatalf("contacts changed: %d -> %d", before, got)
	}
}

func TestSearchWildcardsMatchOnlyLiterally(t *testing.T) {
	s := seedStore(t)
	ctx := context.Background()
	tests := map[string]int{"%": 1, "_": 1, "100%": 1, "'": 1, "a%": 0, "_ice": 0, "Al_ce": 0, "Alice": 1}
	for q, want := range tests {
		res, err := s.Search(ctx, domain.SearchQuery{RS: domain.RulesetPvP, Q: q})
		if err != nil {
			t.Fatal(err)
		}
		if res.Total != want {
			t.Errorf("q=%q total=%d, want %d", q, res.Total, want)
		}
	}
}

func TestSweepSQLiCorpus(t *testing.T) {
	s := seedStore(t)
	ctx := context.Background()
	before := countContacts(t, s)
	for _, p := range injectionPayloads {
		sectors := []domain.Sector{
			{RS: domain.RulesetPvP, Scope: domain.ScopeAll, Query: p},
			{RS: domain.RulesetPvP, Scope: domain.ScopePlayer, GUID: p, Trail: true},
			{RS: domain.RulesetPvP, Scope: domain.ScopeAll, Query: p, Trail: true, ActiveWithinMs: 1 << 40},
		}
		for _, sec := range sectors {
			// a guid with quotes is not a valid player guid for the API, but the store must still be safe
			if _, err := s.Sweep(ctx, sec); err != nil {
				t.Errorf("sweep %+v: %v", sec, err)
			}
		}
		if _, err := s.Trail(ctx, domain.RulesetPvP, p, nil); err != nil {
			t.Errorf("trail %q: %v", p, err)
		}
		if _, err := s.BountyPlayer(ctx, domain.RulesetPvP, p); err != nil && !isNotFound(err) {
			t.Errorf("bounty player %q: %v", p, err)
		}
	}
	if got := countContacts(t, s); got != before {
		t.Fatalf("contacts changed: %d -> %d", before, got)
	}
}

func isNotFound(err error) bool { return err != nil && strings.Contains(err.Error(), "not found") }

func FuzzSearchQuery(f *testing.F) {
	for _, p := range injectionPayloads {
		f.Add(p, 10, 0)
	}
	s, _ := tempStoreForFuzz(f)
	f.Fuzz(func(t *testing.T, q string, limit, offset int) {
		res, err := s.Search(context.Background(), domain.SearchQuery{RS: domain.RulesetPvP, Q: q, Limit: limit, Offset: offset})
		if err != nil {
			return // invalid paging is a clean error; a crash or panic is the failure
		}
		if res.Total < len(res.Contacts) {
			t.Fatalf("total %d < rows %d", res.Total, len(res.Contacts))
		}
	})
}

func tempStoreForFuzz(f *testing.F) (*Store, string) {
	path := filepath.Join(f.TempDir(), "radar.db")
	s, err := Open(path, Options{})
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { _ = s.Close() })
	for i, name := range []string{"Alice", "Bob", "100%", "O'Neil"} {
		if _, err := s.ApplyPing(context.Background(), contactPing(fmt.Sprint("seed", i), i+1, name)); err != nil {
			f.Fatal(err)
		}
	}
	return s, path
}

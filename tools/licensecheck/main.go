// Command licensecheck fails if any npm dependency in a package-lock.json uses a
// licence outside the allowlist (ADR-014). Shipped (prod) packages get the strict
// list; build-only (dev) packages may also use weak-copyleft build tooling licences.
// Go modules linked into Linx binaries are checked by their licence text (gomod.go).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
)

var (
	prodAllowed = set("MIT", "MIT-0", "ISC", "BSD-2-Clause", "BSD-3-Clause", "Apache-2.0",
		"0BSD", "BlueOak-1.0.0", "CC0-1.0", "Unlicense")
	devExtra = set("MPL-2.0", "CC-BY-4.0", "Python-2.0")
)

type lockfile struct {
	Packages map[string]struct {
		License string `json:"license"`
		Dev     bool   `json:"dev"`
		Link    bool   `json:"link"`
	} `json:"packages"`
}

func main() {
	path := flag.String("lock", "web/package-lock.json", "npm lockfile to check")
	flag.Parse()

	b, err := os.ReadFile(*path)
	if err != nil {
		fail(err)
	}
	var lf lockfile
	if err := json.Unmarshal(b, &lf); err != nil {
		fail(fmt.Errorf("%s: %w", *path, err))
	}

	var bad []string
	for p, pkg := range lf.Packages {
		if p == "" || pkg.Link {
			continue // root project or workspace link
		}
		if !allowed(pkg.License, pkg.Dev) {
			scope := "prod"
			if pkg.Dev {
				scope = "dev"
			}
			lic := pkg.License
			if lic == "" {
				lic = "UNKNOWN"
			}
			bad = append(bad, fmt.Sprintf("%s (%s): %s", strings.TrimPrefix(p[strings.LastIndex(p, "node_modules/")+1:], "node_modules/"), scope, lic))
		}
	}
	goMods, goBad, err := checkGo()
	if err != nil {
		fail(err)
	}
	bad = append(bad, goBad...)
	sort.Strings(bad)
	for _, l := range bad {
		fmt.Fprintln(os.Stderr, "licence not allowed:", l)
	}
	if len(bad) > 0 {
		fail(fmt.Errorf("%d package(s) with disallowed licences", len(bad)))
	}
	fmt.Printf("licences: ok (%d npm packages, %d Go modules)\n", len(lf.Packages)-1, goMods)
}

// allowed evaluates simple SPDX expressions: "A OR B" passes if any side passes,
// "A AND B" only if all sides pass.
func allowed(expr string, dev bool) bool {
	expr = strings.Trim(strings.TrimSpace(expr), "()")
	if expr == "" {
		return false
	}
	if parts := strings.Split(expr, " OR "); len(parts) > 1 {
		for _, p := range parts {
			if allowed(p, dev) {
				return true
			}
		}
		return false
	}
	if parts := strings.Split(expr, " AND "); len(parts) > 1 {
		for _, p := range parts {
			if !allowed(p, dev) {
				return false
			}
		}
		return true
	}
	return prodAllowed[expr] || (dev && devExtra[expr])
}

func set(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, i := range items {
		m[i] = true
	}
	return m
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "licensecheck:", err)
	os.Exit(1)
}

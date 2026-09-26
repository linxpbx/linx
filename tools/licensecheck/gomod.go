package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Go modules don't declare SPDX licences, so the licence file is classified by
// its text. Only modules linked into Linx binaries (not test-only ones) count.

var (
	// Each licence matches if all markers of any one alternative appear
	// (whitespace, including line breaks, is ignored when comparing).
	goAllowedMarkers = map[string][][]string{
		"Apache-2.0": {{"Apache License", "Version 2.0"}},
		"MIT":        {{"Permission is hereby granted, free of charge"}},
		"BSD":        {{"Redistribution and use in source and binary forms"}},
		// ISC as the OSI publishes it ("and/or") and its original wording.
		"ISC": {
			{"Permission to use, copy, modify, and/or distribute this software for any purpose"},
			{"Permission to use, copy, modify, and distribute this software for any purpose with or without fee is hereby granted"},
		},
	}
	goDeniedMarkers = []string{
		"GNU GENERAL PUBLIC LICENSE", "GNU LESSER GENERAL PUBLIC LICENSE", "GNU AFFERO GENERAL PUBLIC LICENSE",
		"Mozilla Public License", "Server Side Public License", "Business Source License",
	}
	// Lower-case too (github.com/josharian/native has "license"): Linux's disks
	// are case-sensitive, even where macOS's hide it.
	licenceFiles = []string{"LICENSE", "LICENSE.md", "LICENSE.txt", "LICENCE", "COPYING", "COPYING.md", "license", "license.md", "license.txt"}
)

// checkGo returns the number of modules checked and any problems.
func checkGo() (int, []string, error) {
	// For Linux, where every Linx binary runs: some modules (netlink, for
	// linx-wireguard) are only linked there.
	cmd := exec.Command("go", "list", "-deps",
		"-f", "{{with .Module}}{{if not .Main}}{{.Path}}@{{.Version}} {{.Dir}}{{end}}{{end}}", "./...")
	cmd.Env = append(os.Environ(), "GOOS=linux")
	out, err := cmd.Output()
	if err != nil {
		return 0, nil, fmt.Errorf("go list: %w", err)
	}
	mods := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if mod, dir, ok := strings.Cut(sc.Text(), " "); ok {
			mods[mod] = dir
		}
	}
	var bad []string
	for mod, dir := range mods {
		text := readLicence(dir)
		if lic := classifyLicence(text); lic == "" {
			bad = append(bad, mod+" (go): UNKNOWN or not allowed")
		}
	}
	return len(mods), bad, nil
}

func readLicence(dir string) string {
	var b strings.Builder
	// On a case-insensitive disk "license" and "LICENSE" are one file, read
	// twice: harmless for classifying it.
	for _, name := range licenceFiles {
		if data, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			b.Write(data)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// classifyLicence returns the allowed licences found in text, or "" if none
// are found or any copyleft/source-available licence text is present.
func classifyLicence(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	upper := strings.ToUpper(text)
	for _, m := range goDeniedMarkers {
		if strings.Contains(upper, strings.ToUpper(m)) {
			return ""
		}
	}
	var found []string
	for lic, alternatives := range goAllowedMarkers {
		for _, markers := range alternatives {
			all := true
			for _, m := range markers {
				all = all && strings.Contains(text, m)
			}
			if all {
				found = append(found, lic)
				break
			}
		}
	}
	return strings.Join(found, ",")
}

/*
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package coverage filters a Go cover profile and writes a text summary.
// It is a hack tool for unit-test artifacts, not a public library.
package coverage

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var (
	// ErrEmptyProfile is returned when a profile file has no content.
	ErrEmptyProfile = errors.New("coverage profile is empty")
	// ErrCorruptProfile is returned when a profile is not valid cover data.
	ErrCorruptProfile = errors.New("coverage profile is corrupt")
)

var (
	modeRE  = regexp.MustCompile(`^mode: (set|count|atomic)$`)
	blockRE = regexp.MustCompile(`^(.+):([0-9]+)\.([0-9]+),([0-9]+)\.([0-9]+) ([0-9]+) ([0-9]+)$`)
)

// Profile is a parsed Go coverage profile.
type Profile struct {
	Mode   string
	Blocks []Block
}

// Block is one coverage block from a profile line.
type Block struct {
	File      string
	StartLine int
	StartCol  int
	EndLine   int
	EndCol    int
	NumStmt   int
	Count     int
}

// Parse reads a Go cover profile. A zero-length or whitespace-only input
// returns ErrEmptyProfile. A malformed profile returns ErrCorruptProfile.
// A profile with a mode line and no blocks is valid.
func Parse(r io.Reader) (Profile, error) {
	scanner := bufio.NewScanner(r)
	var (
		mode    string
		blocks  []Block
		lineNo  int
		sawText bool
	)
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		sawText = true
		if mode == "" {
			match := modeRE.FindStringSubmatch(line)
			if match == nil {
				return Profile{}, fmt.Errorf("%w: line %d: expected mode line", ErrCorruptProfile, lineNo)
			}
			mode = match[1]
			continue
		}
		block, err := parseBlock(line)
		if err != nil {
			return Profile{}, fmt.Errorf("%w: line %d: %w", ErrCorruptProfile, lineNo, err)
		}
		blocks = append(blocks, block)
	}
	if err := scanner.Err(); err != nil {
		return Profile{}, fmt.Errorf("%w: %w", ErrCorruptProfile, err)
	}
	if !sawText {
		return Profile{}, ErrEmptyProfile
	}
	return Profile{Mode: mode, Blocks: blocks}, nil
}

func parseBlock(line string) (Block, error) {
	match := blockRE.FindStringSubmatch(line)
	if match == nil {
		return Block{}, errors.New("invalid block")
	}
	nums := make([]int, 6)
	for i := range nums {
		n, err := strconv.Atoi(match[i+2])
		if err != nil {
			return Block{}, fmt.Errorf("invalid block number: %w", err)
		}
		nums[i] = n
	}
	return Block{
		File:      match[1],
		StartLine: nums[0],
		StartCol:  nums[1],
		EndLine:   nums[2],
		EndCol:    nums[3],
		NumStmt:   nums[4],
		Count:     nums[5],
	}, nil
}

// Format renders p in Go cover profile format.
func (p Profile) Format() string {
	var b strings.Builder
	fmt.Fprintf(&b, "mode: %s\n", p.Mode)
	for _, block := range p.Blocks {
		fmt.Fprintf(&b, "%s:%d.%d,%d.%d %d %d\n",
			block.File, block.StartLine, block.StartCol, block.EndLine, block.EndCol, block.NumStmt, block.Count)
	}
	return b.String()
}

// Filter drops blocks whose file path is generated or otherwise not product
// code. Product packages such as pkg/ are kept.
//
// Dropped paths, matched as directory segments or file names:
//   - vendor/
//   - zz_generated.* (deepcopy, conversion, defaults, openapi)
//   - *.pb.go
//   - client-go/ (generated clients, informers, applyconfiguration)
func Filter(in Profile) Profile {
	out := Profile{Mode: in.Mode, Blocks: make([]Block, 0, len(in.Blocks))}
	for _, block := range in.Blocks {
		if excludedPath(block.File) {
			continue
		}
		out.Blocks = append(out.Blocks, block)
	}
	return out
}

func excludedPath(path string) bool {
	path = strings.ReplaceAll(path, `\`, "/")
	if hasDirSegment(path, "vendor") || hasDirSegment(path, "client-go") {
		return true
	}
	name := path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		name = path[i+1:]
	}
	return strings.Contains(name, "zz_generated.") || strings.HasSuffix(name, ".pb.go")
}

func hasDirSegment(path, segment string) bool {
	return path == segment ||
		strings.HasPrefix(path, segment+"/") ||
		strings.Contains(path, "/"+segment+"/")
}

// blockKey is one cover block. make test -coverpkg repeats the same
// file:start,end block once per test package.
type blockKey struct {
	file      string
	startLine int
	startCol  int
	endLine   int
	endCol    int
	numStmt   int
}

// mergeBlocks collapses duplicate blocks so statement totals count each
// block once. Mode set is covered when any copy has a positive count.
// Modes count and atomic add the counts; the block is covered when the
// sum is positive.
func mergeBlocks(p Profile) Profile {
	if len(p.Blocks) < 2 {
		return p
	}
	out := Profile{Mode: p.Mode, Blocks: make([]Block, 0, len(p.Blocks))}
	index := make(map[blockKey]int, len(p.Blocks))
	for _, block := range p.Blocks {
		key := blockKey{
			file:      block.File,
			startLine: block.StartLine,
			startCol:  block.StartCol,
			endLine:   block.EndLine,
			endCol:    block.EndCol,
			numStmt:   block.NumStmt,
		}
		i, ok := index[key]
		if !ok {
			index[key] = len(out.Blocks)
			out.Blocks = append(out.Blocks, block)
			continue
		}
		out.Blocks[i].Count = mergeCount(p.Mode, out.Blocks[i].Count, block.Count)
	}
	return out
}

func mergeCount(mode string, a, b int) int {
	if mode == "set" {
		if a > 0 || b > 0 {
			return 1
		}
		return 0
	}
	// count and atomic: add execution counts.
	return a + b
}

// Summary is a short report of statement coverage in p.
// Duplicate blocks are merged first. The last line is an overall percentage
// a human can read in a log. A profile with no statements reports 0.0% and
// "no statements".
func Summary(p Profile) string {
	p = mergeBlocks(p)
	type stat struct {
		covered int
		total   int
	}
	stats := make(map[string]*stat)
	var files []string
	var covered, total int
	for _, block := range p.Blocks {
		s, ok := stats[block.File]
		if !ok {
			s = &stat{}
			stats[block.File] = s
			files = append(files, block.File)
		}
		s.total += block.NumStmt
		total += block.NumStmt
		if block.Count > 0 {
			s.covered += block.NumStmt
			covered += block.NumStmt
		}
	}
	slices.Sort(files)
	var b strings.Builder
	for _, file := range files {
		s := stats[file]
		fmt.Fprintf(&b, "%s: %.1f%% of statements (%d/%d)\n", file, percent(s.covered, s.total), s.covered, s.total)
	}
	if total == 0 {
		b.WriteString("total: (statements) 0.0% (no statements)\n")
		return b.String()
	}
	fmt.Fprintf(&b, "total: (statements) %.1f%%\n", percent(covered, total))
	return b.String()
}

func percent(covered, total int) float64 {
	if total == 0 {
		return 0
	}
	return 100 * float64(covered) / float64(total)
}

// OutputPaths returns the filtered profile and text summary paths next to
// profilePath. A shard suffix on the input is preserved:
// cover-shard-0.out becomes cover-shard-0.filtered.out.
func OutputPaths(profilePath string) (filtered, summary string) {
	base := strings.TrimSuffix(profilePath, ".out")
	return base + ".filtered.out", base + ".filtered.txt"
}

// Report filters profilePath, merges duplicate blocks, and writes the filtered
// profile and text summary beside it. The summary is also written to stdout.
//
// A missing or empty profile returns an error. A profile that becomes empty
// after filtering is written with a 0% summary and a nil error. There is no
// coverage threshold.
func Report(profilePath string, stdout io.Writer) error {
	if profilePath == "" {
		return errors.New("coverage profile path is empty")
	}
	data, err := os.ReadFile(profilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("coverage profile %q is missing: %w", profilePath, err)
		}
		return fmt.Errorf("read coverage profile %q: %w", profilePath, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return fmt.Errorf("coverage profile %q is empty: %w", profilePath, ErrEmptyProfile)
	}
	parsed, err := Parse(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("parse coverage profile %q: %w", profilePath, err)
	}
	filtered := mergeBlocks(Filter(parsed))
	summary := Summary(filtered)
	filteredPath, summaryPath := OutputPaths(profilePath)
	if err := os.WriteFile(filteredPath, []byte(filtered.Format()), 0o644); err != nil {
		return fmt.Errorf("write filtered coverage profile %q: %w", filteredPath, err)
	}
	if err := os.WriteFile(summaryPath, []byte(summary), 0o644); err != nil {
		return fmt.Errorf("write coverage summary %q: %w", summaryPath, err)
	}
	if _, err := io.WriteString(stdout, summary); err != nil {
		return fmt.Errorf("write coverage summary: %w", err)
	}
	return nil
}

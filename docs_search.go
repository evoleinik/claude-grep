package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// DocMatch is a hit from the curated-docs lane.
type DocMatch struct {
	File       string
	Heading    string
	Text       string
	Similarity float32
}

// docsCap bounds the docs block: surface the few canonical sections, not a dump.
func docsCap(maxResults int) int {
	if maxResults < 5 {
		return maxResults
	}
	return 5
}

// regexDocsSearch scans live .md files, attributing each hit to its heading.
func regexDocsSearch(pattern string, dirs []string, cap int) ([]DocMatch, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	var out []DocMatch
	for _, dir := range dirs {
		filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			for _, c := range chunkMarkdown(data) {
				if re.MatchString(c.Heading) || re.MatchString(c.Body) {
					out = append(out, DocMatch{File: path, Heading: c.Heading, Text: c.Body})
					if len(out) >= cap {
						return filepath.SkipAll
					}
				}
			}
			return nil
		})
		if len(out) >= cap {
			break
		}
	}
	return out, nil
}

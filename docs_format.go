package main

// docsToJSON converts doc hits into JSONMatch entries tagged source="docs",
// so they live in the same output array as session matches.
func docsToJSON(docs []DocMatch) []JSONMatch {
	var out []JSONMatch
	for _, d := range docs {
		out = append(out, JSONMatch{
			Source: "docs", File: d.File, Heading: d.Heading,
			Text: d.Text, Similarity: d.Similarity,
		})
	}
	return out
}

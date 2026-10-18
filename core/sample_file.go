package core

import (
	"path"
	"regexp"
	"strings"
)

// Match explicit sample prefixes/suffixes, not title words such as
// "The.Sample.Movie.2024" or "Sampled.2024".
var sampleFilenamePattern = regexp.MustCompile(`(?i)(?:^sample(?:[-_]|$)|[ ._-]sample$)`)

// IsSampleFile checks both provider identifiers. Samples remain video files and
// stay in stored evidence; this predicate only controls display and selection.
func IsSampleFile(name, filePath string) bool {
	for _, value := range []string{name, filePath} {
		value = strings.ReplaceAll(value, `\`, "/")
		parts := strings.Split(value, "/")
		for _, folder := range parts[:len(parts)-1] {
			folder = strings.TrimSpace(folder)
			if strings.EqualFold(folder, "sample") || strings.EqualFold(folder, "samples") {
				return true
			}
		}
		filename := parts[len(parts)-1]
		if HasVideoExtension(filename) {
			filename = strings.TrimSuffix(filename, path.Ext(filename))
		}
		if sampleFilenamePattern.MatchString(filename) {
			return true
		}
	}
	return false
}

package stremio_shared

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/MunifTanjim/stremthru/internal/config"
	"github.com/MunifTanjim/stremthru/internal/imdb_title"
)

// FileSeasonContext protects known title text before inferring seasons. It is
// built once per selection pass; parsing files performs no metadata lookups.
type FileSeasonContext struct {
	titlePatterns []*regexp.Regexp
	TorrentTitle  string
}

func NewFileSeasonContext(titles ...string) FileSeasonContext {
	c := FileSeasonContext{}
	for _, title := range titles {
		words := strings.FieldsFunc(title, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
		if len(words) == 0 {
			continue
		}
		for i := range words {
			words[i] = regexp.QuoteMeta(words[i])
		}
		c.titlePatterns = append(c.titlePatterns, regexp.MustCompile(`(?i)`+strings.Join(words, `[^\p{L}\p{N}]*`)))
	}
	return c
}

// LoadFileSeasonContext reads local IMDb data only when enabled. An unavailable
// title preserves any titles already verified by the caller. This never
// contacts a metadata API.
func LoadFileSeasonContext(sid string, knownTitles ...string) FileSeasonContext {
	c := NewFileSeasonContext(knownTitles...)
	parts := strings.SplitN(sid, ":", 3)
	if len(parts) != 3 || !strings.HasPrefix(sid, "tt") || !config.Feature.HasIMDBTitle() {
		return c
	}
	title, err := imdb_title.Get(parts[0])
	if err != nil {
		log.Warn("failed to load title for file season matching", "sid", sid, "error", err)
		return c
	}
	if title != nil {
		c = NewFileSeasonContext(append([]string{title.Title, title.OrigTitle}, knownTitles...)...)
	}
	return c
}

func (c FileSeasonContext) withoutTitle(value string) string {
	// Find all spans before masking: a shorter alias must not hide a longer one.
	var spans [][2]int
	for _, pattern := range c.titlePatterns {
		for _, span := range pattern.FindAllStringIndex(value, -1) {
			before, after := rune(' '), rune(' ')
			if span[0] > 0 {
				before, _ = utf8.DecodeLastRuneInString(value[:span[0]])
			}
			if span[1] < len(value) {
				after, _ = utf8.DecodeRuneInString(value[span[1]:])
			}
			if unicode.IsLetter(before) || unicode.IsNumber(before) || unicode.IsLetter(after) || unicode.IsNumber(after) {
				continue
			}
			spans = append(spans, [2]int{span[0], span[1]})
		}
	}
	if len(spans) == 0 {
		return value
	}
	masked := []byte(value)
	for _, span := range spans {
		for i := span[0]; i < span[1]; i++ {
			masked[i] = ' '
		}
	}
	return string(masked)
}

func seasonContext(contexts []FileSeasonContext) FileSeasonContext {
	if len(contexts) > 0 {
		return contexts[0]
	}
	return FileSeasonContext{}
}

func (c FileSeasonContext) ParseName(name string, aggressive bool) seasonEpisodeData {
	return ParseSeasonEpisodeFromName(c.withoutTitle(name), aggressive)
}

// MatchesSeason requires positive season evidence. Filename seasons win; a
// torrent title is a fallback only when folders supply no season at all.
func (c FileSeasonContext) MatchesSeason(name, filePath string, season int) bool {
	if data := c.ParseName(name, false); data.Season != -1 {
		return data.Season == season
	}
	return FilePathMatchesSeason(filePath, season, c)
}

// WithTorrentTitle adds release-level fallback evidence without mutating the
// reusable title context shared by other torrents in the request.
func (c FileSeasonContext) WithTorrentTitle(title string) FileSeasonContext {
	c.TorrentTitle = title
	return c
}

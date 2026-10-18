package stremio_shared

import (
	"cmp"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Folder evidence requires explicit season labels; bare 16x9 can be an aspect ratio.
const folderSeasonLabel = `(?:seasons?|saisons?|seizoen(?:en)?|sezon(?:ul|y|so)?|sez[oó]n[ay]|stagion[ei]|temporadas?|staffeln?|s[äa]song(?:er)?|sesong(?:er)?|s[æa]son(?:er)?|kausi|kaudet|[ée]vad(?:ok)?|сезон[ыи]?|シーズン|시즌)`

// Reverse labels must be singular: "13.Seasons" describes a count, not season 13.
const folderSeasonOrdinalLabel = `(?:season|saison|seizoen|sezon(?:ul)?|sez[oó]na|stagione|temporada|staffel|s[äa]song|sesong|s[æa]son|kausi|[ée]vad|сезон)`
const folderSeasonNumbers = `\d+(?:(?:[ ._]*(?:[-–:,&+~〜～]|to|thru|and)[ ._]*(?:[st]|` + folderSeasonLabel + `)?[ ._:#№-]*\d+)|(?:[ ._]+(?:[st]|` + folderSeasonLabel + `)[ ._:#№-]*\d+)|(?:[ ._]+\d{1,3}))*`

// Full-word labels use short season numbers rather than four-digit years.
var folderSeasonWordNumbers = strings.ReplaceAll(folderSeasonNumbers, `\d+`, `\d{1,3}`)

// All folders contribute evidence. There is no "strong folder wins" shortcut:
// known title spans are removed before this parser sees the remaining text.
var folderSeasonPattern = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])(?:[st](` + folderSeasonNumbers + `)(?:\b|e\d)|` +
	folderSeasonLabel + `[ ._:#№-]*(` + folderSeasonWordNumbers + `)(?:\b|e\d)|` +
	`(\d{1,3})(?:(?:st|nd|rd|th)[ ._:-]*season|(?:[°ºª]|-?й)?[ ._:-]*(?:сезон|temporada)|[ ._]*\.[ ._]*` + folderSeasonOrdinalLabel + `)(?:$|[^\p{L}\p{N}]))|` +
	`(第?[ ._]*\d+[ ._]*(?:季|期)[ ._]*(?:[-–:~〜～]|至|から)[ ._]*第?[ ._]*\d+[ ._]*(?:季|期))|` +
	`第?[ ._]*(` + folderSeasonWordNumbers + `)[ ._]*(?:季|期)|(?:シーズン|시즌)[ ._:#№-]*(` + folderSeasonWordNumbers + `)(?:$|[^\p{L}\p{N}])|^[st][ ._:#№-]*(` + folderSeasonNumbers + `)$`)

// Embedded full-word expressions may be followed by recognizable release tags.
// Arbitrary title words after a number do not establish season evidence.
var folderSeasonSuffixPattern = regexp.MustCompile(`(?i)^(?:[ ._\[\](){}:+-]|\d{3,4}[pi]\b|(?:web[ ._-]?(?:dl|rip)|bluray|b[dr]rip|hdtv|dvdrip|remux|uhd|amzn|nf|dsnp|hmax|atvp|hulu|multi|dual|audio|proper|repack|internal|extended|uncut|hdr10\+?|hdr|sdr|dv|hevc|avc|aac|ac3|eac3|ddp?|dts|truehd|atmos|flac|x26[45]|h[ ._-]?26[45]|8bit|10bit|12bit)\b|(?:aac|ac3|eac3|ddp?|dts|truehd)(?:[ ._-]?\d(?:[ .]\d)?)?\b|\d(?:[ .]\d){1,2})*$`)
var folderSeasonPluralPattern = regexp.MustCompile(`(?i)(?:seasons|saisons|seizoenen|sezony|sez[oó]ny|stagioni|temporadas|staffeln|s[äa]songer|sesonger|s[æa]soner|kaudet|[ée]vadok|сезон[ыи])[ ._:#№-]*$`)
var folderSeasonReleaseGroupPattern = regexp.MustCompile(`^(.*)-[\p{L}\p{N}]+$`)

func validFolderSeasonSuffix(suffix string) bool {
	if folderSeasonSuffixPattern.MatchString(suffix) {
		return true
	}
	if group := folderSeasonReleaseGroupPattern.FindStringSubmatch(suffix); len(group) > 0 && strings.Trim(group[1], " ._[](){}:+-") != "" {
		return folderSeasonSuffixPattern.MatchString(group[1])
	}
	return false
}

var folderSeasonNumberPattern = regexp.MustCompile(`\d+`)
var folderSeasonChannelPattern = regexp.MustCompile(`[ _]+\d(?:\.\d){1,2}(?:$|[ _])`)

// Only the separator immediately after the previous number determines a range.
// A CJK season suffix may precede it; punctuation in the next label does not.
var folderSeasonRangePattern = regexp.MustCompile(`(?i)^[ ._]*(?:[季期][ ._]*)?(?:[-–:~〜～]|to|thru|至|から)`)

type fileSeasonRange struct{ first, last int }

// Merge overlaps before intersecting folders. Keeping a canonical union prevents
// repeated pack folders from multiplying duplicate ranges at each level.
func mergeFileSeasonRanges(ranges []fileSeasonRange) []fileSeasonRange {
	slices.SortFunc(ranges, func(a, b fileSeasonRange) int {
		if order := cmp.Compare(a.first, b.first); order != 0 {
			return order
		}
		return cmp.Compare(a.last, b.last)
	})
	merged := ranges[:0]
	for _, span := range ranges {
		if len(merged) != 0 && span.first <= merged[len(merged)-1].last {
			merged[len(merged)-1].last = max(merged[len(merged)-1].last, span.last)
		} else {
			merged = append(merged, span)
		}
	}
	return merged
}

// filePathSeasons intersects the evidence from each folder. Nil means no evidence;
// an empty non-nil slice means the folders contradict one another.
func filePathSeasons(filePath string, contexts ...FileSeasonContext) []fileSeasonRange {
	context := seasonContext(contexts)
	var seasons []fileSeasonRange
	for _, folder := range strings.Split(path.Dir(filePath), "/") {
		var found []fileSeasonRange
		folder = context.withoutTitle(folder)
		for _, marker := range folderSeasonPattern.FindAllStringSubmatchIndex(folder, -1) {
			expression := ""
			expressionStart, markerEnd := -1, marker[1]
			group := -1
			for i := 2; i < len(marker); i += 2 {
				if marker[i] != -1 {
					expression = folder[marker[i]:marker[i+1]]
					expressionStart = marker[i]
					group = i / 2
					break
				}
			}
			numbers := folderSeasonNumberPattern.FindAllStringIndex(expression, -1)
			// A bare channel layout after a season is release metadata, not another
			// member of a whitespace-separated season list ("Season 2 5.1").
			if channels := folderSeasonChannelPattern.FindStringIndex(expression); channels != nil {
				markerEnd = expressionStart + channels[0]
				expression = expression[:channels[0]]
				numbers = folderSeasonNumberPattern.FindAllStringIndex(expression, -1)
			}
			embedded := strings.Trim(folder[:marker[0]], " ._:[(){-") != ""
			if (group == 2 || group == 3) && embedded && !validFolderSeasonSuffix(folder[markerEnd:]) {
				continue
			}
			if group == 2 && len(numbers) == 1 && embedded && folderSeasonPluralPattern.MatchString(folder[marker[0]:expressionStart]) {
				continue
			}
			spans := []fileSeasonRange{}
			previous, previousEnd, valid := -1, 0, true
			for _, number := range numbers {
				n, err := strconv.Atoi(expression[number[0]:number[1]])
				if err != nil {
					valid = false
					break
				}
				span := fileSeasonRange{n, n}
				if previous != -1 && folderSeasonRangePattern.MatchString(expression[previousEnd:number[0]]) {
					span = fileSeasonRange{min(previous, n), max(previous, n)}
				}
				spans = append(spans, span)
				previous, previousEnd = n, number[1]
			}
			if valid {
				found = append(found, spans...)
			}
		}
		if len(found) == 0 {
			continue
		}
		found = mergeFileSeasonRanges(found)
		if seasons == nil {
			seasons = found
			continue
		}
		// Both unions are sorted and disjoint, so intersect them in one pass.
		intersection := []fileSeasonRange{}
		for i, j := 0, 0; i < len(seasons) && j < len(found); {
			a, b := seasons[i], found[j]
			first, last := max(a.first, b.first), min(a.last, b.last)
			if first <= last {
				intersection = append(intersection, fileSeasonRange{first, last})
			}
			if a.last < b.last {
				i++
			} else {
				j++
			}
		}
		seasons = mergeFileSeasonRanges(intersection)
		if len(seasons) == 0 {
			return seasons
		}
	}
	return seasons
}

// FilePathMatchesSeason requires positive, consistent season evidence. A
// single-season torrent title may supply evidence when folders are unknown.
func FilePathMatchesSeason(filePath string, season int, contexts ...FileSeasonContext) bool {
	context := seasonContext(contexts)
	seasons := filePathSeasons(filePath, context)
	if seasons == nil && context.TorrentTitle != "" {
		// Parse the title as a folder so the same label and title-exclusion rules apply.
		seasons = filePathSeasons("/"+context.TorrentTitle+"/file", context)
		if len(seasons) != 1 || seasons[0].first != seasons[0].last {
			return false
		}
	}
	for _, span := range seasons {
		if season >= span.first && season <= span.last {
			return true
		}
	}
	return false
}

// ResolveFileSeason preserves filename precedence, then uses folder evidence,
// then the existing title fallback. Ambiguous or conflicting folders stay unknown.
func ResolveFileSeason(filenameSeasons []int, filePath string, fallback int, contexts ...FileSeasonContext) int {
	if len(filenameSeasons) > 0 {
		return filenameSeasons[0]
	}
	seasons := filePathSeasons(filePath, contexts...)
	if seasons == nil {
		return fallback
	}
	for _, span := range seasons {
		if span.first <= fallback && fallback <= span.last {
			return fallback
		}
	}
	season := -1
	for _, span := range seasons {
		if span.first != span.last || (season != -1 && season != span.first) {
			return -1
		}
		season = span.first
	}
	return season
}

package stremio_shared

import (
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/MunifTanjim/go-ptt"
	"github.com/MunifTanjim/stremthru/internal/anidb"
	"github.com/MunifTanjim/stremthru/internal/torrent_info"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/MunifTanjim/stremthru/internal/util"
	"github.com/MunifTanjim/stremthru/store"
)

func MatchFileByIdx(files []store.File, idx int, storeCode store.StoreCode) store.File {
	if idx == -1 || storeCode != store.StoreCodeRealDebrid {
		return nil
	}
	for _, f := range files {
		if f.GetIdx() == idx {
			return f
		}
	}
	return nil
}

func MatchFileByLargestSize(files []store.File) (file store.File) {
	for _, f := range files {
		if file == nil || file.GetSize() < f.GetSize() {
			file = f
		}
	}
	return file
}

// MatchFileByLargestSizeForStremId preserves the fallback for movies and anime,
// but an IMDb series fallback requires positive season evidence.
func MatchFileByLargestSizeForStremId(files []store.File, sid string, contexts ...FileSeasonContext) (file store.File) {
	for _, f := range files {
		if (file == nil || file.GetSize() < f.GetSize()) && FilterFileByStremSeason(f, sid, contexts...) != nil {
			file = f
		}
	}
	return file
}

// FilterFileByStremSeason guards inferred playback fallbacks. Explicit SID/ASID
// mappings bypass it; movies and anime keep their existing matching rules.
func FilterFileByStremSeason(file store.File, sid string, contexts ...FileSeasonContext) store.File {
	parts := strings.SplitN(sid, ":", 3)
	if file == nil || !strings.HasPrefix(sid, "tt") || len(parts) < 2 {
		return file
	}
	season, err := strconv.Atoi(parts[1])
	if err != nil {
		return file
	}
	context := seasonContext(contexts)
	data := context.ParseName(file.GetName(), false)
	reason := ""
	if data.Season != -1 {
		if data.Season != season {
			reason = "filename season mismatch"
		}
	} else if !FilePathMatchesSeason(file.GetPath(), season, context) {
		reason = "missing or conflicting season evidence"
	}
	if reason != "" {
		log.Trace("rejecting playback fallback", "sid", sid, "filename", file.GetName(), "path", file.GetPath(), "expected_season", season, "file_season", data.Season, "reason", reason)
		return nil
	}
	return file
}

// LookupFileByName returns a unique basename match and whether the name is
// ambiguous, so callers need only one traversal of the provider's file list.
func LookupFileByName(files []store.File, name string) (store.File, bool) {
	if name == "" {
		return nil, false
	}
	var match store.File
	for _, f := range files {
		if f.GetName() == name {
			if match != nil {
				return nil, true
			}
			match = f
		}
	}
	return match, false
}

func MatchFileByName(files []store.File, name string) store.File {
	file, _ := LookupFileByName(files, name)
	return file
}

func MatchFileByPath(files []store.File, filePath string) store.File {
	if filePath == "" {
		return nil
	}
	wanted := path.Clean("/" + filePath)
	for _, f := range files {
		if f.GetPath() != "" && path.Clean("/"+f.GetPath()) == wanted {
			return f
		}
	}
	return nil
}

func MatchFileByPattern(files []store.File, pattern *regexp.Regexp) store.File {
	if pattern == nil {
		return nil
	}
	for _, f := range files {
		if pattern.MatchString(f.GetName()) {
			return f
		}
	}
	return nil
}

// MatchFileByPatternForStremId searches all pattern matches for an eligible
// season instead of rejecting only the first match after selection.
func MatchFileByPatternForStremId(files []store.File, pattern *regexp.Regexp, sid string, contexts ...FileSeasonContext) store.File {
	if pattern == nil {
		return nil
	}
	for _, f := range files {
		if pattern.MatchString(f.GetName()) && FilterFileByStremSeason(f, sid, contexts...) != nil {
			return f
		}
	}
	return nil
}

var parse_season_episode = ptt.GetPartialParser([]string{"releaseTypes", "seasons", "episodes"})

var digits_regex = regexp.MustCompile(`\b(\d+)\b`)

type seasonEpisodeData struct {
	Season      int
	Episode     int
	ReleaseType string
}

func ParseSeasonEpisodeFromName(title string, extractDigitsAsEpisodeAgressively bool) seasonEpisodeData {
	data := seasonEpisodeData{-1, -1, ""}
	r := parse_season_episode(title)
	if err := r.Error(); err != nil {
		log.Error("failed to parse season episode", "title", title, "error", err)
		return data
	}
	if len(r.Seasons) > 0 {
		data.Season = r.Seasons[0]
	}
	if len(r.Episodes) > 0 {
		data.Episode = r.Episodes[0]
	}
	if extractDigitsAsEpisodeAgressively && data.Season == -1 && data.Episode == -1 {
		matches := digits_regex.FindAllString(title, 2)
		if len(matches) == 1 {
			data.Episode, _ = strconv.Atoi(matches[0])
		}
	}
	if len(r.ReleaseTypes) > 0 {
		data.ReleaseType = r.ReleaseTypes[0]
	}
	return data
}

func matchFileByIMDBStremId(files []store.File, sid string, contexts ...FileSeasonContext) store.File {
	parts := strings.SplitN(sid, ":", 3)
	if len(parts) != 3 {
		return nil
	}
	expectedSeason, err := strconv.Atoi(parts[1])
	if err != nil {
		log.Warn("failed to parse season from imdb strem id", "error", err, "sid", sid)
		return nil
	}
	expectedEpisode := -1
	if len(parts) == 3 {
		expectedEpisode, err = strconv.Atoi(parts[2])
		if err != nil {
			log.Warn("failed to parse episode from imdb strem id", "error", err, "sid", sid)
			return nil
		}
	}
	context := seasonContext(contexts)
	var candidate store.File
	ambiguous := false
	for _, f := range files {
		d := context.ParseName(f.GetName(), false)
		if d.Episode != expectedEpisode {
			continue
		}
		if d.Season != -1 {
			if d.Season == expectedSeason {
				return f
			}
			continue
		}
		if !FilePathMatchesSeason(f.GetPath(), expectedSeason, context) {
			continue
		}
		if candidate != nil {
			ambiguous = true
		}
		candidate = f
	}
	if ambiguous {
		return nil
	}
	return candidate
}

func MatchFileByStremId(folderName string, files []store.File, sid string, torzHash string, storeCode store.StoreCode, contexts ...FileSeasonContext) store.File {
	return MatchFileByStremIdWithPath(folderName, files, sid, torzHash, storeCode, "", contexts...)
}

// MatchFileByStremIdWithPath preserves a previously selected file's identity
// across providers, whose file indexes can differ. Stored mappings still win.
func MatchFileByStremIdWithPath(folderName string, files []store.File, sid string, torzHash string, storeCode store.StoreCode, selectedPath string, contexts ...FileSeasonContext) store.File {
	if torzHash != "" {
		if f, err := torrent_stream.GetFile(torzHash, sid); err != nil {
			log.Error("failed to get file by strem id", "hash", torzHash, "sid", sid, "error", err)
		} else if f != nil {
			if file := MatchFileByPath(files, f.Path); file != nil {
				log.Debug("matched file by strem id - path", "hash", torzHash, "sid", sid, "path", file.GetPath(), "fileidx", file.GetIdx(), "store.name", storeCode.Name())
				return file
			}
			if file := MatchFileByIdx(files, f.Idx, storeCode); file != nil {
				log.Debug("matched file by strem id - fileidx", "hash", torzHash, "sid", sid, "filename", file.GetName(), "fileidx", file.GetIdx(), "store.name", storeCode.Name())
				return file
			}
			if file := MatchFileByName(files, f.Name); file != nil {
				log.Debug("matched file by strem id - filename", "hash", torzHash, "sid", sid, "filename", file.GetName(), "fileidx", file.GetIdx(), "store.name", storeCode.Name())
				return file
			}
		}
	}

	context := seasonContext(contexts).WithTorrentTitle(folderName)
	if file := FilterFileByStremSeason(MatchFileByPath(files, selectedPath), sid, context); file != nil {
		log.Debug("matched file using selected path", "hash", torzHash, "sid", sid, "path", file.GetPath(), "fileidx", file.GetIdx(), "store.name", storeCode.Name())
		return file
	}

	nsid, err := torrent_stream.NormalizeStreamId(sid)
	if err != nil {
		log.Error("failed to normalize strem id", "error", err, "sid", sid)
		return nil
	}

	if nsid.IsAnime {
		anidbId, season, episode := nsid.Id, nsid.Season, nsid.Episode

		expectedEpisode := util.SafeParseInt(episode, -1)
		expectedSeason := util.SafeParseInt(season, -1)

		filesForSeason := []store.File{}
		dataByName := map[string]seasonEpisodeData{}

		minEpisode := 99999
		for _, f := range files {
			d := ParseSeasonEpisodeFromName(f.GetName(), true)
			dataByName[f.GetName()] = d
			if d.ReleaseType == "" && (d.Episode != -1) && ((d.Season == -1 && expectedSeason == 1) || d.Season == expectedSeason) {
				filesForSeason = append(filesForSeason, f)
				if d.Episode < minEpisode {
					minEpisode = d.Episode
				}
			}
		}

		if len(filesForSeason) == 0 {
			tvdbMaps, err := anidb.GetTVDBEpisodeMaps(anidbId, false)
			if err != nil {
				log.Error("failed to get tvdb episode maps for anidb id", "error", err, "anidb_id", anidbId)
				return nil
			}

			absMap := tvdbMaps.GetAbsoluteOrderSeasonMap()
			if absMap != nil {
				expectedEpisode = expectedEpisode + absMap.Offset
				for _, f := range files {
					d := dataByName[f.GetName()]
					if d.Episode == expectedEpisode {
						return f
					}
				}
			}
		} else {
			episodes := []int{}
			if torzHash != "" {
				tInfo, err := torrent_info.GetByHash(torzHash)
				if err != nil {
					log.Error("failed to get torrent info by hash", "error", err, "hash", torzHash)
					return nil
				}
				episodes = tInfo.Episodes
			} else if folderName != "" {
				r, err := util.ParseTorrentTitle(folderName)
				if err != nil {
					log.Error("failed to parse title", "error", err, "title", folderName)
					return nil
				}
				episodes = r.Episodes
			} else {
				return nil
			}

			for _, f := range filesForSeason {
				d := dataByName[f.GetName()]
				if d.Episode == expectedEpisode || (len(episodes) == 0 && minEpisode > 1 && d.Episode-minEpisode+1 == expectedEpisode) {
					return f
				}
			}
		}

		return nil
	}

	return matchFileByIMDBStremId(files, sid, context)
}

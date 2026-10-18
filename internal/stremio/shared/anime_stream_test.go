package stremio_shared

import (
	"crypto/sha1"
	"database/sql"
	"fmt"
	"strconv"
	"testing"

	"github.com/MunifTanjim/stremthru/internal/anidb"
	"github.com/MunifTanjim/stremthru/internal/anime"
	"github.com/MunifTanjim/stremthru/internal/db"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/MunifTanjim/stremthru/store"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestAnimePlaybackCompatibility(t *testing.T) {
	sqlDB, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	goose.SetLogger(goose.NopLogger())
	require.NoError(t, goose.SetDialect("sqlite"))
	require.NoError(t, goose.Up(sqlDB, "../../../migrations/sqlite"))
	db.SetForTesting(sqlDB, t.Cleanup)
	for i, season := range []string{"1", "2", "0"} {
		id := strconv.Itoa(9988100 + i)
		require.NoError(t, anidb.UpsertTitles([]anidb.AniDBTitle{{TId: id, TType: "main", TLang: "en", Value: "Synthetic Anime", Season: season}}))
		require.NoError(t, anime.BulkRecordIdMaps([]anime.AnimeIdMap{{AniDB: id, Kitsu: id, MAL: id}}, anime.IdMapColumn.AniDB))
	}
	require.NoError(t, anidb.UpsertTVDBEpisodeMaps([]anidb.AniDBTVDBEpisodeMap{{AniDBId: "9988101", TVDBId: "9988101", AniDBSeason: 1, TVDBSeason: -1, Offset: 12, Before: anidb.AniDBTVDBEpisodeMapBefore{}, Map: anidb.AniDBTVDBEpisodeMapMap{}}}))
	cases := []struct {
		name, id, episode, folder string
		names                     []string
		want                      int
	}{
		{"relative", "9988100", "2", "Synthetic Anime", []string{"01.mkv", "02.mkv"}, 1},
		{"offset batch", "9988100", "2", "Synthetic Anime", []string{"13.mkv", "14.mkv"}, 1},
		{"absolute second season", "9988101", "2", "Synthetic Anime", []string{"13.mkv", "14.mkv"}, 1},
		{"explicit second season", "9988101", "2", "Synthetic Anime", []string{"S02E01.mkv", "S02E02.mkv"}, 1},
		{"specials", "9988102", "2", "Synthetic Anime", []string{"S00E01.mkv", "S00E02.mkv"}, 1},
		{"release extras", "9988100", "2", "Synthetic Anime", []string{"Synthetic Anime - 01.mkv", "Synthetic Anime - 02.mkv", "Synthetic Anime NCOP.mkv"}, 1},
	}
	for _, c := range cases {
		for _, prefix := range []string{"kitsu:", "mal:", "anidb:"} {
			t.Run(c.name+prefix, func(t *testing.T) {
				files := []store.File{}
				for i, n := range c.names {
					files = append(files, &store.MagnetFile{Name: n, Path: "/Season 99/" + n, Idx: i})
				}
				sid := prefix + c.id + ":" + c.episode
				current := MatchFileByStremId(c.folder, files, sid, "", store.StoreCodeTorBox)
				require.Same(t, files[c.want], current)
			})
		}
	}
	for _, prefix := range []string{"kitsu:", "mal:"} {
		t.Run("explicit mapping "+prefix, func(t *testing.T) {
			hash := fmt.Sprintf("%x", sha1.Sum([]byte(t.TempDir())))
			require.NoError(t, torrent_stream.Record([]torrent_stream.InsertData{{Hash: hash, File: torrent_stream.File{Path: "/Season 99/S99E20.mkv", Idx: 1, ASId: "9988100:2", Source: "tor"}}}, false))
			wrong := &store.MagnetFile{Name: "S99E20.mkv", Path: "/Other/S99E20.mkv", Idx: 1}
			right := &store.MagnetFile{Name: "S99E20.mkv", Path: "/Season 99/S99E20.mkv", Idx: 9}
			require.Same(t, right, MatchFileByStremIdWithPath("Synthetic Anime", []store.File{wrong, right}, prefix+"9988100:2", hash, store.StoreCodeRealDebrid, wrong.Path))
		})
	}
}

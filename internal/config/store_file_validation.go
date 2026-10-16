package config

import (
	"fmt"
	"log"
	"strconv"
	"strings"
)

func parseFileValidationBool(name, value string) (bool, error) {
	value = strings.TrimSpace(value)
	enabled, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("invalid %s %q: %w", name, value, err)
	}
	return enabled, nil
}

func loadFileValidationBool(name string) bool {
	value := strings.TrimSpace(getEnv(name))
	if value == "" {
		value = defaultValueByEnv[""][name]
	}
	enabled, err := parseFileValidationBool(name, value)
	if err != nil {
		log.Fatal(err)
	}
	return enabled
}

var StoreRecheckMissingFileLists = loadFileValidationBool("STREMTHRU_STORE_RECHECK_MISSING_FILE_LISTS")

// StoreRecheckMissingFileListsCrossStore controls fallback checks for cached
// results without a file list when StoreRecheckMissingFileLists is also enabled.
// Cache misses and no-video validation failures still fall back. Strict
// disallowed-file evidence does not.
var StoreRecheckMissingFileListsCrossStore = loadFileValidationBool("STREMTHRU_STORE_RECHECK_MISSING_FILE_LISTS_CROSS_STORE")

// AllowVideoTorrentsWithDisallowedFiles permits mixed torrents, but only allowed
// video files may be selected. This preference does not filter recorded evidence;
// storage still retains paths rather than separate supplied names.
var AllowVideoTorrentsWithDisallowedFiles = loadFileValidationBool("STREMTHRU_ALLOW_VIDEO_TORRENTS_WITH_DISALLOWED_FILES")

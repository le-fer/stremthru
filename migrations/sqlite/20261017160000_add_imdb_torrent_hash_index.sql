-- +goose Up
CREATE INDEX IF NOT EXISTS imdb_torrent_idx_hash ON imdb_torrent (hash);

-- +goose Down
DROP INDEX imdb_torrent_idx_hash;

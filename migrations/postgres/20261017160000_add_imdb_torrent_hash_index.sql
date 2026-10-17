-- +goose Up
CREATE INDEX IF NOT EXISTS imdb_torrent_idx_hash ON public.imdb_torrent (hash);

-- +goose Down
DROP INDEX public.imdb_torrent_idx_hash;

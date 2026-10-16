import { useQuery } from "@tanstack/react-query";

import { api } from "@/lib/api";

export type ConfigData = {
  database: ConfigDatabase;
  features: ConfigFeature[];
  instance: ConfigInstance;
  integrations: ConfigIntegration[];
  network: ConfigNetwork;
  newz: ConfigNewz;
  redis: ConfigRedis;
  server: ConfigServer;
  stores: ConfigStores;
  torz: ConfigTorz;
  tunnel: ConfigTunnel;
  users?: ConfigUser[];
  webdav: ConfigWebDAV;
};

type ConfigDatabase = {
  replica_uris?: string[];
  uri: string;
};

type ConfigFeature = {
  enabled: boolean;
  name: string;
  settings?: Record<string, string>;
};

type ConfigInstance = {
  base_url: string;
  id: string;
  is_public_instance: boolean;
  stremio_locked: boolean;
};

type ConfigIntegration = {
  enabled: boolean;
  name: string;
  settings?: Record<string, string>;
};

type ConfigNetwork = {
  buddy_url?: string;
  machine_ip: string;
  peer_flags?: string;
  peer_url?: string;
  pull_peer_url?: string;
  tunnel_ips?: Record<string, string>;
};

type ConfigNewz = {
  disabled: boolean;
  flags?: string[];
  max_connection_per_stream: string;
  nzb_file_cache_size: string;
  nzb_file_cache_ttl: string;
  nzb_file_max_size: string;
  nzb_link_mode?: Record<string, string>;
  segment_cache_size: string;
  stream_buffer_size: string;
};

type ConfigRedis = {
  disabled: boolean;
  uri: string;
};

type ConfigServer = {
  data_dir: string;
  environment: string;
  listen_addr: string;
  log_format: string;
  log_level: string;
  posthog_enabled: boolean;
  started_at: string;
  version: string;
};

type ConfigStore = {
  cached_stale_time?: string;
  config?: string;
  name: string;
  uncached_stale_time?: string;
};

type ConfigStores = {
  allow_video_torrents_with_disallowed_files: boolean;
  client_user_agent: string;
  disallowed_extensions: string[];
  items: ConfigStore[];
  recheck_missing_file_lists: boolean;
  recheck_missing_file_lists_cross_store: boolean;
};

type ConfigTorz = {
  disabled: boolean;
  torrent_file_cache_size?: string;
  torrent_file_cache_ttl?: string;
  torrent_file_max_size: string;
};

type ConfigTunnel = {
  by_host?: Record<string, string>;
  default: string;
  disabled: boolean;
};

type ConfigUser = {
  content_proxy_connection_limit?: number;
  is_admin: boolean;
  name: string;
  stores: string[];
};

type ConfigWebDAV = {
  file_ext_filter: {
    other: string[];
    subtitle: string[];
    video: string[];
  };
};

export function useConfig() {
  return useQuery({
    queryFn: async () => {
      const { data } = await api<ConfigData>("/config");
      return data;
    },
    queryKey: ["/config"],
    staleTime: Infinity,
  });
}

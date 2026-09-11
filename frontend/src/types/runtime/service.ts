export type RuntimeServiceStatus = {
  running: boolean;
  pid: number;
  pid_file?: string;
  listen_addr?: string;
  config_path?: string;
  cwd?: string;
  executable?: string;
  started_at?: string;
  restart_supported?: boolean;
  note?: string;
};

export type RuntimeServiceStatusResponse = {
  service: RuntimeServiceStatus;
};

export type RuntimeServiceRestartResult = {
  accepted: boolean;
  message?: string;
  requested_at?: string;
};

export type RuntimeServiceRestartResponse = {
  restart: RuntimeServiceRestartResult;
};

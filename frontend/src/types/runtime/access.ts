export type RuntimeAccessStatus = {
  required: boolean;
  authenticated: boolean;
  local: boolean;
  configured: boolean;
};

export type RuntimeAccessLoginResponse = {
  authenticated: boolean;
  expires_at?: string;
};

export type RuntimeAccessLogoutResponse = {
  authenticated: boolean;
};

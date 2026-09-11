export type SiteAccountDetectRequest = {
  base_url: string;
  timeout_ms?: number;
};

export type SiteAccountEndpointHit = {
  path: string;
  site_type: string;
  status_code: number;
  matched: boolean;
  protected?: boolean;
  detail?: string;
};

export type SiteAccountDetectResultPayload = {
  site_type: string;
  confidence: string;
  score?: Record<string, number>;
  hits?: SiteAccountEndpointHit[];
  platform_hints?: Record<string, unknown>;
  detected_at?: string;
  warnings?: string[];
};

export type SiteAccountDetectResult = {
  detect: SiteAccountDetectResultPayload;
};

export type SiteAccountFetchRequest = {
  base_url: string;
  site_type?: string;
  api_key?: string;
  system_access_token?: string;
  subject_user_id?: string | number;
  timeout_ms?: number;
  days?: number;
};

export type SiteAccountSubscription = {
  name?: string;
  status?: string;
  remaining?: number;
  period_end?: string;
  daily_limit?: number;
  weekly_limit?: number;
  monthly_limit?: number;
  daily_usage?: number;
  weekly_usage?: number;
  monthly_usage?: number;
};

export type SiteAccountUsage = {
  total_requests?: number;
  total_cost?: number;
  today_requests?: number;
  today_cost?: number;
};

export type SiteAccountView = {
  site_type?: string;
  confidence?: string;
  source?: string;
  mode?: string;
  currency?: string;
  balance_label?: string;
  balance_value?: number;
  wallet_balance?: number;
  quota_remaining?: number;
  quota_used?: number;
  quota_limit?: number;
  quota_balance?: number;
  plan_name?: string;
  subscriptions?: SiteAccountSubscription[];
  usage?: SiteAccountUsage;
  fetched_at?: string;
  partial?: boolean;
  errors?: string[];
  display_unit?: string;
  display_type?: string;
};

export type SiteAccountSnapshot = {
  site_type?: string;
  source?: string;
  currency?: string;
  mode?: string;
  wallet_balance?: number;
  is_available?: boolean;
  balance_details?: SiteAccountBalanceDetail[];
  quota_balance?: number;
  quota_remaining?: number;
  used_quota?: number;
  quota_limit?: number;
  plan_name?: string;
  subscriptions?: SiteAccountSubscription[];
  usage?: SiteAccountUsage;
  fetched_at?: string;
  partial?: boolean;
  errors?: string[];
};

export type SiteAccountBalanceDetail = {
  currency: string;
  total_balance: number;
  granted_balance: number;
  topped_up_balance: number;
};

export type ProviderAccountCache = {
  source?: string;
  mode?: string;
  currency?: string;
  wallet_balance?: number;
  is_available?: boolean;
  balance_details?: SiteAccountBalanceDetail[];
  quota_balance?: number;
  quota_remaining?: number;
  quota_used?: number;
  quota_limit?: number;
  quota_display_type?: string;
  quota_display_unit?: string;
  plan_name?: string;
  external_user_id?: string;
  external_username_masked?: string;
  subscriptions?: SiteAccountSubscription[];
  usage?: SiteAccountUsage;
  fetched_at?: string;
  partial?: boolean;
  last_error?: string;
};

export type SiteAccountFetchResult = {
  detect?: SiteAccountDetectResultPayload;
  account?: SiteAccountSnapshot;
  account_view?: SiteAccountView;
  balance_line?: string;
  warnings?: string[];
};

export type SiteAccountRefreshRequest = {
  site_type?: string;
  api_key?: string;
  system_access_token?: string;
  subject_user_id?: string | number;
  skip_detect?: boolean;
  timeout_ms?: number;
  days?: number;
  persist?: boolean;
  save_account_auth?: boolean;
};

export type SiteAccountRefreshResult = {
  provider: string;
  site_type: string;
  site_type_confidence?: string;
  site_type_detected_at?: string;
  site_type_scores?: Record<string, number>;
  detect?: SiteAccountDetectResultPayload;
  account?: SiteAccountSnapshot;
  account_view?: SiteAccountView;
  account_cache?: ProviderAccountCache;
  account_auth_ref?: string;
  balance_line?: string;
  warnings?: string[];
  persisted: boolean;
};

package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/siteaccount"
)

type balanceCommandResult struct {
	ConfigPath  string               `json:"config_path,omitempty"`
	Refreshed   bool                 `json:"refreshed"`
	Saved       bool                 `json:"saved,omitempty"`
	Providers   []balanceProviderRow `json:"providers"`
	Total       int                  `json:"total"`
	WithAccount int                  `json:"with_account"`
}

type balanceProviderRow struct {
	Name           string                          `json:"name"`
	Enabled        bool                            `json:"enabled"`
	Default        bool                            `json:"default,omitempty"`
	Protocol       string                          `json:"protocol,omitempty"`
	BaseURL        string                          `json:"base_url,omitempty"`
	SiteType       string                          `json:"site_type,omitempty"`
	AccountAuthRef string                          `json:"account_auth_ref,omitempty"`
	BalanceLine    string                          `json:"balance_line,omitempty"`
	Account        *config.ProviderAccountSnapshot `json:"account,omitempty"`
	AccountView    *siteaccount.AccountView        `json:"account_view,omitempty"`
	Status         string                          `json:"status"`
	Warning        string                          `json:"warning,omitempty"`
	Error          string                          `json:"error,omitempty"`
	Source         string                          `json:"source,omitempty"` // cache|live
}

// NewBalanceCommand creates `aicli balance` for listing provider account balances.
func NewBalanceCommand(configProvider func() *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "balance",
		Aliases: []string{"balances", "account", "accounts"},
		Short:   "查看 provider 账户余额 / 订阅额度",
		Long: `列出已配置 provider 的账户余额与订阅额度。

默认读取 config 中的缓存快照（login 时写入）。
使用 --refresh 可对 sub2api / new-api 等站点实时拉取。

示例：
  aicli balance
  aicli balance --provider my-sub2
  aicli balance --refresh
  aicli balance --refresh --save
  aicli balance --json
  aicli doctor balance --provider my-sub2 --refresh`,
		Example: `  aicli balance
  aicli balance --provider sub2api
  aicli balance --refresh --save
  aicli balance --json
  aicli doctor balance`,
		Run: func(cmd *cobra.Command, args []string) {
			HandleBalance(cmd, configProvider)
		},
	}
	cmd.Flags().StringP("provider", "p", "", "只显示指定 provider")
	cmd.Flags().Bool("refresh", false, "实时拉取账户余额（覆盖缓存展示）")
	cmd.Flags().Bool("save", false, "将 --refresh 结果写回 config（需同时指定 --refresh）")
	cmd.Flags().Duration("timeout", 15*time.Second, "实时拉取超时")
	cmd.Flags().Bool("enabled-only", false, "只显示已启用 provider")
	addBalanceOutputFlags(cmd)
	return cmd
}

// newDoctorBalanceCommand reuses the same handler under `aicli doctor balance`.
func newDoctorBalanceCommand(configProvider func() *config.Config) *cobra.Command {
	cmd := NewBalanceCommand(configProvider)
	cmd.Use = "balance"
	cmd.Short = "诊断/查看 provider 账户余额与订阅额度"
	cmd.Long = `查看 provider 账户余额与订阅额度（缓存或实时）。

等价于 aicli balance，挂在 doctor 下便于诊断流程串联：
  aicli doctor balance
  aicli doctor balance --provider my-sub2 --refresh --json`
	return cmd
}

func addBalanceOutputFlags(cmd *cobra.Command) {
	cmd.Flags().String("output", "", "输出格式（text|json）")
	cmd.Flags().Bool("json", false, "以 JSON 格式输出")
	cmd.Flags().Bool("envelope", false, "JSON 输出使用 {ok,command,data} 包装")
}

// HandleBalance runs the balance command.
func HandleBalance(cmd *cobra.Command, configProvider func() *config.Config) {
	outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
	if err != nil {
		exitCommandError("balance", "json", err, nil)
	}
	providerFilter := stringFlag(cmd, "provider")
	refresh := boolFlag(cmd, "refresh")
	save := boolFlag(cmd, "save")
	enabledOnly := boolFlag(cmd, "enabled-only")
	timeout, _ := cmd.Flags().GetDuration("timeout")
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	if save && !refresh {
		exitCommandError("balance", outputOptions.Format, fmt.Errorf("--save 需要同时指定 --refresh"), nil)
	}

	executeCommand("balance", outputOptions, func() (balanceCommandResult, map[string]interface{}, error) {
		return runBalanceCommand(providerCommandConfig(configProvider), balanceCommandOptions{
			Provider:    providerFilter,
			Refresh:     refresh,
			Save:        save,
			EnabledOnly: enabledOnly,
			Timeout:     timeout,
		})
	}, renderBalanceResult)
}

type balanceCommandOptions struct {
	Provider    string
	Refresh     bool
	Save        bool
	EnabledOnly bool
	Timeout     time.Duration
}

func runBalanceCommand(cfg *config.Config, opts balanceCommandOptions) (balanceCommandResult, map[string]interface{}, error) {
	result := balanceCommandResult{
		Refreshed: opts.Refresh,
	}
	if cfg == nil {
		return result, nil, fmt.Errorf("config is not loaded")
	}
	result.ConfigPath = strings.TrimSpace(cfg.ConfigFilePath)

	names := make([]string, 0, len(cfg.Providers.Items))
	for name := range cfg.Providers.Items {
		names = append(names, name)
	}
	sort.Strings(names)

	filter := strings.TrimSpace(opts.Provider)
	defaultName := strings.TrimSpace(cfg.Providers.DefaultProvider)
	pendingSaves := make([]config.ProviderConfigUpdate, 0)
	client := siteaccount.NewClient(nil)
	ctx := context.Background()

	for _, name := range names {
		if filter != "" && !strings.EqualFold(name, filter) {
			continue
		}
		provider := cfg.Providers.Items[name]
		if opts.EnabledOnly && !provider.Enabled {
			continue
		}
		row := balanceProviderRow{
			Name:           name,
			Enabled:        provider.Enabled,
			Default:        strings.EqualFold(name, defaultName),
			Protocol:       provider.GetProtocol(),
			BaseURL:        strings.TrimSpace(provider.BaseURL),
			SiteType:       strings.TrimSpace(provider.SiteType),
			AccountAuthRef: strings.TrimSpace(provider.AccountAuthRef),
			Source:         "cache",
			Status:         "no_account",
		}

		if opts.Refresh {
			live, liveErr := refreshProviderAccountBalance(ctx, client, name, &provider, opts.Timeout)
			if liveErr != nil {
				row.Error = liveErr.Error()
				row.Status = "refresh_error"
				// Fall back to cache if present.
				if provider.Account != nil {
					row.Account = cloneProviderAccountSnapshot(provider.Account)
					row.BalanceLine = formatProviderAccountBalanceLine(provider.Account, provider.SiteType, provider.SiteTypeConfidence)
					row.Status = "cache_after_error"
					row.Source = "cache"
					row.Warning = liveErr.Error()
					row.Error = ""
				}
			} else {
				row.Source = "live"
				row.Status = live.Status
				row.Warning = live.Warning
				row.BalanceLine = live.BalanceLine
				row.Account = live.Account
				row.AccountView = live.AccountView
				row.SiteType = firstNonEmptyText(live.SiteType, row.SiteType)
				row.AccountAuthRef = firstNonEmptyText(live.AccountAuthRef, row.AccountAuthRef)
				if live.Account != nil {
					provider.Account = cloneProviderAccountSnapshot(live.Account)
					if live.SiteType != "" {
						provider.SiteType = live.SiteType
					}
					if live.SiteTypeConfidence != "" {
						provider.SiteTypeConfidence = live.SiteTypeConfidence
					}
					if live.SiteTypeDetectedAt != "" {
						provider.SiteTypeDetectedAt = live.SiteTypeDetectedAt
					}
					if live.AccountAuthRef != "" {
						provider.AccountAuthRef = live.AccountAuthRef
					}
					cfg.Providers.Items[name] = provider
					if opts.Save {
						update := config.ProviderConfigUpdate{
							Name:    name,
							Account: cloneProviderAccountSnapshot(live.Account),
						}
						if live.SiteType != "" {
							update.SiteType = providerLoginStringValuePtr(live.SiteType)
						}
						if live.SiteTypeConfidence != "" {
							update.SiteTypeConfidence = providerLoginStringValuePtr(live.SiteTypeConfidence)
						}
						if live.SiteTypeDetectedAt != "" {
							update.SiteTypeDetectedAt = providerLoginStringValuePtr(live.SiteTypeDetectedAt)
						}
						if live.AccountAuthRef != "" {
							update.AccountAuthRef = providerLoginStringValuePtr(live.AccountAuthRef)
						}
						pendingSaves = append(pendingSaves, update)
					}
				}
			}
		} else if provider.Account != nil {
			row.Account = cloneProviderAccountSnapshot(provider.Account)
			row.BalanceLine = formatProviderAccountBalanceLine(provider.Account, provider.SiteType, provider.SiteTypeConfidence)
			if row.BalanceLine != "" {
				row.Status = "cached"
			} else {
				row.Status = "cached_empty"
			}
			if errText := strings.TrimSpace(provider.Account.LastError); errText != "" {
				row.Warning = errText
			}
		} else if strings.TrimSpace(provider.SiteType) != "" &&
			!strings.EqualFold(provider.SiteType, string(siteaccount.SiteTypeUnknown)) {
			row.Status = "no_account"
			row.Warning = "no cached account; try --refresh or aicli login --require-account"
		}

		result.Providers = append(result.Providers, row)
		if row.Account != nil || row.BalanceLine != "" {
			result.WithAccount++
		}
	}

	if filter != "" && len(result.Providers) == 0 {
		return result, map[string]interface{}{"provider": filter}, fmt.Errorf("provider %q not found", filter)
	}

	if opts.Save && len(pendingSaves) > 0 {
		configPath := strings.TrimSpace(cfg.ConfigFilePath)
		if configPath == "" {
			return result, nil, fmt.Errorf("config path is required to save balance refresh")
		}
		for _, update := range pendingSaves {
			if _, err := config.UpdateProviderConfig(configPath, update); err != nil {
				return result, nil, fmt.Errorf("save provider %s account: %w", update.Name, err)
			}
		}
		result.Saved = true
	}

	result.Total = len(result.Providers)
	return result, nil, nil
}

type liveBalanceOutcome struct {
	Status             string
	Warning            string
	BalanceLine        string
	Account            *config.ProviderAccountSnapshot
	AccountView        *siteaccount.AccountView
	SiteType           string
	SiteTypeConfidence string
	SiteTypeDetectedAt string
	AccountAuthRef     string
}

func refreshProviderAccountBalance(
	ctx context.Context,
	client *siteaccount.Client,
	providerName string,
	provider *config.Provider,
	timeout time.Duration,
) (liveBalanceOutcome, error) {
	out := liveBalanceOutcome{Status: "skipped"}
	if provider == nil {
		return out, fmt.Errorf("provider is nil")
	}
	if client == nil {
		client = siteaccount.NewClient(nil)
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}

	siteType := siteaccount.NormalizeSiteType(provider.SiteType)
	confidence := siteaccount.ConfidenceLow
	if conf := strings.TrimSpace(provider.SiteTypeConfidence); conf != "" {
		confidence = siteaccount.Confidence(conf)
	}

	// Auto-detect when site type is unknown/empty.
	if siteType == "" || siteType == siteaccount.SiteTypeUnknown {
		detectResult, detectErr := client.DetectSiteType(ctx, siteaccount.DetectInput{
			BaseURL: provider.BaseURL,
			Timeout: timeout,
		})
		if detectErr != nil {
			return out, fmt.Errorf("site detect failed: %w", detectErr)
		}
		siteType = detectResult.SiteType
		confidence = detectResult.Confidence
		out.SiteType = string(siteType)
		out.SiteTypeConfidence = string(confidence)
		if !detectResult.DetectedAt.IsZero() {
			out.SiteTypeDetectedAt = detectResult.DetectedAt.UTC().Format(time.RFC3339)
		}
	} else {
		out.SiteType = string(siteType)
		out.SiteTypeConfidence = string(confidence)
		out.SiteTypeDetectedAt = strings.TrimSpace(provider.SiteTypeDetectedAt)
	}

	switch siteType {
	case siteaccount.SiteTypeSub2API:
		apiKey := strings.TrimSpace(provider.GetAPIKey())
		if apiKey == "" {
			out.Status = "skipped"
			out.Warning = "api key missing"
			return out, fmt.Errorf("api key missing for sub2api account refresh")
		}
		snapshot, err := client.FetchAccountSnapshot(ctx, siteaccount.FetchInput{
			BaseURL:  provider.BaseURL,
			SiteType: siteaccount.SiteTypeSub2API,
			Credential: siteaccount.AccountCredential{
				APIKey: apiKey,
			},
			Timeout: timeout,
		})
		if err != nil {
			return out, err
		}
		view := siteaccount.NormalizeAccountView(snapshot, confidence)
		out.AccountView = &view
		out.BalanceLine = siteaccount.FormatBalanceLine(view)
		out.Account = providerAccountSnapshotFromSiteAccount(snapshot)
		out.Status = "ok"
		return out, nil

	case siteaccount.SiteTypeNewAPI:
		token, userID, authRef, warnings, err := resolveNewAPIAccountCredentials(providerLoginRequest{}, providerName, provider)
		if len(warnings) > 0 {
			out.Warning = strings.Join(warnings, "; ")
		}
		if err != nil {
			return out, err
		}
		if strings.TrimSpace(token) == "" || userID <= 0 {
			out.Status = "skipped"
			out.Warning = firstNonEmptyText(out.Warning, "new-api system access token or user id missing")
			return out, fmt.Errorf("%s", out.Warning)
		}
		snapshot, fetchErr := client.FetchAccountSnapshot(ctx, siteaccount.FetchInput{
			BaseURL:  provider.BaseURL,
			SiteType: siteaccount.SiteTypeNewAPI,
			Credential: siteaccount.AccountCredential{
				SystemAccessToken: token,
				SubjectUserID:     userID,
			},
			Timeout: timeout,
		})
		if fetchErr != nil {
			return out, fetchErr
		}
		view := siteaccount.NormalizeAccountView(snapshot, confidence)
		out.AccountView = &view
		out.BalanceLine = siteaccount.FormatBalanceLine(view)
		out.Account = providerAccountSnapshotFromSiteAccount(snapshot)
		out.AccountAuthRef = authRef
		out.Status = "ok"
		return out, nil

	default:
		out.Status = "unsupported"
		out.Warning = fmt.Sprintf("site type %q does not support account balance", siteType)
		return out, fmt.Errorf("%s", out.Warning)
	}
}

func renderBalanceResult(result balanceCommandResult, outputOptions structuredOutputOptions) {
	if isJSONOutputFormat(outputOptions.Format) {
		printCommandJSONOutput("balance", outputOptions.Envelope, result)
		return
	}
	if len(result.Providers) == 0 {
		fmt.Println("没有匹配的 provider")
		return
	}

	fmt.Printf("%-20s %-10s %-12s %-8s %s\n", "NAME", "SITE", "STATUS", "SOURCE", "BALANCE")
	for _, item := range result.Providers {
		balance := strings.TrimSpace(item.BalanceLine)
		if balance == "" {
			if item.Warning != "" {
				balance = item.Warning
			} else if item.Error != "" {
				balance = item.Error
			} else {
				balance = "-"
			}
		}
		fmt.Printf("%-20s %-10s %-12s %-8s %s\n",
			item.Name,
			emptyIfBlank(item.SiteType),
			emptyIfBlank(item.Status),
			emptyIfBlank(item.Source),
			balance,
		)
		if item.Account != nil && item.Account.PlanName != "" {
			fmt.Printf("  plan: %s\n", item.Account.PlanName)
		}
		if item.Account != nil && item.Account.FetchedAt != "" {
			fmt.Printf("  fetched_at: %s\n", item.Account.FetchedAt)
		}
		if item.Warning != "" && item.BalanceLine != "" {
			fmt.Printf("  warning: %s\n", item.Warning)
		}
	}
	fmt.Printf("\nTotal: %d  with_account: %d", result.Total, result.WithAccount)
	if result.Refreshed {
		fmt.Printf("  refreshed: true")
	}
	if result.Saved {
		fmt.Printf("  saved: true")
	}
	fmt.Println()
	if result.ConfigPath != "" {
		fmt.Printf("Config: %s\n", result.ConfigPath)
	}
	fmt.Println("提示: 实时刷新用 aicli balance --refresh [--save]；登录同步用 aicli login --require-account")
}

// formatProviderAccountBalanceLine renders a one-line balance summary from the
// cached provider account snapshot used by /status and balance CLI.
func formatProviderAccountBalanceLine(account *config.ProviderAccountSnapshot, siteType, confidence string) string {
	if account == nil {
		return ""
	}
	view := accountViewFromProviderSnapshot(account, siteType, confidence)
	return siteaccount.FormatBalanceLine(view)
}

func accountViewFromProviderSnapshot(account *config.ProviderAccountSnapshot, siteType, confidence string) siteaccount.AccountView {
	if account == nil {
		return siteaccount.AccountView{}
	}
	snapshot := &siteaccount.AccountSnapshot{
		SiteType:          siteaccount.NormalizeSiteType(siteType),
		Source:            account.Source,
		Currency:          account.Currency,
		Mode:              account.Mode,
		WalletBalance:     siteaccount.CloneFloat64(account.WalletBalance),
		QuotaBalance:      siteaccount.CloneFloat64(account.QuotaBalance),
		QuotaRemaining:    siteaccount.CloneFloat64(account.QuotaRemaining),
		UsedQuota:         siteaccount.CloneFloat64(account.QuotaUsed),
		QuotaLimit:        siteaccount.CloneFloat64(account.QuotaLimit),
		QuotaDisplayType:  account.QuotaDisplayType,
		QuotaDisplayUnit:  account.QuotaDisplayUnit,
		QuotaDisplayScale: siteaccount.CloneFloat64(account.QuotaDisplayScale),
		PlanName:          account.PlanName,
		Partial:           account.Partial,
	}
	if account.LastError != "" {
		snapshot.Errors = []string{account.LastError}
	}
	if ts := strings.TrimSpace(account.FetchedAt); ts != "" {
		if parsed, err := time.Parse(time.RFC3339, ts); err == nil {
			snapshot.FetchedAt = parsed
		}
	}
	if len(account.Subscriptions) > 0 {
		snapshot.Subscriptions = make([]siteaccount.SubscriptionSummary, 0, len(account.Subscriptions))
		for _, sub := range account.Subscriptions {
			item := siteaccount.SubscriptionSummary{
				Name:      sub.Name,
				Status:    sub.Status,
				Remaining: siteaccount.CloneFloat64(sub.Remaining),
			}
			if pe := strings.TrimSpace(sub.PeriodEnd); pe != "" {
				if parsed, err := time.Parse(time.RFC3339, pe); err == nil {
					item.PeriodEnd = &parsed
				}
			}
			snapshot.Subscriptions = append(snapshot.Subscriptions, item)
		}
	}
	if account.Usage != nil {
		snapshot.Usage = &siteaccount.UsageSummary{
			TotalRequests: cloneInt64(account.Usage.TotalRequests),
			TotalCost:     siteaccount.CloneFloat64(account.Usage.TotalCost),
			TodayRequests: cloneInt64(account.Usage.TodayRequests),
			TodayCost:     siteaccount.CloneFloat64(account.Usage.TodayCost),
		}
	}
	if account.ExternalUserID != "" || account.ExternalUsernameMasked != "" {
		snapshot.ExternalUser = &siteaccount.ExternalUserSummary{
			ID:       account.ExternalUserID,
			Username: account.ExternalUsernameMasked,
		}
	}
	conf := siteaccount.Confidence(strings.TrimSpace(confidence))
	if conf == "" {
		conf = siteaccount.ConfidenceLow
	}
	return siteaccount.NormalizeAccountView(snapshot, conf)
}

func cloneProviderAccountSnapshot(in *config.ProviderAccountSnapshot) *config.ProviderAccountSnapshot {
	if in == nil {
		return nil
	}
	out := *in
	out.WalletBalance = siteaccount.CloneFloat64(in.WalletBalance)
	out.QuotaBalance = siteaccount.CloneFloat64(in.QuotaBalance)
	out.QuotaRemaining = siteaccount.CloneFloat64(in.QuotaRemaining)
	out.QuotaUsed = siteaccount.CloneFloat64(in.QuotaUsed)
	out.QuotaLimit = siteaccount.CloneFloat64(in.QuotaLimit)
	out.QuotaDisplayScale = siteaccount.CloneFloat64(in.QuotaDisplayScale)
	if len(in.Subscriptions) > 0 {
		out.Subscriptions = make([]config.ProviderAccountSubscription, len(in.Subscriptions))
		copy(out.Subscriptions, in.Subscriptions)
		for i := range out.Subscriptions {
			out.Subscriptions[i].Remaining = siteaccount.CloneFloat64(in.Subscriptions[i].Remaining)
		}
	}
	if in.Usage != nil {
		usage := *in.Usage
		usage.TotalRequests = cloneInt64(in.Usage.TotalRequests)
		usage.TotalCost = siteaccount.CloneFloat64(in.Usage.TotalCost)
		usage.TodayRequests = cloneInt64(in.Usage.TodayRequests)
		usage.TodayCost = siteaccount.CloneFloat64(in.Usage.TodayCost)
		out.Usage = &usage
	}
	return &out
}

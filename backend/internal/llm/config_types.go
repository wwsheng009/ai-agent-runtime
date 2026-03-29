package llm

// HeaderMatchCondition defines an additional match condition.
type HeaderMatchCondition struct {
	Header    string `yaml:"header" mapstructure:"header" json:"header"`
	MatchType string `yaml:"match_type" mapstructure:"match_type" json:"match_type"`
	Match     string `yaml:"match" mapstructure:"match" json:"match"`
	Not       bool   `yaml:"not" mapstructure:"not" json:"not"`
}

// HeaderMappingRule defines a provider-level request header conditional rewrite rule.
type HeaderMappingRule struct {
	Name                string                 `yaml:"name" mapstructure:"name" json:"name"`
	Enabled             *bool                  `yaml:"enabled" mapstructure:"enabled" json:"enabled"`
	Header              string                 `yaml:"header" mapstructure:"header" json:"header"`
	TargetHeader        string                 `yaml:"target_header" mapstructure:"target_header" json:"target_header"`
	MatchType           string                 `yaml:"match_type" mapstructure:"match_type" json:"match_type"`
	Match               string                 `yaml:"match" mapstructure:"match" json:"match"`
	Conditions          []HeaderMatchCondition `yaml:"conditions" mapstructure:"conditions" json:"conditions"`
	OrConditions        []HeaderMatchCondition `yaml:"or_conditions" mapstructure:"or_conditions" json:"or_conditions"`
	Value               string                 `yaml:"value" mapstructure:"value" json:"value"`
	ReplaceWith         string                 `yaml:"replace_with" mapstructure:"replace_with" json:"replace_with"`
	ReplaceAll          *bool                  `yaml:"replace_all" mapstructure:"replace_all" json:"replace_all"`
	CopyFromHeader      string                 `yaml:"copy_from_header" mapstructure:"copy_from_header" json:"copy_from_header"`
	CopyMatchedValue    bool                   `yaml:"copy_matched_value" mapstructure:"copy_matched_value" json:"copy_matched_value"`
	CopyFromCapture     int                    `yaml:"copy_from_capture" mapstructure:"copy_from_capture" json:"copy_from_capture"`
	CopyFromCaptureName string                 `yaml:"copy_from_capture_name" mapstructure:"copy_from_capture_name" json:"copy_from_capture_name"`
	SetIfMissing        bool                   `yaml:"set_if_missing" mapstructure:"set_if_missing" json:"set_if_missing"`
	PrependValue        string                 `yaml:"prepend_value" mapstructure:"prepend_value" json:"prepend_value"`
	AppendValue         string                 `yaml:"append_value" mapstructure:"append_value" json:"append_value"`
	Priority            int                    `yaml:"priority" mapstructure:"priority" json:"priority"`
	Remove              bool                   `yaml:"remove" mapstructure:"remove" json:"remove"`
	StopOnMatch         bool                   `yaml:"stop_on_match" mapstructure:"stop_on_match" json:"stop_on_match"`
}

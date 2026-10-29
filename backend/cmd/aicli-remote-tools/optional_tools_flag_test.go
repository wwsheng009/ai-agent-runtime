package main

import (
	"strings"
	"testing"
)

func TestOptionalToolsAndNetworkFlagsReachConfigAndDoctor(t *testing.T) {
	flags := &serveFlags{
		workspace:           t.TempDir(),
		profile:             "ops",
		optionalTools:       "multiedit,fetch",
		network:             true,
		networkAllowedHosts: "example.com,sourcegraph.com",
		networkDeniedHosts:  "bad.example",
	}
	cfg, err := loadConfig(flags)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	names := strings.Join(cfg.OptionalToolNames(), ",")
	if names != "multiedit,fetch" {
		t.Fatalf("effective optional tools = %q, want multiedit,fetch", names)
	}
	if !cfg.Network.Enabled {
		t.Fatal("--network did not enable network")
	}
	if got := strings.Join(cfg.Network.AllowedHosts, ","); got != "example.com,sourcegraph.com" {
		t.Fatalf("allowed hosts = %q", got)
	}

	report := buildDoctorReport(cfg)
	if strings.Join(report.OptionalTools, ",") != "multiedit,fetch" {
		t.Fatalf("doctor optional tools = %v", report.OptionalTools)
	}
	if !report.NetworkEnabled {
		t.Fatal("doctor did not report network enabled")
	}
}

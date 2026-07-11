package main

import (
	"encoding/json"
	"runtime"
	"strings"
	"time"
)

type CloudDiagnosticBundle struct {
	GeneratedAt  time.Time          `json:"generatedAt"`
	AppVersion   string             `json:"appVersion"`
	OS           string             `json:"os"`
	Architecture string             `json:"architecture"`
	ProviderType string             `json:"providerType"`
	Mount        CloudMountResponse `json:"mount"`
	Capabilities VaultCapabilities  `json:"capabilities"`
}

func BuildCloudDiagnostics(mount CloudJournalMountRecord, provider VaultProvider, capabilities VaultCapabilities) ([]byte, error) {
	bundle := CloudDiagnosticBundle{GeneratedAt: time.Now().UTC(), AppVersion: appVersion, OS: runtime.GOOS, Architecture: runtime.GOARCH, ProviderType: provider.ID, Mount: mountResponse(mount), Capabilities: capabilities}
	bundle.Mount.CachePath = ""
	bundle.Mount.LastSyncError = redactDiagnosticText(bundle.Mount.LastSyncError)
	return json.Marshal(bundle)
}
func redactDiagnosticText(value string) string {
	value = strings.ReplaceAll(value, "\n", " ")
	if len(value) > 240 {
		value = value[:240]
	}
	for _, needle := range []string{"password", "token", "secret", "authorization"} {
		if strings.Contains(strings.ToLower(value), needle) {
			return "redacted provider error"
		}
	}
	return value
}

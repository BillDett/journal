package main

import "fmt"

// Settings persistence is isolated from document and library workflows.
func (s *JournalService) GetAppSettings() (AppSettingsResponse, error) {
	return AppSettingsResponse{AutosaveIntervalMS: s.autosaveIntervalMS(), LastDocumentID: s.settingValue(settingLastDocumentID), LibraryWidth: s.libraryWidth()}, nil
}

func (s *JournalService) UpdateAppSettings(settings AppSettingsPatch) (AppSettingsResponse, error) {
	interval := settings.AutosaveIntervalMS
	if interval < 500 {
		interval = defaultAutosaveIntervalMS
	}
	width := s.libraryWidth()
	if settings.LibraryWidth > 0 {
		width = clampInt(settings.LibraryWidth, minLibraryWidth, maxLibraryWidth)
	}
	now := nowString()
	if _, err := s.db.Exec(`INSERT INTO app_settings (key, value, updated_at) VALUES ('autosave_interval_ms', ?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, fmt.Sprintf("%d", interval), now); err != nil {
		return AppSettingsResponse{}, err
	}
	if _, err := s.db.Exec(`INSERT INTO app_settings (key, value, updated_at) VALUES (?, ?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, settingLibraryWidth, fmt.Sprintf("%d", width), now); err != nil {
		return AppSettingsResponse{}, err
	}
	return s.GetAppSettings()
}

func (s *JournalService) autosaveIntervalMS() int {
	var value string
	if err := s.db.QueryRow(`SELECT value FROM app_settings WHERE key = 'autosave_interval_ms'`).Scan(&value); err != nil {
		return defaultAutosaveIntervalMS
	}
	var parsed int
	if _, err := fmt.Sscanf(value, "%d", &parsed); err != nil || parsed < 500 {
		return defaultAutosaveIntervalMS
	}
	return parsed
}

func (s *JournalService) libraryWidth() int {
	var value string
	if err := s.db.QueryRow(`SELECT value FROM app_settings WHERE key = ?`, settingLibraryWidth).Scan(&value); err != nil {
		return defaultLibraryWidth
	}
	var parsed int
	if _, err := fmt.Sscanf(value, "%d", &parsed); err != nil {
		return defaultLibraryWidth
	}
	return clampInt(parsed, minLibraryWidth, maxLibraryWidth)
}

func clampInt(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

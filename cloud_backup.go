package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/google/uuid"
)

const cloudCredentialAD = "journal:v1:cloud-backup-credentials"

var ErrCloudBackupConflict = errors.New("the cloud backup changed on another device")
var ErrCloudBackupRestoreRequired = errors.New("a backup already exists at this endpoint; restore it before syncing")
var ErrCloudBackupNothingToSync = errors.New("there are no local changes to back up")

type cloudCredentials struct {
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
	SessionToken    string `json:"sessionToken,omitempty"`
}

type cloudBackupConfig struct {
	EndpointURL       string
	Bucket            string
	Region            string
	Prefix            string
	ForcePathStyle    bool
	DisplayName       string
	CredentialNonce   []byte
	CredentialCipher  []byte
	ValidatedAt       sql.NullString
	LastManifestToken sql.NullString
	LastSnapshotID    sql.NullString
	LastSnapshotHash  sql.NullString
	LastSnapshotSize  sql.NullInt64
	LastBackupAt      sql.NullString
	LastRemoteAt      sql.NullString
	LastError         sql.NullString
}

type cloudManifest struct {
	Format             string `json:"format"`
	FormatVersion      int    `json:"formatVersion"`
	SnapshotID         string `json:"snapshotId"`
	CreatedAt          string `json:"createdAt"`
	PreviousSnapshotID string `json:"previousSnapshotId,omitempty"`
	Database           struct {
		Key    string `json:"key"`
		SHA256 string `json:"sha256"`
		Size   int64  `json:"size"`
	} `json:"database"`
}

func (s *JournalService) ConfigureCloudBackup(ctx context.Context, command CloudBackupEndpointCommand) (CloudBackupStatusResponse, error) {
	config, credentialsValue, err := normalizeCloudBackupCommand(command)
	if err != nil {
		return CloudBackupStatusResponse{}, err
	}
	key, err := s.verifyMasterPassword(command.MasterPassword)
	if err != nil {
		return CloudBackupStatusResponse{}, err
	}
	defer zeroBytes(key)
	manifestData, token, missing, err := s.validateCloudEndpoint(ctx, config, credentialsValue)
	if err != nil {
		return CloudBackupStatusResponse{}, err
	}
	var remote cloudManifest
	if !missing {
		remote, err = parseCloudManifest(manifestData)
		if err != nil {
			return CloudBackupStatusResponse{}, fmt.Errorf("remote manifest is invalid: %w", err)
		}
	}
	payload, err := json.Marshal(credentialsValue)
	if err != nil {
		return CloudBackupStatusResponse{}, err
	}
	nonce, ciphertext, err := sealDetached(key, payload, []byte(cloudCredentialAD))
	if err != nil {
		return CloudBackupStatusResponse{}, err
	}
	previous, previousErr := s.loadCloudBackupConfig()
	if previousErr != nil && !errors.Is(previousErr, sql.ErrNoRows) {
		return CloudBackupStatusResponse{}, previousErr
	}
	sameDestination := previousErr == nil && sameCloudDestination(previous, config)
	lastManifestToken := sql.NullString{}
	lastSnapshotID := sql.NullString{}
	lastSnapshotHash := sql.NullString{}
	lastSnapshotSize := sql.NullInt64{}
	lastBackupAt := sql.NullString{}
	lastRemoteAt := sql.NullString{}
	if sameDestination {
		lastManifestToken = previous.LastManifestToken
		lastSnapshotID = previous.LastSnapshotID
		lastSnapshotHash = previous.LastSnapshotHash
		lastSnapshotSize = previous.LastSnapshotSize
		lastBackupAt = previous.LastBackupAt
		lastRemoteAt = previous.LastRemoteAt
	} else if !missing {
		lastManifestToken = sql.NullString{String: token, Valid: true}
		lastSnapshotID = sql.NullString{String: remote.SnapshotID, Valid: true}
		lastSnapshotHash = sql.NullString{String: remote.Database.SHA256, Valid: true}
		lastSnapshotSize = sql.NullInt64{Int64: remote.Database.Size, Valid: true}
		lastRemoteAt = sql.NullString{String: nowString(), Valid: true}
	}
	now := nowString()
	_, err = s.db.Exec(`INSERT INTO cloud_backup_config
		(id, endpoint_url, bucket, region, prefix, force_path_style, display_name, credential_nonce, credential_ciphertext, validated_at,
		 last_manifest_token, last_snapshot_id, last_snapshot_sha256, last_snapshot_size, last_backup_at, last_remote_at, created_at, updated_at)
		VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET endpoint_url=excluded.endpoint_url, bucket=excluded.bucket, region=excluded.region,
		prefix=excluded.prefix, force_path_style=excluded.force_path_style, display_name=excluded.display_name,
		credential_nonce=excluded.credential_nonce, credential_ciphertext=excluded.credential_ciphertext,
		validated_at=excluded.validated_at, last_manifest_token=excluded.last_manifest_token,
		last_snapshot_id=excluded.last_snapshot_id, last_snapshot_sha256=excluded.last_snapshot_sha256,
		last_snapshot_size=excluded.last_snapshot_size, last_backup_at=excluded.last_backup_at,
		last_remote_at=excluded.last_remote_at, last_error=NULL, updated_at=excluded.updated_at`,
		config.EndpointURL, config.Bucket, config.Region, config.Prefix, boolInt(config.ForcePathStyle), config.DisplayName,
		nonce, ciphertext, now, lastManifestToken, lastSnapshotID, lastSnapshotHash, lastSnapshotSize, lastBackupAt, lastRemoteAt, now, now)
	if err != nil {
		return CloudBackupStatusResponse{}, err
	}
	// Endpoint credentials are always re-entered, so require an explicit unlock
	// before the next sync even when only metadata changed.
	s.clearCloudCredentials()
	return s.GetCloudBackupStatus()
}

func (s *JournalService) GetCloudBackupStatus() (CloudBackupStatusResponse, error) {
	config, err := s.loadCloudBackupConfig()
	if errors.Is(err, sql.ErrNoRows) {
		s.cloudMu.Lock()
		busy := s.cloudBusy
		s.cloudMu.Unlock()
		return CloudBackupStatusResponse{Busy: busy}, nil
	}
	if err != nil {
		return CloudBackupStatusResponse{}, err
	}
	s.cloudMu.Lock()
	busy := s.cloudBusy
	credentialsReady := s.cloudCredentials != nil
	s.cloudMu.Unlock()
	status := cloudStatus(config, busy)
	status.Unsynced = s.cloudBackupUnsynced(config)
	status.CredentialsReady = credentialsReady
	return status, nil
}

// UnlockCloudBackupCredentials opens the stored endpoint credential for this
// app session only. It deliberately does not unlock encrypted Journals.
func (s *JournalService) UnlockCloudBackupCredentials(password string) (CloudBackupStatusResponse, error) {
	_, credentialsValue, err := s.cloudConfigAndCredentials(password)
	if err != nil {
		return CloudBackupStatusResponse{}, err
	}
	s.setCloudCredentials(credentialsValue)
	return s.GetCloudBackupStatus()
}

func (s *JournalService) DisconnectCloudBackup() error {
	if s.isCloudBusy() {
		return fmt.Errorf("cloud backup is busy")
	}
	_, err := s.db.Exec(`DELETE FROM cloud_backup_config WHERE id = 1`)
	if err == nil {
		s.clearCloudCredentials()
	}
	return err
}

func (s *JournalService) SyncCloudBackup(ctx context.Context) (CloudBackupStatusResponse, error) {
	if err := s.beginCloudOperation(); err != nil {
		return CloudBackupStatusResponse{}, err
	}
	operationEnded := false
	defer func() {
		if !operationEnded {
			s.endCloudOperation()
		}
	}()
	s.cloudWriteMu.Lock()
	defer s.cloudWriteMu.Unlock()
	config, credentialsValue, err := s.cloudConfigAndSession()
	if err != nil {
		return CloudBackupStatusResponse{}, err
	}
	if err := s.FlushAll(); err != nil {
		return CloudBackupStatusResponse{}, err
	}
	if !s.cloudBackupUnsynced(config) {
		return CloudBackupStatusResponse{}, ErrCloudBackupNothingToSync
	}
	generation, err := s.cloudChangeGeneration()
	if err != nil {
		return CloudBackupStatusResponse{}, s.recordCloudError(err)
	}
	staging, err := s.createSQLiteSnapshot("cloud-sync")
	if err != nil {
		return CloudBackupStatusResponse{}, s.recordCloudError(err)
	}
	defer os.Remove(staging)
	hash, size, err := fileSHA256(staging)
	if err != nil {
		return CloudBackupStatusResponse{}, s.recordCloudError(err)
	}
	client := newS3CloudClient(config, credentialsValue)
	manifestData, token, missing, err := client.getCurrent(ctx)
	if err != nil {
		return CloudBackupStatusResponse{}, s.recordCloudError(err)
	}
	if !missing {
		if _, err := parseCloudManifest(manifestData); err != nil {
			return CloudBackupStatusResponse{}, s.recordCloudError(fmt.Errorf("remote manifest is invalid: %w", err))
		}
	}
	if !missing && !config.LastBackupAt.Valid {
		return CloudBackupStatusResponse{}, s.recordCloudError(ErrCloudBackupRestoreRequired)
	}
	if config.LastManifestToken.Valid && config.LastManifestToken.String != token {
		return CloudBackupStatusResponse{}, s.recordCloudError(ErrCloudBackupConflict)
	}
	snapshotID := uuid.NewString()
	objectKey := cloudSnapshotKey(config.Prefix, snapshotID)
	if err := client.putFile(ctx, objectKey, staging); err != nil {
		return CloudBackupStatusResponse{}, s.recordCloudError(err)
	}
	if err := client.verifyFile(ctx, objectKey, hash, size); err != nil {
		return CloudBackupStatusResponse{}, s.recordCloudError(err)
	}
	manifest := cloudManifest{Format: "journal-cloud-backup", FormatVersion: 1, SnapshotID: snapshotID, CreatedAt: nowString()}
	manifest.Database.Key, manifest.Database.SHA256, manifest.Database.Size = objectKey, hash, size
	if !missing {
		previous, _ := parseCloudManifest(manifestData)
		manifest.PreviousSnapshotID = previous.SnapshotID
	}
	encodedManifest, err := json.Marshal(manifest)
	if err != nil {
		return CloudBackupStatusResponse{}, s.recordCloudError(err)
	}
	newToken, err := client.putCurrent(ctx, encodedManifest)
	if err != nil {
		return CloudBackupStatusResponse{}, s.recordCloudError(err)
	}
	confirmed, confirmedToken, confirmedMissing, err := client.getCurrent(ctx)
	if err != nil || confirmedMissing || confirmedToken != newToken {
		if err == nil {
			err = fmt.Errorf("published cloud manifest could not be verified")
		}
		return CloudBackupStatusResponse{}, s.recordCloudError(err)
	}
	if _, err := parseCloudManifest(confirmed); err != nil {
		return CloudBackupStatusResponse{}, s.recordCloudError(err)
	}
	if err := s.recordCloudBackupSuccess(newToken, snapshotID, hash, size, generation); err != nil {
		return CloudBackupStatusResponse{}, err
	}
	// Release the operation before building the response so the frontend does
	// not retain a completed sync's transient busy state.
	s.endCloudOperation()
	operationEnded = true
	return s.GetCloudBackupStatus()
}

// RestoreCloudBackup stages and validates a remote database, keeps a local
// recovery copy, then replaces the active database. The caller must restart
// the application after success because database-bound application state is no
// longer valid.
func (s *JournalService) RestoreCloudBackup(ctx context.Context, password string) (CloudBackupStatusResponse, error) {
	if err := s.beginCloudOperation(); err != nil {
		return CloudBackupStatusResponse{}, err
	}
	defer s.endCloudOperation()
	s.cloudWriteMu.Lock()
	defer s.cloudWriteMu.Unlock()
	config, credentialsValue, err := s.cloudConfigAndCredentials(password)
	if err != nil {
		return CloudBackupStatusResponse{}, err
	}
	if err := s.FlushAll(); err != nil {
		return CloudBackupStatusResponse{}, err
	}
	client := newS3CloudClient(config, credentialsValue)
	data, token, missing, err := client.getCurrent(ctx)
	if err != nil {
		return CloudBackupStatusResponse{}, s.recordCloudError(err)
	}
	if missing {
		return CloudBackupStatusResponse{}, s.recordCloudError(fmt.Errorf("no cloud backup has been published at this endpoint"))
	}
	manifest, err := parseCloudManifest(data)
	if err != nil {
		return CloudBackupStatusResponse{}, s.recordCloudError(fmt.Errorf("remote manifest is invalid: %w", err))
	}
	staging, err := s.downloadCloudSnapshot(ctx, client, manifest)
	if err != nil {
		return CloudBackupStatusResponse{}, s.recordCloudError(err)
	}
	defer os.Remove(staging)
	if err := validateSQLiteFile(staging); err != nil {
		return CloudBackupStatusResponse{}, s.recordCloudError(fmt.Errorf("downloaded database failed integrity validation: %w", err))
	}
	recovery, activePath, err := s.replaceDatabaseWith(staging)
	if err != nil {
		return CloudBackupStatusResponse{}, fmt.Errorf("restore failed; recovery database retained at %s: %w", recovery, err)
	}
	if err := recordRestoredCloudSyncState(activePath, token, manifest); err != nil {
		return CloudBackupStatusResponse{}, fmt.Errorf("restore completed but could not record its sync state: %w", err)
	}
	return CloudBackupStatusResponse{Configured: true, LastSnapshotID: manifest.SnapshotID, LastManifestToken: token}, nil
}

/*
The caller restarts after success. The returned recovery path is deliberately
kept outside the active database so a failed later startup still leaves a
plain SQLite recovery file for the user.
*/
func (s *JournalService) replaceDatabaseWith(staged string) (string, string, error) {
	path, err := s.databasePath()
	if err != nil {
		return "", "", err
	}
	recoveryPath := filepath.Join(filepath.Dir(path), "journal-recovery-"+time.Now().UTC().Format("20060102T150405Z")+".db")
	recoveryStaging, err := s.createSQLiteSnapshot("recovery")
	if err != nil {
		return "", "", err
	}
	if err := os.Rename(recoveryStaging, recoveryPath); err != nil {
		return "", "", err
	}
	if err := s.repository.Close(); err != nil {
		return recoveryPath, path, err
	}
	if err := os.Rename(staged, path); err != nil {
		return recoveryPath, path, fmt.Errorf("replace local database: %w", err)
	}
	return recoveryPath, path, nil
}

func recordRestoredCloudSyncState(path, token string, manifest cloudManifest) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer rollback(tx)
	now := nowString()
	if _, err := tx.Exec(`UPDATE cloud_backup_config SET last_manifest_token=?, last_snapshot_id=?, last_snapshot_sha256=?, last_snapshot_size=?, last_backup_at=?, last_remote_at=?, last_error=NULL, updated_at=? WHERE id=1`, token, manifest.SnapshotID, manifest.Database.SHA256, manifest.Database.Size, now, now, now); err != nil {
		return err
	}
	if err := ensureCloudBackupState(tx); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE cloud_backup_state SET last_backup_generation = change_generation WHERE id = 1`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *JournalService) cloudBackupUnsynced(config cloudBackupConfig) bool {
	if !config.LastBackupAt.Valid {
		return true
	}
	var generation int64
	var lastBackupGeneration sql.NullInt64
	err := s.db.QueryRow(`SELECT change_generation, last_backup_generation FROM cloud_backup_state WHERE id = 1`).Scan(&generation, &lastBackupGeneration)
	return err != nil || !lastBackupGeneration.Valid || generation != lastBackupGeneration.Int64
}

func (s *JournalService) cloudChangeGeneration() (int64, error) {
	var generation int64
	err := s.db.QueryRow(`SELECT change_generation FROM cloud_backup_state WHERE id = 1`).Scan(&generation)
	return generation, err
}

func (s *JournalService) recordCloudBackupSuccess(token, snapshotID, hash string, size, generation int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer rollback(tx)
	now := nowString()
	if _, err := tx.Exec(`UPDATE cloud_backup_config SET last_manifest_token=?, last_snapshot_id=?, last_snapshot_sha256=?, last_snapshot_size=?, last_backup_at=?, last_remote_at=?, last_error=NULL, updated_at=? WHERE id=1`, token, snapshotID, hash, size, now, now, now); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE cloud_backup_state SET last_backup_generation = ? WHERE id = 1`, generation); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *JournalService) cloudConfigAndCredentials(password string) (cloudBackupConfig, cloudCredentials, error) {
	config, err := s.loadCloudBackupConfig()
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return cloudBackupConfig{}, cloudCredentials{}, fmt.Errorf("configure a Cloud Backup endpoint first")
		}
		return cloudBackupConfig{}, cloudCredentials{}, err
	}
	if !config.ValidatedAt.Valid {
		return cloudBackupConfig{}, cloudCredentials{}, fmt.Errorf("Cloud Backup endpoint has not been validated")
	}
	key, err := s.verifyMasterPassword(password)
	if err != nil {
		return cloudBackupConfig{}, cloudCredentials{}, err
	}
	defer zeroBytes(key)
	payload, err := openDetached(key, config.CredentialNonce, config.CredentialCipher, []byte(cloudCredentialAD))
	if err != nil {
		return cloudBackupConfig{}, cloudCredentials{}, ErrInvalidMasterPassword
	}
	var credentialsValue cloudCredentials
	if err := json.Unmarshal(payload, &credentialsValue); err != nil {
		return cloudBackupConfig{}, cloudCredentials{}, fmt.Errorf("stored Cloud Backup credentials are invalid")
	}
	return config, credentialsValue, nil
}

func (s *JournalService) cloudConfigAndSession() (cloudBackupConfig, cloudCredentials, error) {
	config, err := s.loadCloudBackupConfig()
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return cloudBackupConfig{}, cloudCredentials{}, fmt.Errorf("configure a Cloud Backup endpoint first")
		}
		return cloudBackupConfig{}, cloudCredentials{}, err
	}
	if !config.ValidatedAt.Valid {
		return cloudBackupConfig{}, cloudCredentials{}, fmt.Errorf("Cloud Backup endpoint has not been validated")
	}
	credentialsValue, ok := s.cloudCredentialsSnapshot()
	if !ok {
		return cloudBackupConfig{}, cloudCredentials{}, fmt.Errorf("enter the master password to start Cloud Backup syncing")
	}
	return config, credentialsValue, nil
}

func (s *JournalService) setCloudCredentials(credentialsValue cloudCredentials) {
	s.cloudMu.Lock()
	copy := credentialsValue
	s.cloudCredentials = &copy
	s.cloudMu.Unlock()
}

func (s *JournalService) cloudCredentialsSnapshot() (cloudCredentials, bool) {
	s.cloudMu.Lock()
	defer s.cloudMu.Unlock()
	if s.cloudCredentials == nil {
		return cloudCredentials{}, false
	}
	return *s.cloudCredentials, true
}

func (s *JournalService) clearCloudCredentials() {
	s.cloudMu.Lock()
	if s.cloudCredentials != nil {
		s.cloudCredentials.AccessKeyID = ""
		s.cloudCredentials.SecretAccessKey = ""
		s.cloudCredentials.SessionToken = ""
		s.cloudCredentials = nil
	}
	s.cloudMu.Unlock()
}

// rewrapCloudBackupCredentials keeps endpoint credentials usable after the
// master password changes without coupling them to the encrypted-Journal key
// cache. It is called before the password-change transaction begins because
// the service intentionally uses a single SQLite connection.
func (s *JournalService) rewrapCloudBackupCredentials(oldKey, newKey []byte) ([]byte, []byte, bool, error) {
	config, err := s.loadCloudBackupConfig()
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, err
	}
	payload, err := openDetached(oldKey, config.CredentialNonce, config.CredentialCipher, []byte(cloudCredentialAD))
	if err != nil {
		return nil, nil, false, fmt.Errorf("Cloud Backup credentials could not be verified")
	}
	nonce, ciphertext, err := sealDetached(newKey, payload, []byte(cloudCredentialAD))
	if err != nil {
		return nil, nil, false, err
	}
	return nonce, ciphertext, true, nil
}

func (s *JournalService) loadCloudBackupConfig() (cloudBackupConfig, error) {
	var config cloudBackupConfig
	var forcePathStyle int
	err := s.db.QueryRow(`SELECT endpoint_url, bucket, region, prefix, force_path_style, display_name, credential_nonce, credential_ciphertext, validated_at, last_manifest_token, last_snapshot_id, last_snapshot_sha256, last_snapshot_size, last_backup_at, last_remote_at, last_error FROM cloud_backup_config WHERE id = 1`).Scan(
		&config.EndpointURL, &config.Bucket, &config.Region, &config.Prefix, &forcePathStyle, &config.DisplayName, &config.CredentialNonce, &config.CredentialCipher,
		&config.ValidatedAt, &config.LastManifestToken, &config.LastSnapshotID, &config.LastSnapshotHash, &config.LastSnapshotSize, &config.LastBackupAt, &config.LastRemoteAt, &config.LastError,
	)
	config.ForcePathStyle = forcePathStyle != 0
	return config, err
}

func normalizeCloudBackupCommand(command CloudBackupEndpointCommand) (cloudBackupConfig, cloudCredentials, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(command.EndpointURL), "/")
	if !strings.HasPrefix(endpoint, "https://") {
		return cloudBackupConfig{}, cloudCredentials{}, fmt.Errorf("S3 endpoint must use HTTPS")
	}
	if strings.TrimSpace(command.Bucket) == "" || strings.TrimSpace(command.Region) == "" {
		return cloudBackupConfig{}, cloudCredentials{}, fmt.Errorf("bucket and signing region are required")
	}
	credentialsValue := cloudCredentials{AccessKeyID: strings.TrimSpace(command.AccessKeyID), SecretAccessKey: strings.TrimSpace(command.SecretAccessKey), SessionToken: strings.TrimSpace(command.SessionToken)}
	if credentialsValue.AccessKeyID == "" || credentialsValue.SecretAccessKey == "" {
		return cloudBackupConfig{}, cloudCredentials{}, fmt.Errorf("access key ID and secret access key are required")
	}
	return cloudBackupConfig{EndpointURL: endpoint, Bucket: strings.TrimSpace(command.Bucket), Region: strings.TrimSpace(command.Region), Prefix: strings.Trim(strings.TrimSpace(command.Prefix), "/"), ForcePathStyle: command.ForcePathStyle, DisplayName: strings.TrimSpace(command.DisplayName)}, credentialsValue, nil
}

func sameCloudDestination(a, b cloudBackupConfig) bool {
	return a.EndpointURL == b.EndpointURL && a.Bucket == b.Bucket && a.Prefix == b.Prefix
}

func cloudStatus(config cloudBackupConfig, busy bool) CloudBackupStatusResponse {
	return CloudBackupStatusResponse{Configured: true, Validated: config.ValidatedAt.Valid, EndpointURL: config.EndpointURL, Bucket: config.Bucket, Region: config.Region, Prefix: config.Prefix, ForcePathStyle: config.ForcePathStyle, DisplayName: config.DisplayName, LastBackupAt: nullString(config.LastBackupAt), LastRemoteAt: nullString(config.LastRemoteAt), LastSnapshotID: nullString(config.LastSnapshotID), LastManifestToken: nullString(config.LastManifestToken), LastError: nullString(config.LastError), Busy: busy}
}

func (s *JournalService) validateCloudEndpoint(ctx context.Context, config cloudBackupConfig, credentialsValue cloudCredentials) ([]byte, string, bool, error) {
	client := newS3CloudClient(config, credentialsValue)
	data, token, missing, err := client.getCurrent(ctx)
	if err != nil {
		return nil, "", false, err
	}
	if err := client.validate(ctx); err != nil {
		return nil, "", false, err
	}
	return data, token, missing, nil
}

func (s *JournalService) beginCloudOperation() error {
	s.cloudMu.Lock()
	defer s.cloudMu.Unlock()
	if s.cloudBusy {
		return fmt.Errorf("a Cloud Backup operation is already running")
	}
	s.cloudBusy = true
	return nil
}

func (s *JournalService) endCloudOperation() {
	s.cloudMu.Lock()
	s.cloudBusy = false
	s.cloudMu.Unlock()
}
func (s *JournalService) isCloudBusy() bool {
	s.cloudMu.Lock()
	defer s.cloudMu.Unlock()
	return s.cloudBusy
}

func (s *JournalService) recordCloudError(err error) error {
	if err == nil {
		return nil
	}
	_, _ = s.db.Exec(`UPDATE cloud_backup_config SET last_error=?, updated_at=? WHERE id=1`, err.Error(), nowString())
	return err
}

func (s *JournalService) createSQLiteSnapshot(label string) (string, error) {
	path, err := s.databasePath()
	if err != nil {
		return "", err
	}
	stagingDir := filepath.Join(filepath.Dir(path), ".journal-cloud-staging")
	if err := os.MkdirAll(stagingDir, 0o700); err != nil {
		return "", err
	}
	staging := filepath.Join(stagingDir, fmt.Sprintf("%s-%s.db", label, uuid.NewString()))
	quoted := strings.ReplaceAll(staging, "'", "''")
	if _, err := s.db.Exec("PRAGMA wal_checkpoint(FULL)"); err != nil {
		return "", err
	}
	if _, err := s.db.Exec("VACUUM INTO '" + quoted + "'"); err != nil {
		return "", err
	}
	if err := validateSQLiteFile(staging); err != nil {
		_ = os.Remove(staging)
		return "", err
	}
	return staging, nil
}

func (s *JournalService) databasePath() (string, error) {
	var sequence int
	var name, file string
	if err := s.db.QueryRow(`PRAGMA database_list`).Scan(&sequence, &name, &file); err != nil {
		return "", err
	}
	if file == "" {
		return "", fmt.Errorf("active database has no filesystem path")
	}
	return file, nil
}

func validateSQLiteFile(path string) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	var result string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("SQLite integrity check returned %q", result)
	}
	return nil
}

func fileSHA256(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func (s *JournalService) downloadCloudSnapshot(ctx context.Context, client *s3CloudClient, manifest cloudManifest) (string, error) {
	path, err := s.databasePath()
	if err != nil {
		return "", err
	}
	stagingDir := filepath.Join(filepath.Dir(path), ".journal-cloud-staging")
	if err := os.MkdirAll(stagingDir, 0o700); err != nil {
		return "", err
	}
	staging := filepath.Join(stagingDir, "cloud-restore-"+uuid.NewString()+".db")
	if err := client.downloadFile(ctx, manifest.Database.Key, staging); err != nil {
		return "", err
	}
	hash, size, err := fileSHA256(staging)
	if err != nil {
		_ = os.Remove(staging)
		return "", err
	}
	if size != manifest.Database.Size || !strings.EqualFold(hash, manifest.Database.SHA256) {
		_ = os.Remove(staging)
		return "", fmt.Errorf("downloaded snapshot digest or size does not match the manifest")
	}
	return staging, nil
}

func parseCloudManifest(data []byte) (cloudManifest, error) {
	var manifest cloudManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return cloudManifest{}, err
	}
	if manifest.Format != "journal-cloud-backup" || manifest.FormatVersion != 1 || manifest.SnapshotID == "" || manifest.Database.Key == "" || manifest.Database.SHA256 == "" || manifest.Database.Size < 1 {
		return cloudManifest{}, fmt.Errorf("unsupported or incomplete manifest")
	}
	if _, err := hex.DecodeString(manifest.Database.SHA256); err != nil {
		return cloudManifest{}, fmt.Errorf("manifest digest is not SHA-256")
	}
	return manifest, nil
}

func cloudRoot(prefix string) string {
	if prefix = strings.Trim(prefix, "/"); prefix != "" {
		return prefix + "/journal-cloud-backup"
	}
	return "journal-cloud-backup"
}
func cloudCurrentKey(prefix string) string { return cloudRoot(prefix) + "/current.json" }
func cloudSnapshotKey(prefix, id string) string {
	return cloudRoot(prefix) + "/snapshots/" + id + "/journal.db"
}
func nullString(value sql.NullString) string {
	if value.Valid {
		return value.String
	}
	return ""
}
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
func zeroBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}

type s3CloudClient struct {
	client *s3.Client
	config cloudBackupConfig
}

func newS3CloudClient(config cloudBackupConfig, credentialsValue cloudCredentials) *s3CloudClient {
	awsConfig := aws.Config{
		Region:                     config.Region,
		Credentials:                credentials.NewStaticCredentialsProvider(credentialsValue.AccessKeyID, credentialsValue.SecretAccessKey, credentialsValue.SessionToken),
		BaseEndpoint:               aws.String(config.EndpointURL),
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	}
	return &s3CloudClient{client: s3.NewFromConfig(awsConfig, func(options *s3.Options) { options.UsePathStyle = config.ForcePathStyle }), config: config}
}

func (c *s3CloudClient) getCurrent(ctx context.Context) ([]byte, string, bool, error) {
	output, err := c.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(c.config.Bucket), Key: aws.String(cloudCurrentKey(c.config.Prefix))})
	if err != nil {
		if s3NotFound(err) {
			return nil, "", true, nil
		}
		return nil, "", false, fmt.Errorf("read Cloud Backup manifest: %w", err)
	}
	defer output.Body.Close()
	data, err := io.ReadAll(output.Body)
	if err != nil {
		return nil, "", false, err
	}
	return data, aws.ToString(output.ETag), false, nil
}

func (c *s3CloudClient) putFile(ctx context.Context, key, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	input := &s3.PutObjectInput{Bucket: aws.String(c.config.Bucket), Key: aws.String(key), Body: file, ContentLength: aws.Int64(info.Size())}
	_, err = c.client.PutObject(ctx, input)
	if err != nil {
		return fmt.Errorf("upload Cloud Backup object: %w", err)
	}
	return nil
}

func (c *s3CloudClient) verifyFile(ctx context.Context, key, expectedHash string, expectedSize int64) error {
	output, err := c.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(c.config.Bucket), Key: aws.String(key)})
	if err != nil {
		return fmt.Errorf("verify Cloud Backup object: %w", err)
	}
	if aws.ToInt64(output.ContentLength) != expectedSize {
		return fmt.Errorf("uploaded Cloud Backup object size did not match")
	}
	object, err := c.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(c.config.Bucket), Key: aws.String(key)})
	if err != nil {
		return fmt.Errorf("read back Cloud Backup object: %w", err)
	}
	defer object.Body.Close()
	hash := sha256.New()
	actualSize, err := io.Copy(hash, object.Body)
	if err != nil {
		return fmt.Errorf("read back Cloud Backup object: %w", err)
	}
	if actualSize != expectedSize || !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), expectedHash) {
		return fmt.Errorf("uploaded Cloud Backup object digest did not match")
	}
	return nil
}

// validate uses one immutable probe object. The harmless probe is intentionally
// never deleted because the configured principal does not need DeleteObject.
// A UUID key makes collision infeasible without requiring optional conditional
// request headers that Backblaze B2's S3 API does not implement.
func (c *s3CloudClient) validate(ctx context.Context) error {
	key := cloudRoot(c.config.Prefix) + "/validation/" + uuid.NewString() + ".json"
	value := []byte(`{"format":"journal-cloud-backup-validation","version":1}`)
	checksum := sha256.Sum256(value)
	input := func() *s3.PutObjectInput {
		return &s3.PutObjectInput{
			Bucket:        aws.String(c.config.Bucket),
			Key:           aws.String(key),
			Body:          strings.NewReader(string(value)),
			ContentLength: aws.Int64(int64(len(value))),
		}
	}
	if _, err := c.client.PutObject(ctx, input()); err != nil {
		return fmt.Errorf("validate Cloud Backup write access: %w", err)
	}
	if err := c.verifyFile(ctx, key, hex.EncodeToString(checksum[:]), int64(len(value))); err != nil {
		return err
	}
	return nil
}

func (c *s3CloudClient) putCurrent(ctx context.Context, data []byte) (string, error) {
	input := &s3.PutObjectInput{Bucket: aws.String(c.config.Bucket), Key: aws.String(cloudCurrentKey(c.config.Prefix)), Body: strings.NewReader(string(data)), ContentLength: aws.Int64(int64(len(data)))}
	output, err := c.client.PutObject(ctx, input)
	if err != nil {
		return "", fmt.Errorf("publish Cloud Backup manifest: %w", err)
	}
	return aws.ToString(output.ETag), nil
}

func (c *s3CloudClient) downloadFile(ctx context.Context, key, path string) error {
	output, err := c.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(c.config.Bucket), Key: aws.String(key)})
	if err != nil {
		return fmt.Errorf("download Cloud Backup snapshot: %w", err)
	}
	defer output.Body.Close()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(file, output.Body)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func s3NotFound(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NoSuchKey" || apiErr.ErrorCode() == "NotFound" || apiErr.ErrorCode() == "404") {
		return true
	}
	var responseErr *smithyhttp.ResponseError
	return errors.As(err, &responseErr) && responseErr.HTTPStatusCode() == 404
}

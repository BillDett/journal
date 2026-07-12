package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	vaultCurrentFormat  = "journal-vault-current"
	vaultRevisionFormat = "journal-vault-revision"
	vaultLeaseFormat    = "journal-vault-lease"
	vaultMetadataFormat = "journal-vault-metadata"
	vaultFormatVersion  = 1
	vaultRevisionPrefix = "rev-"
	vaultRevisionLayout = "20060102T150405.000Z"
)

type VaultProvider struct {
	ID   string
	Root string
}

type VaultCapabilities struct {
	ImmutableWrite    bool
	ConditionalWrite  bool
	ConditionalCreate bool
	ReadAfterWrite    bool
	ObjectListing     bool
	ConditionalDelete bool
}

type ObjectMeta struct {
	Key     string
	Size    int64
	Digest  string
	Version string
}

type VaultStore interface {
	Validate(context.Context, VaultProvider) (VaultCapabilities, error)
	GetObject(context.Context, VaultProvider, string) (io.ReadCloser, ObjectMeta, error)
	PutImmutable(context.Context, VaultProvider, string, io.Reader, string) (ObjectMeta, error)
	HeadObject(context.Context, VaultProvider, string) (ObjectMeta, error)
	GetControl(context.Context, VaultProvider, string) ([]byte, string, error)
	PutControlIf(context.Context, VaultProvider, string, []byte, string) (string, error)
	CreateControlIfAbsent(context.Context, VaultProvider, string, []byte) (string, error)
}

type VaultMaintenanceStore interface {
	ListPrefix(context.Context, VaultProvider, string) ([]ObjectMeta, error)
	DeleteImmutableIfVersion(context.Context, VaultProvider, string, string) error
}

type VaultErrorKind string

const (
	VaultNotFound      VaultErrorKind = "not_found"
	VaultConflict      VaultErrorKind = "precondition_failed"
	VaultAlreadyExists VaultErrorKind = "already_exists"
	VaultUnavailable   VaultErrorKind = "unavailable"
	VaultUnauthorized  VaultErrorKind = "unauthorized"
	VaultMalformed     VaultErrorKind = "malformed"
)

type VaultError struct {
	Kind VaultErrorKind
	Err  error
}

func (e *VaultError) Error() string {
	if e.Err != nil {
		return string(e.Kind) + ": " + e.Err.Error()
	}
	return string(e.Kind)
}
func (e *VaultError) Unwrap() error { return e.Err }

func vaultKey(cloudJournalID, suffix string) (string, error) {
	if err := validateCloudJournalID(cloudJournalID); err != nil {
		return "", err
	}
	suffix = strings.Trim(strings.TrimSpace(suffix), "/")
	if suffix == "" || strings.Contains(suffix, "..") {
		return "", fmt.Errorf("invalid Vault key suffix")
	}
	return "journals/" + cloudJournalID + "/" + suffix, nil
}
func vaultCurrentKey(id string) (string, error) { return vaultKey(id, "current.json") }
func vaultLeaseKey(id string) (string, error)   { return vaultKey(id, "lease.json") }
func vaultJournalMetadataKey(id string) (string, error) {
	return vaultKey(id, "journal.json")
}
func vaultManifestKey(id, revisionID string) (string, error) {
	if err := validateVaultRevisionID(revisionID); err != nil {
		return "", err
	}
	return vaultKey(id, "revisions/"+revisionID+"/manifest.json")
}
func vaultDatabaseKey(id, revisionID string) (string, error) {
	if err := validateVaultRevisionID(revisionID); err != nil {
		return "", err
	}
	return vaultKey(id, "revisions/"+revisionID+"/journal.db")
}

type VaultObjectDescriptor struct {
	Key    string `json:"key"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type VaultCurrent struct {
	Format             string                `json:"format"`
	FormatVersion      int                   `json:"formatVersion"`
	CloudJournalID     string                `json:"cloudJournalId"`
	RevisionID         string                `json:"revisionId"`
	RevisionNumber     int64                 `json:"revisionNumber"`
	RevisionManifest   VaultObjectDescriptor `json:"revisionManifest"`
	UpdatedAt          time.Time             `json:"updatedAt"`
	UpdatedAtLocal     string                `json:"updatedAtLocal,omitempty"`
	PreviousRevisionID string                `json:"previousRevisionId,omitempty"`
	PortableEncryption struct {
		Enabled         bool `json:"enabled"`
		MetadataVersion int  `json:"metadataVersion"`
	} `json:"portableEncryption"`
}
type VaultRevisionManifest struct {
	Format            string                 `json:"format"`
	FormatVersion     int                    `json:"formatVersion"`
	CloudJournalID    string                 `json:"cloudJournalId"`
	RevisionID        string                 `json:"revisionId"`
	RevisionNumber    int64                  `json:"revisionNumber"`
	ParentRevisionID  string                 `json:"parentRevisionId,omitempty"`
	CreatedAt         time.Time              `json:"createdAt"`
	CreatedAtLocal    string                 `json:"createdAtLocal,omitempty"`
	CreatedByDeviceID string                 `json:"createdByDeviceId"`
	Database          VaultObjectDescriptor  `json:"database"`
	Attachments       []AttachmentDescriptor `json:"attachments"`
	Encryption        struct {
		Enabled         bool `json:"enabled"`
		MetadataVersion int  `json:"metadataVersion"`
	} `json:"encryption"`
}
type VaultLease struct {
	Format              string    `json:"format"`
	FormatVersion       int       `json:"formatVersion"`
	CloudJournalID      string    `json:"cloudJournalId"`
	LeaseID             string    `json:"leaseId"`
	OwnerDeviceID       string    `json:"ownerDeviceId"`
	OwnerLabel          string    `json:"ownerLabel"`
	AcquiredAt          time.Time `json:"acquiredAt"`
	ExpiresAt           time.Time `json:"expiresAt"`
	CurrentPointerToken string    `json:"currentPointerToken"`
}

// VaultJournalMetadata is a public, advisory label for browsing a Vault. It
// never controls recovery or data integrity; current.json remains authoritative.
type VaultJournalMetadata struct {
	Format         string    `json:"format"`
	FormatVersion  int       `json:"formatVersion"`
	CloudJournalID string    `json:"cloudJournalId"`
	DisplayName    string    `json:"displayName"`
	UpdatedAt      time.Time `json:"updatedAt"`
	UpdatedAtLocal string    `json:"updatedAtLocal,omitempty"`
}

func canonicalVaultJSON(value any) ([]byte, error) { return json.Marshal(value) }
func parseVaultCurrent(data []byte) (VaultCurrent, error) {
	var v VaultCurrent
	if len(data) > 1<<20 {
		return v, fmt.Errorf("current pointer too large")
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return v, err
	}
	if v.Format != vaultCurrentFormat || v.FormatVersion != vaultFormatVersion || v.RevisionNumber < 1 {
		return v, fmt.Errorf("invalid current pointer format")
	}
	if err := validateCloudJournalID(v.CloudJournalID); err != nil {
		return v, err
	}
	if err := validateVaultLocalDisplayTime(v.UpdatedAtLocal); err != nil {
		return v, err
	}
	key, err := vaultManifestKey(v.CloudJournalID, v.RevisionID)
	if err != nil || v.RevisionManifest.Key != key {
		return v, fmt.Errorf("invalid current manifest key")
	}
	if err := validateVaultDescriptor(v.RevisionManifest); err != nil {
		return v, err
	}
	return v, nil
}
func parseVaultManifest(data []byte) (VaultRevisionManifest, error) {
	var v VaultRevisionManifest
	if len(data) > 8<<20 {
		return v, fmt.Errorf("manifest too large")
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return v, err
	}
	if v.Format != vaultRevisionFormat || v.FormatVersion != vaultFormatVersion || v.RevisionNumber < 1 || strings.TrimSpace(v.CreatedByDeviceID) == "" {
		return v, fmt.Errorf("invalid revision manifest")
	}
	if err := validateCloudJournalID(v.CloudJournalID); err != nil {
		return v, err
	}
	if err := validateVaultLocalDisplayTime(v.CreatedAtLocal); err != nil {
		return v, err
	}
	key, err := vaultDatabaseKey(v.CloudJournalID, v.RevisionID)
	if err != nil || v.Database.Key != key {
		return v, fmt.Errorf("invalid database key")
	}
	if err := validateVaultDescriptor(v.Database); err != nil {
		return v, err
	}
	if err := ValidateAttachmentDescriptors(v.CloudJournalID, v.Attachments); err != nil {
		return v, err
	}
	return v, nil
}
func parseVaultLease(data []byte) (VaultLease, error) {
	var v VaultLease
	if len(data) > 1<<20 {
		return v, fmt.Errorf("lease too large")
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return v, err
	}
	if v.Format != vaultLeaseFormat || v.FormatVersion != vaultFormatVersion || v.LeaseID == "" || v.OwnerDeviceID == "" || v.AcquiredAt.IsZero() || v.ExpiresAt.IsZero() {
		return v, fmt.Errorf("invalid lease")
	}
	if err := validateCloudJournalID(v.CloudJournalID); err != nil {
		return v, err
	}
	return v, nil
}

func parseVaultJournalMetadata(data []byte) (VaultJournalMetadata, error) {
	var metadata VaultJournalMetadata
	if len(data) > 1<<20 {
		return metadata, fmt.Errorf("journal metadata too large")
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		return metadata, err
	}
	metadata.DisplayName = strings.TrimSpace(metadata.DisplayName)
	if metadata.Format != vaultMetadataFormat || metadata.FormatVersion != vaultFormatVersion || metadata.DisplayName == "" || len(metadata.DisplayName) > 512 || metadata.UpdatedAt.IsZero() {
		return metadata, fmt.Errorf("invalid journal metadata")
	}
	if err := validateCloudJournalID(metadata.CloudJournalID); err != nil {
		return metadata, err
	}
	if err := validateVaultLocalDisplayTime(metadata.UpdatedAtLocal); err != nil {
		return metadata, err
	}
	return metadata, nil
}

func vaultLocalDisplayTime(value time.Time) string {
	return value.In(time.Local).Format(time.RFC3339Nano)
}

func validateVaultLocalDisplayTime(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil // Metadata written before this display field remains valid.
	}
	if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
		return fmt.Errorf("invalid Vault local display timestamp")
	}
	return nil
}

func newVaultRevisionID(now time.Time) (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	return vaultRevisionPrefix + now.UTC().Format(vaultRevisionLayout) + "-" + id.String(), nil
}

func validateVaultRevisionID(revisionID string) error {
	revisionID = strings.TrimSpace(revisionID)
	// UUID-only revision IDs were emitted by earlier releases and must remain
	// readable when a Vault is upgraded in place.
	if parsed, err := uuid.Parse(revisionID); err == nil && parsed != uuid.Nil {
		return nil
	}
	timestampLength := len(time.Now().UTC().Format(vaultRevisionLayout))
	prefixLength := len(vaultRevisionPrefix)
	if !strings.HasPrefix(revisionID, vaultRevisionPrefix) || len(revisionID) != prefixLength+timestampLength+1+36 || revisionID[prefixLength+timestampLength] != '-' {
		return fmt.Errorf("invalid Vault revision ID")
	}
	if _, err := time.Parse(vaultRevisionLayout, revisionID[prefixLength:prefixLength+timestampLength]); err != nil {
		return fmt.Errorf("invalid Vault revision timestamp")
	}
	parsed, err := uuid.Parse(revisionID[prefixLength+timestampLength+1:])
	if err != nil || parsed == uuid.Nil || parsed.Version() != 7 {
		return fmt.Errorf("invalid Vault revision UUID")
	}
	return nil
}
func validateVaultDescriptor(d VaultObjectDescriptor) error {
	if err := validateSHA256Digest(d.SHA256); err != nil {
		return err
	}
	if d.Size < 0 || strings.TrimSpace(d.Key) == "" || strings.Contains(d.Key, "..") {
		return fmt.Errorf("invalid Vault descriptor")
	}
	return nil
}
func bytesDescriptor(key string, data []byte) VaultObjectDescriptor {
	return VaultObjectDescriptor{Key: key, SHA256: digestBytes(data), Size: int64(len(data))}
}
func verifyObject(meta ObjectMeta, expected VaultObjectDescriptor) error {
	if meta.Size != expected.Size || meta.Digest != expected.SHA256 {
		return fmt.Errorf("digest_mismatch")
	}
	return nil
}
func readVaultObject(ctx context.Context, store VaultStore, provider VaultProvider, key string, expected VaultObjectDescriptor) ([]byte, error) {
	r, meta, err := store.GetObject(ctx, provider, key)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(r, expected.Size+1))
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if int64(len(data)) != expected.Size || digestBytes(data) != expected.SHA256 || verifyObject(meta, expected) != nil {
		return nil, fmt.Errorf("digest_mismatch")
	}
	return data, nil
}
func controlToken(data []byte) string   { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func bytesReader(data []byte) io.Reader { return bytes.NewReader(data) }

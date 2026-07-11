package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

type vaultStoreFactory func(*testing.T) (VaultStore, VaultProvider)

func runVaultStoreContract(t *testing.T, factory vaultStoreFactory) {
	t.Helper()
	store, provider := factory(t)
	ctx := context.Background()
	capabilities, err := store.Validate(ctx, provider)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !capabilities.ImmutableWrite || !capabilities.ConditionalWrite || !capabilities.ConditionalCreate {
		t.Fatalf("required capabilities missing: %#v", capabilities)
	}
	id := uuid.NewString()
	key, _ := vaultKey(id, "blobs/sha256/contract")
	data := []byte("contract bytes")
	if _, err := store.PutImmutable(ctx, provider, key, bytesReader(data), digestBytes(data)); err != nil {
		t.Fatalf("put immutable: %v", err)
	}
	if _, err := store.PutImmutable(ctx, provider, key, bytesReader(data), digestBytes(data)); err != nil {
		t.Fatalf("idempotent put: %v", err)
	}
	if _, err := store.PutImmutable(ctx, provider, key, bytesReader([]byte("different")), digestBytes([]byte("different"))); !isVault(err, VaultAlreadyExists) {
		t.Fatalf("collision error: %v", err)
	}
	control, _ := vaultCurrentKey(id)
	token, err := store.CreateControlIfAbsent(ctx, provider, control, []byte("one"))
	if err != nil {
		t.Fatalf("create control: %v", err)
	}
	if _, err := store.PutControlIf(ctx, provider, control, []byte("two"), "stale"); !isVault(err, VaultConflict) {
		t.Fatalf("stale control: %v", err)
	}
	if _, err := store.PutControlIf(ctx, provider, control, []byte("two"), token); err != nil {
		t.Fatalf("conditional update: %v", err)
	}
}

func TestFilesystemVaultStoreContract(t *testing.T) {
	runVaultStoreContract(t, func(t *testing.T) (VaultStore, VaultProvider) {
		return FilesystemVaultStore{}, VaultProvider{ID: "filesystem", Root: t.TempDir()}
	})
}

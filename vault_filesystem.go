package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/google/uuid"
)

// FilesystemVaultStore is the Phase 3 reference provider. It is suitable for
// deterministic integration tests and a deliberately configured local vault;
// production transports plug into the same VaultStore contract.
type FilesystemVaultStore struct{}

func (FilesystemVaultStore) path(provider VaultProvider, key string) (string, error) {
	root := strings.TrimSpace(provider.Root)
	if root == "" {
		return "", &VaultError{Kind: VaultUnavailable, Err: fmt.Errorf("provider root missing")}
	}
	key = strings.Trim(key, "/")
	if key == "" || strings.Contains(key, "..") {
		return "", &VaultError{Kind: VaultMalformed, Err: fmt.Errorf("invalid key")}
	}
	return filepath.Join(root, filepath.FromSlash(key)), nil
}
func (f FilesystemVaultStore) Validate(ctx context.Context, p VaultProvider) (VaultCapabilities, error) {
	if err := ctx.Err(); err != nil {
		return VaultCapabilities{}, err
	}
	if strings.TrimSpace(p.Root) == "" {
		return VaultCapabilities{}, &VaultError{Kind: VaultUnavailable, Err: fmt.Errorf("root required")}
	}
	if err := os.MkdirAll(p.Root, 0o700); err != nil {
		return VaultCapabilities{}, &VaultError{Kind: VaultUnavailable, Err: err}
	}
	probe := ".journal-probe/" + uuid.NewString()
	meta, err := f.PutImmutable(ctx, p, probe, strings.NewReader("probe"), digestBytes([]byte("probe")))
	if err != nil {
		return VaultCapabilities{}, err
	}
	if _, err := f.HeadObject(ctx, p, probe); err != nil {
		return VaultCapabilities{}, err
	}
	if err := f.DeleteImmutableIfVersion(ctx, p, probe, meta.Version); err != nil {
		return VaultCapabilities{}, err
	}
	return VaultCapabilities{ImmutableWrite: true, ConditionalWrite: true, ConditionalCreate: true, ReadAfterWrite: true, ObjectListing: true, ConditionalDelete: true}, nil
}
func (f FilesystemVaultStore) GetObject(ctx context.Context, p VaultProvider, key string) (io.ReadCloser, ObjectMeta, error) {
	if err := ctx.Err(); err != nil {
		return nil, ObjectMeta{}, err
	}
	path, err := f.path(p, key)
	if err != nil {
		return nil, ObjectMeta{}, err
	}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, ObjectMeta{}, &VaultError{Kind: VaultNotFound, Err: err}
	}
	if err != nil {
		return nil, ObjectMeta{}, &VaultError{Kind: VaultUnavailable, Err: err}
	}
	meta, err := f.HeadObject(ctx, p, key)
	if err != nil {
		_ = file.Close()
		return nil, ObjectMeta{}, err
	}
	return file, meta, nil
}
func (f FilesystemVaultStore) HeadObject(ctx context.Context, p VaultProvider, key string) (ObjectMeta, error) {
	if err := ctx.Err(); err != nil {
		return ObjectMeta{}, err
	}
	path, err := f.path(p, key)
	if err != nil {
		return ObjectMeta{}, err
	}
	digest, size, err := digestFile(path)
	if os.IsNotExist(err) {
		return ObjectMeta{}, &VaultError{Kind: VaultNotFound, Err: err}
	}
	if err != nil {
		return ObjectMeta{}, &VaultError{Kind: VaultUnavailable, Err: err}
	}
	return ObjectMeta{Key: key, Size: size, Digest: digest, Version: digest + fmt.Sprintf("-%d", size)}, nil
}
func (f FilesystemVaultStore) PutImmutable(ctx context.Context, p VaultProvider, key string, source io.Reader, expected string) (ObjectMeta, error) {
	if err := validateSHA256Digest(expected); err != nil {
		return ObjectMeta{}, err
	}
	path, err := f.path(p, key)
	if err != nil {
		return ObjectMeta{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return ObjectMeta{}, err
	}
	tmp := path + ".tmp-" + uuid.NewString()
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return ObjectMeta{}, err
	}
	_, copyErr := io.Copy(file, source)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(tmp)
		if copyErr != nil {
			return ObjectMeta{}, copyErr
		}
		return ObjectMeta{}, closeErr
	}
	digest, size, err := digestFile(tmp)
	if err != nil || digest != expected {
		_ = os.Remove(tmp)
		if err != nil {
			return ObjectMeta{}, err
		}
		return ObjectMeta{}, fmt.Errorf("digest_mismatch")
	}
	if err := os.Link(tmp, path); err != nil {
		_ = os.Remove(tmp)
		if os.IsExist(err) {
			meta, headErr := f.HeadObject(ctx, p, key)
			if headErr == nil && meta.Digest == expected {
				return meta, nil
			}
			return ObjectMeta{}, &VaultError{Kind: VaultAlreadyExists, Err: fmt.Errorf("immutable collision")}
		}
		return ObjectMeta{}, err
	}
	_ = os.Remove(tmp)
	return ObjectMeta{Key: key, Size: size, Digest: digest, Version: digest + fmt.Sprintf("-%d", size)}, nil
}
func (f FilesystemVaultStore) withControlLock(p VaultProvider, key string, fn func(string, []byte) error) error {
	path, err := f.path(p, key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	var data []byte
	if value, err := os.ReadFile(path); err == nil {
		data = value
	} else if !os.IsNotExist(err) {
		return err
	}
	return fn(path, data)
}
func (f FilesystemVaultStore) GetControl(ctx context.Context, p VaultProvider, key string) ([]byte, string, error) {
	r, _, err := f.GetObject(ctx, p, key)
	if err != nil {
		return nil, "", err
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, "", err
	}
	return data, controlToken(data), nil
}
func (f FilesystemVaultStore) CreateControlIfAbsent(ctx context.Context, p VaultProvider, key string, value []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var token string
	err := f.withControlLock(p, key, func(path string, current []byte) error {
		if current != nil {
			return &VaultError{Kind: VaultAlreadyExists, Err: fmt.Errorf("control exists")}
		}
		tmp := path + ".tmp-" + uuid.NewString()
		if err := os.WriteFile(tmp, value, 0o600); err != nil {
			return err
		}
		if err := os.Rename(tmp, path); err != nil {
			return err
		}
		token = controlToken(value)
		return nil
	})
	return token, err
}
func (f FilesystemVaultStore) PutControlIf(ctx context.Context, p VaultProvider, key string, value []byte, expected string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var token string
	err := f.withControlLock(p, key, func(path string, current []byte) error {
		if current == nil || controlToken(current) != expected {
			return &VaultError{Kind: VaultConflict, Err: fmt.Errorf("stale control token")}
		}
		tmp := path + ".tmp-" + uuid.NewString()
		if err := os.WriteFile(tmp, value, 0o600); err != nil {
			return err
		}
		if err := os.Rename(tmp, path); err != nil {
			return err
		}
		token = controlToken(value)
		return nil
	})
	return token, err
}
func (f FilesystemVaultStore) ListPrefix(ctx context.Context, p VaultProvider, prefix string) ([]ObjectMeta, error) {
	root, err := f.path(p, prefix)
	if err != nil {
		return nil, err
	}
	var metas []ObjectMeta
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() || strings.HasSuffix(d.Name(), ".lock") {
			return nil
		}
		relative, err := filepath.Rel(p.Root, path)
		if err != nil {
			return err
		}
		meta, err := f.HeadObject(ctx, p, filepath.ToSlash(relative))
		if err != nil {
			return err
		}
		metas = append(metas, meta)
		return nil
	})
	if os.IsNotExist(err) {
		return []ObjectMeta{}, nil
	}
	if err != nil {
		return nil, err
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].Key < metas[j].Key })
	return metas, nil
}
func (f FilesystemVaultStore) DeleteImmutableIfVersion(ctx context.Context, p VaultProvider, key, version string) error {
	meta, err := f.HeadObject(ctx, p, key)
	if err != nil {
		return err
	}
	if meta.Version != version {
		return &VaultError{Kind: VaultConflict, Err: fmt.Errorf("stale object version")}
	}
	path, err := f.path(p, key)
	if err != nil {
		return err
	}
	return os.Remove(path)
}

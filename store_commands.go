package main

import (
	"context"
	"fmt"
	"sync"
)

// StoreCommandRouter is the explicit-store command path used by cloud-aware
// callers. Existing Wails methods continue to call the local Commands facade
// until Phase 4 migrates their request contracts.
type StoreCommandRouter struct {
	stores   *JournalStoreRouter
	mu       sync.RWMutex
	services map[StoreID]*JournalService
}

func NewStoreCommandRouter(stores *JournalStoreRouter, local *JournalService) (*StoreCommandRouter, error) {
	if stores == nil || local == nil || local.StoreID() != LocalStoreID {
		return nil, fmt.Errorf("local store command service is required")
	}
	return &StoreCommandRouter{stores: stores, services: map[StoreID]*JournalService{LocalStoreID: local}}, nil
}

func (r *StoreCommandRouter) Register(service *JournalService) error {
	if service == nil || service.StoreKind() != StoreKindCloud {
		return fmt.Errorf("cloud Journal service is required")
	}
	if _, err := r.stores.Resolve(context.Background(), service.StoreID()); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.services[service.StoreID()]; exists {
		return fmt.Errorf("store command service already registered: %s", service.StoreID())
	}
	r.services[service.StoreID()] = service
	return nil
}

func (r *StoreCommandRouter) Resolve(ctx context.Context, storeID StoreID) (*JournalService, error) {
	if _, err := r.stores.Resolve(ctx, storeID); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	service, ok := r.services[storeID]
	if !ok {
		return nil, fmt.Errorf("store_not_open: %s", storeID)
	}
	return service, nil
}

func (r *StoreCommandRouter) GetTree(ctx context.Context, storeID StoreID) (TreeResponse, error) {
	service, err := r.Resolve(ctx, storeID)
	if err != nil {
		return TreeResponse{}, err
	}
	return service.GetLibraryTree()
}

func (r *StoreCommandRouter) CreateFolder(ctx context.Context, storeID StoreID, parentID, title string) (ItemResponse, error) {
	service, err := r.Resolve(ctx, storeID)
	if err != nil {
		return ItemResponse{}, err
	}
	return service.CreateFolder(parentID, title)
}

func (r *StoreCommandRouter) CreateDocument(ctx context.Context, storeID StoreID, parentID string) (DocumentResponse, error) {
	service, err := r.Resolve(ctx, storeID)
	if err != nil {
		return DocumentResponse{}, err
	}
	return service.CreateDocument(parentID)
}

func (r *StoreCommandRouter) OpenDocument(ctx context.Context, storeID StoreID, id string) (DocumentResponse, error) {
	service, err := r.Resolve(ctx, storeID)
	if err != nil {
		return DocumentResponse{}, err
	}
	return service.OpenDocument(id)
}

func (r *StoreCommandRouter) RenameItem(ctx context.Context, storeID StoreID, id, title string) (ItemResponse, error) {
	service, err := r.Resolve(ctx, storeID)
	if err != nil {
		return ItemResponse{}, err
	}
	return service.RenameItem(id, title)
}

func (r *StoreCommandRouter) MoveItem(ctx context.Context, storeID StoreID, id, parentID string, sortOrder int) (TreeResponse, error) {
	service, err := r.Resolve(ctx, storeID)
	if err != nil {
		return TreeResponse{}, err
	}
	return service.MoveItem(id, parentID, sortOrder)
}

func (r *StoreCommandRouter) TrashItem(ctx context.Context, storeID StoreID, command TrashItemCommand) (TreeResponse, error) {
	service, err := r.Resolve(ctx, storeID)
	if err != nil {
		return TreeResponse{}, err
	}
	return service.TrashItem(command)
}

func (r *StoreCommandRouter) Search(ctx context.Context, storeID StoreID, query string) (SearchResponse, error) {
	service, err := r.Resolve(ctx, storeID)
	if err != nil {
		return SearchResponse{}, err
	}
	return service.SearchLibrary(query)
}

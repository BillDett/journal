package main

import (
	"sort"
	"strings"
)

// Tree projection is kept separate from mutation commands so callers can
// reason about display ordering and aggregate counts independently.
func buildTree(items []rowItem, matches map[string]bool) []TreeItem {
	children := map[string][]rowItem{}
	var roots []rowItem
	for _, item := range items {
		if item.ParentID.Valid {
			children[item.ParentID.String] = append(children[item.ParentID.String], item)
		} else {
			roots = append(roots, item)
		}
	}
	var build func(rowItem) TreeItem
	build = func(item rowItem) TreeItem {
		node := treeItemFromRow(item)
		sortChildRows(children[item.ID])
		for _, child := range children[item.ID] {
			childNode := build(child)
			node.DocumentCount += childNode.DocumentCount
			node.ItemCount += childNode.ItemCount + 1
			node.Children = append(node.Children, childNode)
		}
		if item.Kind == KindDocument {
			node.DocumentCount = 1
		}
		return node
	}
	sortRootRows(roots)
	tree := make([]TreeItem, 0, len(roots))
	for _, root := range roots {
		tree = append(tree, build(root))
	}
	return tree
}

func sortRootRows(rows []rowItem) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].SystemKey.String == SystemTrash {
			return false
		}
		if rows[j].SystemKey.String == SystemTrash {
			return true
		}
		if rows[i].Kind == KindJournal && rows[j].Kind == KindJournal {
			if rows[i].SortOrder == rows[j].SortOrder {
				return strings.ToLower(rows[i].Title) < strings.ToLower(rows[j].Title)
			}
			return rows[i].SortOrder < rows[j].SortOrder
		}
		if rows[i].Kind == KindJournal {
			return true
		}
		if rows[j].Kind == KindJournal {
			return false
		}
		return updatedRowsLess(rows[i], rows[j])
	})
}

func sortChildRows(rows []rowItem) {
	sort.SliceStable(rows, func(i, j int) bool { return updatedRowsLess(rows[i], rows[j]) })
}
func updatedRowsLess(a, b rowItem) bool {
	if a.UpdatedAt == b.UpdatedAt {
		return strings.ToLower(a.Title) < strings.ToLower(b.Title)
	}
	return a.UpdatedAt > b.UpdatedAt
}

func treeItemFromRow(item rowItem) TreeItem {
	parentID, systemKey, encryptionKeyID := "", "", ""
	if item.ParentID.Valid {
		parentID = item.ParentID.String
	}
	if item.SystemKey.Valid {
		systemKey = item.SystemKey.String
	}
	if item.EncryptionKeyID.Valid {
		encryptionKeyID = item.EncryptionKeyID.String
	}
	encryptionState := item.EncryptionState
	if encryptionState == "" {
		encryptionState = EncryptionPlaintext
	}
	return TreeItem{ID: item.ID, ParentID: parentID, Kind: item.Kind, Title: item.Title, SortOrder: item.SortOrder, SystemKey: systemKey, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt, EncryptionState: encryptionState, EncryptionKeyID: encryptionKeyID, EncryptionLocked: item.EncryptionLocked, Children: []TreeItem{}}
}

func rowIsInTrash(items map[string]rowItem, id string, trashID string) bool {
	for id != "" {
		if id == trashID {
			return true
		}
		item, ok := items[id]
		if !ok || !item.ParentID.Valid {
			return false
		}
		id = item.ParentID.String
	}
	return false
}

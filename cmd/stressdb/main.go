package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

const (
	kindJournal  = "journal"
	kindFolder   = "folder"
	kindDocument = "document"
	systemTrash  = "trash"
)

type config struct {
	output          string
	journals        int
	minFolders      int
	maxFolders      int
	nestedPercent   int
	minDocuments    int
	maxDocuments    int
	minWords        int
	maxWords        int
	minWordLen      int
	maxWordLen      int
	seed            int64
	overwrite       bool
	paragraphWords  int
	autosaveMS      int
	reportEveryDocs int
}

type generator struct {
	cfg        config
	rng        *rand.Rand
	now        string
	folders    []string
	documents  int
	itemRows   int
	searchRows int
}

func main() {
	cfg := parseFlags()
	if err := validateConfig(cfg); err != nil {
		exitErr(err)
	}
	if err := generate(cfg); err != nil {
		exitErr(err)
	}
}

func parseFlags() config {
	cfg := config{}
	flag.StringVar(&cfg.output, "out", "journal-stress.db", "output SQLite database filename")
	flag.IntVar(&cfg.journals, "journals", 1, "number of journals to create")
	flag.IntVar(&cfg.minFolders, "min-folders", 0, "minimum folders per journal")
	flag.IntVar(&cfg.maxFolders, "max-folders", 25, "maximum folders per journal")
	flag.IntVar(&cfg.nestedPercent, "nested-percent", 35, "percentage of folders created under another folder")
	flag.IntVar(&cfg.minDocuments, "min-documents", 10, "minimum documents per journal")
	flag.IntVar(&cfg.maxDocuments, "max-documents", 100, "maximum documents per journal")
	flag.IntVar(&cfg.minWords, "min-words", 50, "minimum words per document")
	flag.IntVar(&cfg.maxWords, "max-words", 500, "maximum words per document")
	flag.IntVar(&cfg.minWordLen, "min-word-len", 3, "minimum characters per random word")
	flag.IntVar(&cfg.maxWordLen, "max-word-len", 12, "maximum characters per random word")
	flag.Int64Var(&cfg.seed, "seed", time.Now().UnixNano(), "random seed")
	flag.BoolVar(&cfg.overwrite, "overwrite", false, "replace an existing output file")
	flag.IntVar(&cfg.paragraphWords, "paragraph-words", 80, "target words per ProseMirror paragraph")
	flag.IntVar(&cfg.autosaveMS, "autosave-ms", 2000, "autosave_interval_ms setting value")
	flag.IntVar(&cfg.reportEveryDocs, "report-every-docs", 1000, "print progress after this many documents; 0 disables progress")
	flag.Parse()
	return cfg
}

func validateConfig(cfg config) error {
	if strings.TrimSpace(cfg.output) == "" {
		return errors.New("output filename is required")
	}
	if cfg.journals < 1 {
		return errors.New("journals must be at least 1")
	}
	if cfg.minFolders < 0 || cfg.maxFolders < cfg.minFolders {
		return errors.New("folder range must be non-negative and max-folders must be >= min-folders")
	}
	if cfg.nestedPercent < 0 || cfg.nestedPercent > 100 {
		return errors.New("nested-percent must be between 0 and 100")
	}
	if cfg.minDocuments < 0 || cfg.maxDocuments < cfg.minDocuments {
		return errors.New("document range must be non-negative and max-documents must be >= min-documents")
	}
	if cfg.minWords < 0 || cfg.maxWords < cfg.minWords {
		return errors.New("word range must be non-negative and max-words must be >= min-words")
	}
	if cfg.minWordLen < 1 || cfg.maxWordLen < cfg.minWordLen {
		return errors.New("word length range must be positive and max-word-len must be >= min-word-len")
	}
	if cfg.paragraphWords < 1 {
		return errors.New("paragraph-words must be at least 1")
	}
	if cfg.autosaveMS < 500 {
		return errors.New("autosave-ms must be at least 500")
	}
	return nil
}

func generate(cfg config) error {
	if err := prepareOutput(cfg.output, cfg.overwrite); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cfg.output), 0o755); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", cfg.output)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	gen := generator{
		cfg: cfg,
		rng: rand.New(rand.NewSource(cfg.seed)),
		now: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := gen.createSchema(db); err != nil {
		return err
	}
	if err := gen.populate(db); err != nil {
		return err
	}
	fmt.Printf("created %s with %d journals, %d folders, %d documents, seed %d\n", cfg.output, cfg.journals, len(gen.folders), gen.documents, cfg.seed)
	return nil
}

func prepareOutput(path string, overwrite bool) error {
	if _, err := os.Stat(path); err == nil {
		if !overwrite {
			return fmt.Errorf("%s already exists; pass -overwrite to replace it", path)
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		for _, suffix := range []string{"-wal", "-shm"} {
			if err := os.Remove(path + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (g *generator) createSchema(db *sql.DB) error {
	statements := []string{
		`PRAGMA foreign_keys = ON`,
		`CREATE TABLE items (
			id TEXT PRIMARY KEY,
			parent_id TEXT NULL REFERENCES items(id) ON DELETE CASCADE,
			kind TEXT NOT NULL CHECK (kind IN ('journal', 'folder', 'document')),
			title TEXT NOT NULL,
			sort_order INTEGER NOT NULL DEFAULT 0,
			system_key TEXT NULL UNIQUE,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE documents (
			item_id TEXT PRIMARY KEY REFERENCES items(id) ON DELETE CASCADE,
			schema_version INTEGER NOT NULL,
			content_json TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE VIRTUAL TABLE library_search_fts USING fts5(
			item_id UNINDEXED,
			kind UNINDEXED,
			title,
			body
		)`,
		`CREATE TABLE app_settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX idx_items_parent_sort ON items(parent_id, sort_order, title)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}

func (g *generator) populate(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer rollback(tx)

	if err := g.insertSettings(tx); err != nil {
		return err
	}
	if err := g.insertTrash(tx); err != nil {
		return err
	}
	for i := 0; i < g.cfg.journals; i++ {
		if err := g.insertJournal(tx, i); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (g *generator) insertSettings(tx *sql.Tx) error {
	settings := map[string]string{
		"autosave_interval_ms": fmt.Sprintf("%d", g.cfg.autosaveMS),
		"last_document_id":     "",
	}
	for key, value := range settings {
		if _, err := tx.Exec(
			`INSERT INTO app_settings (key, value, updated_at) VALUES (?, ?, ?)`,
			key, value, g.now,
		); err != nil {
			return err
		}
	}
	return nil
}

func (g *generator) insertTrash(tx *sql.Tx) error {
	id := uuid.NewString()
	if err := g.insertItem(tx, id, "", kindFolder, "Trash", 999999, systemTrash); err != nil {
		return err
	}
	return g.insertSearch(tx, id, kindFolder, "Trash", "")
}

func (g *generator) insertJournal(tx *sql.Tx, index int) error {
	journalID := uuid.NewString()
	title := fmt.Sprintf("Journal %04d", index+1)
	if err := g.insertItem(tx, journalID, "", kindJournal, title, index, ""); err != nil {
		return err
	}
	if err := g.insertSearch(tx, journalID, kindJournal, title, ""); err != nil {
		return err
	}

	containers := []string{journalID}
	sortOrders := map[string]int{journalID: 0}
	folderCount := g.randomInRange(g.cfg.minFolders, g.cfg.maxFolders)
	for i := 0; i < folderCount; i++ {
		parentID := journalID
		if len(containers) > 1 && g.rng.Intn(100) < g.cfg.nestedPercent {
			parentID = containers[1+g.rng.Intn(len(containers)-1)]
		}
		sortOrder := sortOrders[parentID]
		sortOrders[parentID]++
		id := uuid.NewString()
		title := fmt.Sprintf("Folder %04d-%05d", index+1, i+1)
		if err := g.insertItem(tx, id, parentID, kindFolder, title, sortOrder, ""); err != nil {
			return err
		}
		if err := g.insertSearch(tx, id, kindFolder, title, ""); err != nil {
			return err
		}
		containers = append(containers, id)
		g.folders = append(g.folders, id)
	}

	documentCount := g.randomInRange(g.cfg.minDocuments, g.cfg.maxDocuments)
	for i := 0; i < documentCount; i++ {
		parentID := containers[g.rng.Intn(len(containers))]
		sortOrder := sortOrders[parentID]
		sortOrders[parentID]++
		if err := g.insertDocument(tx, parentID, index, i, sortOrder); err != nil {
			return err
		}
	}
	return nil
}

func (g *generator) insertDocument(tx *sql.Tx, parentID string, journalIndex int, documentIndex int, sortOrder int) error {
	id := uuid.NewString()
	title := fmt.Sprintf("Document %04d-%06d", journalIndex+1, documentIndex+1)
	words := g.randomWords(g.randomInRange(g.cfg.minWords, g.cfg.maxWords))
	body := strings.Join(words, " ")
	content, err := json.Marshal(proseMirrorDoc(words, g.cfg.paragraphWords))
	if err != nil {
		return err
	}
	if err := g.insertItem(tx, id, parentID, kindDocument, title, sortOrder, ""); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO documents (item_id, schema_version, content_json, created_at, updated_at)
		 VALUES (?, 1, ?, ?, ?)`,
		id, string(content), g.now, g.now,
	); err != nil {
		return err
	}
	if err := g.insertSearch(tx, id, kindDocument, title, body); err != nil {
		return err
	}
	g.documents++
	if g.cfg.reportEveryDocs > 0 && g.documents%g.cfg.reportEveryDocs == 0 {
		fmt.Printf("inserted %d documents\n", g.documents)
	}
	return nil
}

func (g *generator) insertItem(tx *sql.Tx, id string, parentID string, kind string, title string, sortOrder int, systemKey string) error {
	var parent any
	if parentID != "" {
		parent = parentID
	}
	var key any
	if systemKey != "" {
		key = systemKey
	}
	_, err := tx.Exec(
		`INSERT INTO items (id, parent_id, kind, title, sort_order, system_key, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, parent, kind, title, sortOrder, key, g.now, g.now,
	)
	g.itemRows++
	return err
}

func (g *generator) insertSearch(tx *sql.Tx, id string, kind string, title string, body string) error {
	_, err := tx.Exec(
		`INSERT INTO library_search_fts (item_id, kind, title, body) VALUES (?, ?, ?, ?)`,
		id, kind, title, body,
	)
	g.searchRows++
	return err
}

func (g *generator) randomWords(count int) []string {
	words := make([]string, count)
	for i := range words {
		words[i] = g.randomWord(g.randomInRange(g.cfg.minWordLen, g.cfg.maxWordLen))
	}
	return words
}

func (g *generator) randomWord(length int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	var builder strings.Builder
	builder.Grow(length)
	for i := 0; i < length; i++ {
		builder.WriteByte(letters[g.rng.Intn(len(letters))])
	}
	return builder.String()
}

func (g *generator) randomInRange(min int, max int) int {
	if min == max {
		return min
	}
	return min + g.rng.Intn(max-min+1)
}

func proseMirrorDoc(words []string, paragraphWords int) map[string]any {
	content := []any{}
	for start := 0; start < len(words); start += paragraphWords {
		end := start + paragraphWords
		if end > len(words) {
			end = len(words)
		}
		content = append(content, map[string]any{
			"type": "paragraph",
			"content": []any{
				map[string]any{
					"type": "text",
					"text": strings.Join(words[start:end], " "),
				},
			},
		})
	}
	return map[string]any{
		"type":    "doc",
		"content": content,
	}
}

func rollback(tx *sql.Tx) {
	if tx != nil {
		_ = tx.Rollback()
	}
}

func exitErr(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

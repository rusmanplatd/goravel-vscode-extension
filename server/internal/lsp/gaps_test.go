package lsp

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rusmanplatd/goravel-lsp/internal/indexer"
)

func TestGapsLSPFeatures(t *testing.T) {
	root := t.TempDir()
	modelsDir := filepath.Join(root, "app", "models")
	if err := os.MkdirAll(modelsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	modelSrc := `package models

const UserOriginalTableName = "users"

type User struct {
	ID uint ` + "`gorm:\"column:id\"`" + `
	// @Description User email address
	// @Example alice@example.com
	Email string ` + "`gorm:\"column:email\"`" + `
}

func (User) TableName() string {
	return UserOriginalTableName
}

func (User) OriginalSchemaName() string {
	return "auth"
}
`
	modelPath := filepath.Join(modelsDir, "user.go")
	if err := os.WriteFile(modelPath, []byte(modelSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	s := New(&bytes.Buffer{}, &bytes.Buffer{})
	s.root = root
	s.reindex()
	s.cat.DBFunctions = append(s.cat.DBFunctions, &indexer.DBFunction{
		Schema: "auth", Name: "fn_normalize_email", Kind: "FUNCTION", ReturnType: "TEXT",
	})

	repoPath := filepath.Join(root, "repo.go")
	repoSrc := `package repo

import "app/models"

func f() {
	_ = models.UserOriginalTableName
	facades.DB().Exec("INSERT INTO users () VALUES ($1)")
	facades.DB().Select(&rows, "SELECT u.email FROM users u WHERE ")
}
`
	uri := pathToURI(repoPath)
	s.docs[uri] = repoSrc

	// 1. INSERT INTO users (<cursor>) narrows to users columns
	insOff := strings.Index(repoSrc, "INSERT INTO users (") + len("INSERT INTO users (")
	compIns, _ := json.Marshal(map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     offsetToPos(repoSrc, insOff),
	})
	resIns := s.completion(compIns).(map[string]any)
	itemsIns := resIns["items"].([]map[string]any)
	if len(itemsIns) != 2 {
		t.Fatalf("expected exactly 2 columns for INSERT INTO users (...), got %d", len(itemsIns))
	}

	// 2. Expanded SQL functions + DBFunctions in WHERE completion
	whereOff := strings.Index(repoSrc, "WHERE ") + len("WHERE ")
	compWhere, _ := json.Marshal(map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     offsetToPos(repoSrc, whereOff),
	})
	resWhere := s.completion(compWhere).(map[string]any)
	itemsWhere := resWhere["items"].([]map[string]any)
	hasJSONAgg, hasDBFn := false, false
	for _, it := range itemsWhere {
		if it["label"] == "JSON_AGG" {
			hasJSONAgg = true
		}
		if it["label"] == "fn_normalize_email" {
			hasDBFn = true
		}
	}
	if !hasJSONAgg || !hasDBFn {
		t.Fatalf("expected JSON_AGG (%v) and fn_normalize_email (%v) in completions", hasJSONAgg, hasDBFn)
	}

	// 3. Hover on u.email shows @Example and @Description
	emailOff := strings.Index(repoSrc, "u.email") + 3
	hovEmail, _ := json.Marshal(map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     offsetToPos(repoSrc, emailOff),
	})
	resHov := s.hover(hovEmail).(map[string]any)
	md := resHov["contents"].(map[string]any)["value"].(string)
	if !strings.Contains(md, "alice@example.com") {
		t.Fatalf("expected @Example in hover markdown, got:\n%s", md)
	}

	// 4. Hover & Definition on UserOriginalTableName
	constOff := strings.Index(repoSrc, "UserOriginalTableName") + 4
	hovConst, _ := json.Marshal(map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     offsetToPos(repoSrc, constOff),
	})
	resConstHov := s.hover(hovConst).(map[string]any)
	constMD := resConstHov["contents"].(map[string]any)["value"].(string)
	if !strings.Contains(constMD, "users") || !strings.Contains(constMD, "models.User") {
		t.Fatalf("expected constant hover to show table and model, got:\n%s", constMD)
	}
	locs := s.definition(hovConst).([]LocationT)
	if len(locs) == 0 || !strings.HasSuffix(locs[0].URI, "user.go") {
		t.Fatalf("expected definition to jump to user.go, got: %+v", locs)
	}

	// 5. Dynamic workspace/didChangeConfiguration toggles LiveDBEnabled
	prev := indexer.LiveDBEnabled
	defer func() { indexer.LiveDBEnabled = prev }()
	indexer.LiveDBEnabled = false
	cfgParams, _ := json.Marshal(map[string]any{
		"settings": map[string]any{
			"goravel": map[string]any{
				"sql": map[string]any{"liveIntrospection": true},
			},
		},
	})
	s.dispatch(&message{Method: "workspace/didChangeConfiguration", Params: cfgParams})
	if !indexer.LiveDBEnabled {
		t.Fatalf("expected LiveDBEnabled to become true after didChangeConfiguration")
	}
}

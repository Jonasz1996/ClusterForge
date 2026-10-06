package backups

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestSandboxCallers eist dat alleen de bewaakte functies in sandbox.go een
// VM terugzetten, verwijderen, de guest agent iets vragen of er cf-agent
// verify starten. Elke andere aanroep van Restore, Destroy, AgentInfo,
// AgentRunVerify of AgentExecStatus kan een productie-VM raken.
func TestSandboxCallers(t *testing.T) {
	root := filepath.Join("..", "..")
	guarded := map[string]bool{"Restore": true, "Destroy": true, "AgentInfo": true, "AgentRunVerify": true, "AgentExecStatus": true}
	allowed := map[string]bool{
		filepath.Join("internal", "backups", "sandbox.go"): true,
		// De definitie zelf en de nep-Proxmox van de tests.
		filepath.Join("internal", "proxmox", "client.go"): true,
	}
	found := 0
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "pvefake" || d.Name() == "gen" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !guarded[sel.Sel.Name] {
					return true
				}
				if !allowed[rel] {
					t.Errorf("%s roept %s aan; alleen de bewaakte functies in internal/backups/sandbox.go mogen dat", rel, sel.Sel.Name)
				}
				if rel == filepath.Join("internal", "backups", "sandbox.go") {
					found++
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if found < 5 {
		t.Fatalf("maar %d bewaakte aanroepen in sandbox.go gevonden; klopt de zoekopdracht nog?", found)
	}
}

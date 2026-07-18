package platform

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAsynqTasksUseBoundedUniquenessInsteadOfFixedTaskIDs(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	fset := token.NewFileSet()
	dispatchers := 0

	err := filepath.WalkDir(repoRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			name := entry.Name()
			if name == ".git" || name == "vendor" || name == "node_modules" || name == "web" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, obsoleteTable := range []string{
			"proxy_check_jobs", "proxy_check_job_items",
			"allocation_candidate_refresh_jobs", "admin_resource_bulk_commands",
			"mailmatch_fetch_jobs", "mailmatch_resource_fetch_jobs", "mailmatch_resource_fetch_requests",
			"mailmatch_project_history_scan_jobs", "microsoft_token_refresh_jobs", "microsoft_token_refresh_requests",
		} {
			if strings.Contains(string(source), obsoleteTable) {
				t.Errorf("obsolete async task table %q is forbidden: %s", obsoleteTable, path)
			}
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			packageName, packageOK := selector.X.(*ast.Ident)
			if packageOK && packageName.Name == "asynq" && selector.Sel.Name == "TaskID" {
				t.Errorf("fixed Asynq TaskID is forbidden: %s", fset.Position(call.Pos()))
			}
			if packageOK && packageName.Name == "asynq" && selector.Sel.Name == "Queue" && len(call.Args) > 0 {
				if literal, literalOK := call.Args[0].(*ast.BasicLit); literalOK && literal.Kind == token.STRING {
					t.Errorf("literal Asynq queue name is forbidden; use a platform queue constant: %s", fset.Position(call.Pos()))
				}
			}
			return true
		})
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			isDispatcher := strings.Contains(function.Name.Name, "Dispatcher") || strings.HasSuffix(function.Name.Name, "Dispatch")
			if !strings.HasPrefix(function.Name.Name, "Enqueue") || !isDispatcher {
				continue
			}
			dispatchers++
			options := map[string]bool{}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if ok {
					options[selector.Sel.Name] = true
				}
				return true
			})
			for _, required := range []string{"Unique", "Timeout", "Retention"} {
				if !options[required] {
					t.Errorf("%s must configure asynq.%s: %s", function.Name.Name, required, fset.Position(function.Pos()))
				}
			}
		}
		return nil
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, dispatchers, 9)
}

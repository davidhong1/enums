// Generates methods for enum-like types so they can implement JSON
// marshal/unmarshal and sql valuer/scanner interfaces.
// Borrows ideas and portions of code from github.com/campoy/jsonenums and
// golang.org/x/tools/cmd/stringer

package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/build"
	"go/constant"
	"go/format"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"html/template"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	var typeNames string
	flag.StringVar(&typeNames, "t", "", "comma-separated list of types")
	flag.Parse()
	if typeNames == "" {
		flag.Usage()
		os.Exit(1)
	}
	typeList := strings.Split(typeNames, ",")
	directory := "."
	if flag.NArg() > 0 {
		directory = flag.Arg(0)
	}
	if err := genEnums(typeList, directory); err != nil {
		log.Fatal(err)
	}
}

func genEnums(typeList []string, dir string) error {
	g := new(Generator)
	if err := g.parsePackageDir(dir); err != nil {
		return err
	}
	for _, typeName := range typeList {
		var analysis = struct {
			Command        string
			PackageName    string
			TypesAndValues map[string][]Value
		}{
			Command:        strings.Join(os.Args, " "),
			PackageName:    g.pkg.name,
			TypesAndValues: make(map[string][]Value),
		}
		g.valuesForType(typeName)
		analysis.TypesAndValues[typeName] = g.values
		var buf bytes.Buffer
		if err := genTemplate.Execute(&buf, analysis); err != nil {
			return err
		}
		src, err := format.Source(buf.Bytes())
		if err != nil {
			log.Printf("WARNING: formatting source: %v", err)
			src = buf.Bytes()
		}
		outputFile := filepath.Join(dir, strings.ToLower(typeName)+"_enum.go")
		if err := ioutil.WriteFile(outputFile, src, 0644); err != nil {
			return fmt.Errorf("writing output file %s: %v", outputFile, err)
		}
	}
	return nil
}

type Generator struct {
	pkg    *Package
	values []Value
}

type Package struct {
	name  string
	files []*ast.File
	defs  map[*ast.Ident]types.Object
}

func (g *Generator) parsePackageDir(dir string) error {
	pkg, err := build.Default.ImportDir(dir, 0)
	if err != nil {
		return fmt.Errorf("importing dir %s: %v", dir, err)
	}
	var names []string
	names = append(names, pkg.GoFiles...)
	names = append(names, pkg.CgoFiles...)
	names = append(names, pkg.SFiles...)
	prefixDirectory(dir, names)
	return g.parsePackage(dir, names)
}

func prefixDirectory(directory string, names []string) {
	if directory == "." {
		return
	}
	for i, name := range names {
		names[i] = filepath.Join(directory, name)
	}
}

func (g *Generator) parsePackage(dir string, names []string) error {
	var files []*ast.File
	fset := token.NewFileSet()
	for _, name := range names {
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			return fmt.Errorf("parsing file %s: %v", name, err)
		}
		files = append(files, f)
	}

	if len(files) == 0 {
		return fmt.Errorf("%s: no buildable Go files", dir)
	}

	defs := make(map[*ast.Ident]types.Object)
	config := types.Config{Importer: importer.ForCompiler(fset, "source", nil)}
	info := &types.Info{Defs: defs}
	if _, err := config.Check(dir, fset, files, info); err != nil {
		return fmt.Errorf("type-checking package: %v", err)
	}

	g.pkg = &Package{
		files[0].Name.Name,
		files,
		defs,
	}

	return nil
}

func (g *Generator) valuesForType(typeName string) {
	var values []Value
	for _, file := range g.pkg.files {
		ast.Inspect(file, func(n ast.Node) bool {
			decl, ok := n.(*ast.GenDecl)
			if !ok || decl.Tok != token.CONST {
				return true
			}
			var typ string
			for _, spec := range decl.Specs {
				vspec := spec.(*ast.ValueSpec)
				if vspec.Type == nil && len(vspec.Values) > 0 {
					typ = ""
					continue
				}
				if vspec.Type != nil {
					ident, ok := vspec.Type.(*ast.Ident)
					if !ok {
						continue
					}
					typ = ident.Name
				}
				if typ != typeName {
					continue
				}
				for _, name := range vspec.Names {
					if name.Name == "_" {
						continue
					}
					obj, ok := g.pkg.defs[name]
					if !ok {
						log.Fatalf("no value for constant %q", name)
					}
					info := obj.Type().Underlying().(*types.Basic).Info()
					if info&types.IsInteger == 0 {
						log.Fatalf("can't handle non-integer constant type %s", typ)
					}
					value := obj.(*types.Const).Val()
					if value.Kind() != constant.Int {
						log.Fatalf("can't handle non-integer constant value %s", name)
					}
					i64, isInt := constant.Int64Val(value)
					u64, isUint := constant.Uint64Val(value)
					if !isInt && !isUint {
						log.Fatalf("internal error: value of %s is not an integer: %s", name, value.String())
					}
					if !isInt {
						u64 = uint64(i64)
					}
					v := Value{
						Name:   name.Name,
						Value:  u64,
						Signed: info&types.IsUnsigned == 0,
						Str:    value.String(),
					}
					values = append(values, v)
				}
			}
			return false
		})
	}
	g.values = values
}

type Value struct {
	Name   string
	Value  uint64
	Signed bool
	Str    string
}

var genTemplate = template.Must(template.New("generated").Parse(`
// generated by {{.Command}}; DO NOT EDIT!

package {{.PackageName}}

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

{{range $typename, $values := .TypesAndValues}}

var (
	_{{$typename}}NameToValue = map[string]{{$typename}} {
		{{range $values}}"{{.Name}}": {{.Name}},
		{{end}}
	}

	_{{$typename}}ValueToName = map[{{$typename}}]string {
		{{range $values}}{{.Name}}: "{{.Name}}",
		{{end}}
	}
)

func init() {
	var v {{$typename}}
	if _, ok := interface{}(v).(fmt.Stringer); ok {
		_{{$typename}}NameToValue = map[string]{{$typename}} {
			{{range $values}}interface{}({{.Name}}).(fmt.Stringer).String(): {{.Name}},
			{{end}}
		}

		_{{$typename}}ValueToName = map[{{$typename}}]string {
			{{range $values}}{{.Name}}: interface{}({{.Name}}).(fmt.Stringer).String(),
			{{end}}
		}
	}
}

// Value implements the driver.Valuer interface (database/sql/driver) for
// converting to a value than can be stored in the database.
func (r {{$typename}}) Value() (driver.Value, error) {
	if v, ok := interface{}(r).(fmt.Stringer); ok {
		return v.String(), nil
	}
	return _{{$typename}}ValueToName[r], nil
}

// Scan implements the sql.Scanner interface for reading this enum from
// the database.
func (r *{{$typename}}) Scan(src interface{}) error {
	*r = 0
	val, ok := src.([]uint8)
	if !ok {
		return nil
	}
	s := string(val)
	v, ok := _{{$typename}}NameToValue[s]
	if !ok {
		return fmt.Errorf("invalid {{$typename}} %q", s)
	}
	*r = v
	return nil
}

// MarshalJSON is generated so {{$typename}} satisfies json.Marshaler.
func (r {{$typename}}) MarshalJSON() ([]byte, error) {
    if s, ok := interface{}(r).(fmt.Stringer); ok {
        return json.Marshal(s.String())
    }
    s, ok := _{{$typename}}ValueToName[r]
    if !ok {
        return nil, fmt.Errorf("invalid {{$typename}}: %d", r)
    }
    return json.Marshal(s)
}

// UnmarshalJSON is generated so {{$typename}} satisfies json.Unmarshaler.
func (r *{{$typename}}) UnmarshalJSON(data []byte) error {
    var s string
    if err := json.Unmarshal(data, &s); err != nil {
        return fmt.Errorf("{{$typename}} should be a string, got %s", data)
    }
    v, ok := _{{$typename}}NameToValue[s]
    if !ok {
        return fmt.Errorf("invalid {{$typename}} %q", s)
    }
    *r = v
    return nil
}

{{end}}
`))

package templates

import (
	"fmt"
	"regexp/syntax"
	"slices"
	"text/template/parse"
)

// De agent voert een command-stap uit met sh -c, en een pad bepaalt welk
// bestand root schrijft. Een waarde in run, unless, creates of een pad mag
// daarom geen tekens kunnen bevatten waarmee ze uit haar plek breekt. Dat
// geldt ook voor waarden die later uit Git komen. checkUnsafe weigert een
// template die in die velden iets gebruikt waarvan de vorm niet vastligt.

// safeTypes zijn parametertypes die na normaliseren alleen cijfers, letters,
// punten en een schuine streep van een prefix kunnen bevatten.
var safeTypes = map[string]bool{"int": true, "ipv4": true, "cidr": true, "bool": true, "size": true}

// safeNodeFields zijn de velden van een node die de server zelf controleert.
// De netwerkkaart komt uit de facts van de agent en hoort er niet bij.
var safeNodeFields = []string{"hostname", "role", "index", "address", "prefix"}

// checkUnsafe controleert de command-stappen en paden van alle rollen.
func (t *Template) checkUnsafe() error {
	for _, r := range t.Roles {
		for i, raw := range r.Steps {
			step, _ := raw.(map[string]any)
			for kind, fields := range map[string][]string{
				"command": {"run", "unless", "creates"}, "file": {"path"}, "directory": {"path"},
			} {
				m, _ := step[kind].(map[string]any)
				for _, f := range fields {
					s, _ := m[f].(string)
					if err := t.checkValue(s); err != nil {
						return fmt.Errorf("rol %s, stap %d, %s.%s: %w", r.Name, i+1, kind, f, err)
					}
				}
			}
		}
	}
	return nil
}

func (t *Template) checkValue(s string) error {
	tr := parse.New("waarde")
	tr.Mode = parse.SkipFuncCheck
	if _, err := tr.Parse(s, "{{", "}}", map[string]*parse.Tree{}); err != nil {
		return err
	}
	var err error
	walk(tr.Root, func(ident []string) {
		if err == nil {
			err = t.checkIdent(ident)
		}
	})
	return err
}

// checkIdent controleert één veldketen, zoals params.vip of, binnen een
// range over peers, address.
func (t *Template) checkIdent(ident []string) error {
	if len(ident) == 0 {
		return nil
	}
	switch ident[0] {
	case "params":
		if len(ident) < 2 {
			return fmt.Errorf("gebruik een parameter bij naam (.params.naam)")
		}
		i := slices.IndexFunc(t.Params, func(p Param) bool { return p.Name == ident[1] })
		if i < 0 {
			return fmt.Errorf("onbekende parameter %s", ident[1])
		}
		if p := t.Params[i]; !safeTypes[p.Type] && (p.Type != "string" || !safePattern(p.Pattern)) {
			return fmt.Errorf("parameter %s mag hier niet: gebruik type int, ipv4, cidr, bool of size, of een string met een pattern van alleen letters, cijfers en . _ - : @ , + =", p.Name)
		}
		return nil
	case "cluster":
		if len(ident) == 2 && (ident[1] == "slug" || ident[1] == "environment") {
			return nil
		}
		return fmt.Errorf("van het cluster mogen hier alleen slug en environment")
	case "node":
		if len(ident) == 2 && slices.Contains(safeNodeFields, ident[1]) {
			return nil
		}
		return fmt.Errorf("van een node mogen hier alleen hostname, role, index, address en prefix")
	case "nodes", "peers":
		if len(ident) == 1 {
			return nil
		}
	}
	// Binnen range over nodes of peers is de punt een node.
	if len(ident) == 1 && slices.Contains(safeNodeFields, ident[0]) {
		return nil
	}
	return fmt.Errorf("%s mag hier niet", joinIdent(ident))
}

func joinIdent(ident []string) string {
	s := ""
	for _, p := range ident {
		s += "." + p
	}
	return s
}

// walk roept fn aan voor elke veldketen in een sjabloon. Een variabele telt
// zonder haar naam: $.params.vip wordt params.vip, $p.address wordt address.
func walk(n parse.Node, fn func([]string)) {
	switch x := n.(type) {
	case *parse.ListNode:
		if x == nil {
			return
		}
		for _, c := range x.Nodes {
			walk(c, fn)
		}
	case *parse.ActionNode:
		walk(x.Pipe, fn)
	case *parse.PipeNode:
		if x == nil {
			return
		}
		for _, c := range x.Cmds {
			walk(c, fn)
		}
	case *parse.CommandNode:
		for _, a := range x.Args {
			walk(a, fn)
		}
	case *parse.FieldNode:
		fn(x.Ident)
	case *parse.DotNode:
		fn([]string{""})
	case *parse.VariableNode:
		if len(x.Ident) > 1 {
			fn(x.Ident[1:])
		}
	case *parse.ChainNode:
		// (iets).veld: alleen het veld is te zien; behandel het als onbekend.
		fn(append([]string{"(...)"}, x.Field...))
		walk(x.Node, fn)
	case *parse.IfNode:
		walkBranch(&x.BranchNode, fn)
	case *parse.RangeNode:
		walkBranch(&x.BranchNode, fn)
	case *parse.WithNode:
		walkBranch(&x.BranchNode, fn)
	case *parse.TemplateNode:
		fn([]string{"template " + x.Name})
	}
}

func walkBranch(b *parse.BranchNode, fn func([]string)) {
	walk(b.Pipe, fn)
	walk(b.List, fn)
	walk(b.ElseList, fn)
}

// safePattern is true als een pattern alleen letters, cijfers en . _ - : @ ,
// + = toelaat, dus geen spatie, aanhalingsteken, schuine streep of iets wat
// de shell leest.
func safePattern(pattern string) bool {
	if pattern == "" {
		return false
	}
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return false
	}
	return safeRegexp(re.Simplify())
}

func safeRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || slices.Contains([]rune("._-:@,+="), r)
}

func safeRegexp(re *syntax.Regexp) bool {
	switch re.Op {
	case syntax.OpLiteral:
		for _, r := range re.Rune {
			if !safeRune(r) {
				return false
			}
		}
	case syntax.OpCharClass:
		for i := 0; i+1 < len(re.Rune); i += 2 {
			if re.Rune[i+1]-re.Rune[i] > 128 {
				return false
			}
			for r := re.Rune[i]; r <= re.Rune[i+1]; r++ {
				if !safeRune(r) {
					return false
				}
			}
		}
	case syntax.OpAnyChar, syntax.OpAnyCharNotNL:
		return false
	}
	for _, s := range re.Sub {
		if !safeRegexp(s) {
			return false
		}
	}
	return true
}

package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/token"
	"go/types"
	"os"
	"path"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/MarcosTypeAP/go-p2p/internal/assert"
	"github.com/MarcosTypeAP/go-p2p/internal/style"
	"golang.org/x/tools/go/packages"
)

const packetKindType = "github.com/MarcosTypeAP/go-p2p.PacketKind"

func linesText(lines ...string) string {
	return strings.Join(lines, "\n")
}

func exitWithMsg(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}

type Scope byte

const (
	ScopeNone Scope = iota
	ScopeBuilder
	ScopeParser
	ScopeAll
)

func (s Scope) affects(scope Scope) bool {
	return s != ScopeNone && (s == ScopeAll || s == scope)
}

func (s *Scope) Set(value string) error {
	switch value {
	case "none":
		*s = ScopeNone
	case "builder":
		*s = ScopeBuilder
	case "parser":
		*s = ScopeParser
	case "all":
		*s = ScopeAll
	default:
		return fmt.Errorf("invalid scope: %q", value)
	}
	return nil
}

func (s Scope) String() string {
	switch s {
	case ScopeNone:
		return "none"
	case ScopeBuilder:
		return "builder"
	case ScopeParser:
		return "parser"
	case ScopeAll:
		return "all"
	default:
		panic("invalid scope")
	}
}

func (s Scope) Values() []string {
	return []string{"none", "builder", "parser", "all"}
}

type PacketFlags struct {
	*flag.FlagSet
	exclude      Scope
	needAck      bool
	registerName bool
}

func (f *PacketFlags) reset() {
	*f = PacketFlags{FlagSet: f.FlagSet, registerName: true}
}

var packetFlags PacketFlags

func init() {
	packetFlags.FlagSet = flag.NewFlagSet("", flag.ContinueOnError)
	packetFlags.SetOutput(flag.CommandLine.Output())
	packetFlags.Usage = func() {
		fmt.Fprint(packetFlags.Output(), linesText(
			style.Text("//p2p:packet [flags]", style.Underline),
			"",
			"Must be placed above or next to a packet kind declaration of type p2p.PacketKind.",
			"It registers a single variable (or group) as a packet kind and specifies their behavior.",
			"If it is placed on a declaration group, it applies to all declarations in the group.",
			"If it is placed on a single declaration, it overwrites the group directive.",
			"",
			style.Text("Flags:", style.Bold),
			"",
		))
		packetFlags.PrintDefaults()
		fmt.Fprint(packetFlags.Output(), linesText(
			"",
			style.Text("Example:", style.Bold),
			"  //p2p:packet -need-ack",
			"  const (",
			"      PacketA = p2p.PacketCustom + iota",
			"      PacketB //p2p:packet -exclude=builder",
			"      ...",
			"  )",
			"",
			"  const PacketC p2p.PacketKind = 69",
			"",
			style.Text("Final flags:", style.Bold),
			"  PacketA has -need-ack (from group directive)",
			"  PacketB has -exclude=builder (overwrites group directive)",
			"  PacketC has none (It is not recognized as a valid kind because it does not have the directive)",
			"",
			"",
		))
	}

	packetFlags.Var(&packetFlags.exclude, "exclude",
		"This indicates that no utility should be generated for this packet kind and the given `scope`.")

	packetFlags.BoolVar(&packetFlags.needAck, "need-ack", false,
		"This indicates that the receiver needs to send an acknowledgment that it received the packet.")

	packetFlags.BoolVar(&packetFlags.registerName, "register-name", true,
		"Indicates that names should be generated for the registered packet kinds.")
}

func parsePacketFlags(args []string) (exclude Scope, needAck bool, registerName bool, err error) {
	defer packetFlags.reset()
	if err := packetFlags.Parse(args); err != nil {
		return 0, false, false, err
	}
	return packetFlags.exclude, packetFlags.needAck, packetFlags.registerName, nil
}

type ExportType byte

const (
	FieldExported ExportType = iota
	FieldUnexported
	FieldAll
)

func (e ExportType) affects(name string) bool {
	switch e {
	case FieldAll:
		return true
	case FieldExported:
		return isExported(name)
	case FieldUnexported:
		return !isExported(name)
	default:
		panic("invalid export type")
	}
}

func (e *ExportType) Set(value string) error {
	switch value {
	case "exported":
		*e = FieldExported
	case "unexported":
		*e = FieldUnexported
	case "all":
		*e = FieldAll
	default:
		return fmt.Errorf("invalid scope: %q", value)
	}
	return nil
}

func (e ExportType) String() string {
	switch e {
	case FieldExported:
		return "exported"
	case FieldUnexported:
		return "unexported"
	case FieldAll:
		return "all"
	default:
		panic("invalid export type")
	}
}

func (e ExportType) Values() []string {
	return []string{"exported", "unexported", "all"}
}

type PayloadFlags struct {
	*flag.FlagSet
	useRef     Scope
	exclude    Scope
	alloc      bool
	exportType ExportType
}

func (f *PayloadFlags) reset() {
	*f = PayloadFlags{FlagSet: f.FlagSet}
}

var payloadFlags PayloadFlags

func init() {
	payloadFlags.FlagSet = flag.NewFlagSet("", flag.ContinueOnError)
	payloadFlags.SetOutput(flag.CommandLine.Output())
	payloadFlags.Usage = func() {
		fmt.Fprint(payloadFlags.Output(), linesText(
			style.Text("//p2p:payload PACKET_KIND [flags]", style.Underline),
			"",
			"Must be placed above a struct. It indicates that the struct below represents the payload format of the specified packet kind.",
			"By default, only a builder is generated for packets without this directive.",
			"",
			style.Text("Flags:", style.Bold),
			"",
		))
		payloadFlags.PrintDefaults()
		fmt.Fprint(payloadFlags.Output(), linesText(
			"",
			style.Text("Example:", style.Bold),
			"  //p2p:packet",
			"  const (",
			"      PacketPing = p2p.PacketCustom + iota",
			"      ...",
			"  )",
			"",
			"  //p2p:payload PacketPing -ref=parser",
			"  type packetPingPayload struct {",
			"      a int",
			"      b bool",
			"  }",
			"",
			style.Text("Generates:", style.Bold),
			"  func BuildPacketPing(buf []byte, payload packetPingPayload) p2p.Packet { ... }",
			"  func ParsePacketPing(packet p2p.Packet, payload *packetPingPayload) (err error) { ... }",
			"",
		))
	}

	payloadFlags.Var(&payloadFlags.useRef, "ref",
		"This indicates that the struct should be passed by reference to the generated utilities on the given `scope`. ")

	payloadFlags.BoolVar(&payloadFlags.alloc, "alloc", false,
		"This indicates that memory should be allocated for slices and maps.\n"+
			"It only affects parsers and it is always 'on' when -ref is not used.")

	payloadFlags.Var(&payloadFlags.exportType, "fields",
		"This filters which fields are used to generate the utilities given the `export-type`.")

	payloadFlags.Var(&payloadFlags.exclude, "exclude",
		"This indicates that no utility should be generated for this packet kind and the given `scope`. ")
}

func parsePayloadFlags(args []string) (useRef Scope, alloc bool, exportType ExportType, exclude Scope, err error) {
	defer payloadFlags.reset()
	if err := payloadFlags.Parse(args); err != nil {
		return 0, false, 0, 0, err
	}
	return payloadFlags.useRef, payloadFlags.alloc, payloadFlags.exportType, payloadFlags.exclude, nil
}

func extractDirectives(docs, comments *ast.CommentGroup) ([][]string, error) {
	var list [][]string

	if comments != nil {
		for _, comment := range comments.List {
			text := comment.Text[2:] // or /*
			if !strings.HasPrefix(text, "p2p:") {
				break
			}
			parts := strings.Split(strings.TrimSpace(text), " ")
			list = append(list, parts)
		}
	}

	if docs != nil {
		for _, comment := range slices.Backward(docs.List) {
			text := comment.Text[2:] // or /*
			if !strings.HasPrefix(text, "p2p:") {
				break
			}
			parts := strings.Split(strings.TrimSpace(text), " ")
			list = append(list, parts)
		}
	}

	for i := range len(list) - 1 {
		for j := i + 1; j < len(list); j++ {
			if list[i][0] == list[j][0] {
				return nil, fmt.Errorf("re-declared directive: %q", list[i][0])
			}
		}
	}

	return list, nil
}

func isExported(name string) bool {
	ch, _ := utf8.DecodeRuneInString(name)
	return unicode.IsUpper(ch)
}

func toPascalCase(s string) string {
	ch, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(ch)) + s[size:]
}

func toCamelCase(s string) string {
	ch, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToLower(ch)) + s[size:]
}

type Field struct {
	name string
	typ  types.Type
}

type PayloadInfo struct {
	typeName   string
	fields     []Field
	useRef     Scope
	alloc      bool
	exportType ExportType
	exclude    Scope
	line       int
}

type PacketInfo struct {
	kind         string
	payload      *PayloadInfo
	exclude      Scope
	needAck      bool
	registerName bool
}

type ListValue []string

func (l *ListValue) Set(value string) error {
	if strings.HasPrefix(value, `"`) || strings.HasSuffix(value, `"`) {
		return fmt.Errorf("value must not be surrounded by `\"`")
	}
	*l = strings.Split(value, ",")
	return nil
}

func (l ListValue) String() string {
	return fmt.Sprint([]string(l))
}

func main() {
	var structsPrefix string
	flag.StringVar(&structsPrefix, "prefix", "", linesText(
		"Generate utilities for structs that have the prefix followed by packet kind.",
		"eg: prefix=Payload and kind=PacketPing matches: type PayloadPacketPing struct { ... }",
	))
	var prefixPayloadFlags ListValue
	flag.Var(&prefixPayloadFlags, "payload-flags",
		"Comma-separated list of payload directive `flags` passed to structs matched by prefix.",
	)
	var registerNames bool
	flag.BoolVar(&registerNames, "register-names", true,
		"Indicates that names should be generated for the registered packet kinds.",
	)
	var genFilepath string
	flag.StringVar(&genFilepath, "path", "packets",
		"Indicates the file path of the generated code, it will be suffixed with '_gen.go'.\n"+
			"(absolute or relative to the file where //go:generate was placed)")

	flag.Usage = func() {
		fmt.Fprint(flag.CommandLine.Output(), linesText(
			"Genpackets generates builders and parsers for the specified packet kinds.",
			"It looks for comment directives to generate packet utilities.",
			"",
			style.Text("Usage:", style.Bold),
			"",
			"  genpackets [flags]",
			"",
			style.Text("Flags:", style.Bold),
			"",
		))
		flag.PrintDefaults()
		fmt.Fprint(flag.CommandLine.Output(), linesText(
			"",
			style.Text("Directives:", style.Bold),
			"",
		))
		packetFlags.Usage()
		payloadFlags.Usage()
		fmt.Fprint(flag.CommandLine.Output(), linesText(
			"",
			style.Text("Types:", style.Bold),
			"",
		))

		scopeValues := strings.Join(new(Scope).Values(), " | ")
		exportTypeValues := strings.Join(new(ExportType).Values(), " | ")
		fmt.Fprint(flag.CommandLine.Output(), linesText(
			"  Scope = "+scopeValues,
			"  Export-type = "+exportTypeValues,
			"",
		))
	}

	flag.Parse()

	cwd, err := os.Getwd()
	assert.NoError(err)

	sourceFilename := os.Getenv("GOFILE")
	assert.NotEqual(sourceFilename, "")
	assert.True(strings.HasSuffix(sourceFilename, ".go"), sourceFilename)

	sourcePackage := os.Getenv("GOPACKAGE")
	assert.NotEqual(sourcePackage, "")

	sourcePath := path.Join(cwd, sourceFilename)
	if !path.IsAbs(genFilepath) {
		genFilepath = path.Join(cwd, genFilepath)
	}
	genFilepath += "_gen.go"

	fileSet := token.NewFileSet()
	pkgConfig := packages.Config{
		Mode: 0 |
			packages.NeedTypes |
			packages.NeedTypesInfo |
			packages.NeedDeps |
			packages.NeedImports |
			packages.NeedFiles |
			packages.NeedName |
			packages.NeedSyntax,
		Fset: fileSet,
	}
	pkgs, err := packages.Load(&pkgConfig, sourcePath)
	if err != nil {
		exitWithMsg(fmt.Sprintf("Error loading source code: %v", err))
	}
	assert.Equal(len(pkgs), 1)
	pkg := pkgs[0]

	newPkgErrors := make([]packages.Error, 0, len(pkg.Errors))
	for _, err := range pkg.Errors {
		if err.Kind == packages.TypeError {
			continue
		}
		newPkgErrors = append(newPkgErrors, err)
	}
	pkg.Errors = newPkgErrors
	if packages.PrintErrors([]*packages.Package{pkg}) > 0 {
		os.Exit(1)
	}

	assert.Equal(len(pkg.Syntax), 1)
	syntaxFile := pkg.Syntax[0]

	getLine := func(node ast.Node) int {
		return pkg.Fset.Position(node.Pos()).Line
	}

	filterPacketDirectives := func(directives [][]string) [][]string {
		return slices.DeleteFunc(directives, func(dir []string) bool {
			return dir[0] != "p2p:packet"
		})
	}
	filterPayloadDirectives := func(directives [][]string) [][]string {
		return slices.DeleteFunc(directives, func(dir []string) bool {
			return dir[0] != "p2p:payload"
		})
	}

	p2pPkg, ok := pkg.Imports["github.com/MarcosTypeAP/go-p2p"]
	assert.True(ok)
	imports := map[string]*packages.Package{
		"github.com/MarcosTypeAP/go-p2p": p2pPkg,
	}

	type Kind = string
	packets := map[Kind]PacketInfo{}
	packetsOrder := []Kind{}
	prefixMatchedPayloads := map[Kind]PayloadInfo{}

	parsePayloadStruct := func(typeSpec *ast.TypeSpec, structType *ast.StructType, payloadDirective []string) (Kind, PayloadInfo) {
		payloadInfo := PayloadInfo{
			typeName: typeSpec.Name.Name,
			line:     getLine(structType),
		}

		if typeSpec.TypeParams != nil {
			exitWithMsg(fmt.Sprintf("Error parsing directives: line %d: Generics are not supported", getLine(typeSpec)))
		}

		if len(payloadDirective) == 1 {
			exitWithMsg(fmt.Sprintf("Error parsing directives: line %d: missing packet kind", getLine(typeSpec)))
		}

		packetKind := payloadDirective[1]

		payloadInfo.useRef, payloadInfo.alloc, payloadInfo.exportType, payloadInfo.exclude, err = parsePayloadFlags(payloadDirective[2:])
		if err != nil {
			exitWithMsg(fmt.Sprintf("Error parsing directives: line %d: %v", getLine(typeSpec), err))
		}

		for _, field := range structType.Fields.List {
			// Check for tags

			typ := pkg.TypesInfo.ObjectOf(field.Names[0]).Type()

			if !isSupported(typ.Underlying()) {
				exitWithMsg(fmt.Sprintf("Error parsing directives: line %d: unsupported type", getLine(field)))
			}

			if named, ok := typ.(*types.Named); ok {
				pkg_ := named.Obj().Pkg()
				if pkg_.Name() != "main" {
					imports[pkg_.Path()] = pkg.Imports[pkg_.Path()]
				}
			}

			payloadInfo.fields = append(payloadInfo.fields, Field{
				name: field.Names[0].Name,
				typ:  typ,
			})
		}

		return packetKind, payloadInfo
	}

	for _, decl := range syntaxFile.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}

		switch genDecl.Tok {
		case token.CONST, token.VAR:
			valueSpec := genDecl.Specs[0].(*ast.ValueSpec)

			groupDirectives, err := extractDirectives(genDecl.Doc, nil)
			if err != nil {
				exitWithMsg(fmt.Sprintf("Error parsing group directives: line %d: %v", getLine(valueSpec), err))
			}
			groupDirectives = filterPacketDirectives(groupDirectives)

			for _, spec := range genDecl.Specs {
				valueSpec := spec.(*ast.ValueSpec)
				directives, err := extractDirectives(valueSpec.Doc, valueSpec.Comment)
				if err != nil {
					line := fileSet.Position(valueSpec.Pos()).Line
					exitWithMsg(fmt.Sprintf("Error parsing directives: line %d: %v", line, err))
				}
				directives = filterPacketDirectives(directives)

			OuterLoop:
				for _, groupDir := range groupDirectives {
					for _, dir := range directives {
						if dir[0] == groupDir[0] {
							continue OuterLoop
						}
					}
					directives = append(directives, groupDir)
				}

				if len(directives) == 0 {
					continue
				}
				assert.LessEqual(len(directives), 1, "for now there is only support for p2p:packet")
				packetDirective := directives[0]

				packetKind := valueSpec.Names[0].Name
				valueType := pkg.TypesInfo.ObjectOf(valueSpec.Names[0]).Type().(*types.Named)

				if valueType.String() != packetKindType {
					exitWithMsg(fmt.Sprintf(
						"Error parsing directives: line %d: p2p:packet directive placed on a declaration that is not p2p.PacketKind: %q is type %q",
						getLine(valueSpec),
						packetKind,
						valueType.String(),
					))
				}

				packetInfo := packets[packetKind]
				if packetInfo.kind != "" {
					exitWithMsg(fmt.Sprintf("Error parsing directives: line %d: packet kind already registered: %q", getLine(valueSpec), packetKind))
				}
				packetInfo.kind = packetKind

				packetInfo.exclude, packetInfo.needAck, packetInfo.registerName, err = parsePacketFlags(packetDirective[1:])
				if err != nil {
					exitWithMsg(fmt.Sprintf("Error parsing directives: line %d: %v", getLine(valueSpec), err))
				}

				packets[packetKind] = packetInfo
				packetsOrder = append(packetsOrder, packetKind)
			}

		case token.TYPE:
			typeSpec := genDecl.Specs[0].(*ast.TypeSpec)

			structType, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				continue
			}

			directives, err := extractDirectives(genDecl.Doc, nil)
			if err != nil {
				exitWithMsg(fmt.Sprintf("Error parsing directives: line %d: %v", getLine(typeSpec), err))
			}
			directives = filterPayloadDirectives(directives)

			if len(directives) > 0 {
				assert.LessEqual(len(directives), 1, "for now there is only support for p2p:payload")
				payloadDirective := directives[0]

				packetKind, payloadInfo := parsePayloadStruct(typeSpec, structType, payloadDirective)

				packetInfo := packets[packetKind]
				if packetInfo.payload != nil {
					exitWithMsg(fmt.Sprintf("Error parsing directives: line %d: re-declared payload for packet kind %q", getLine(structType), packetKind))
				}
				packetInfo.payload = &payloadInfo
				packets[packetKind] = packetInfo

			} else {
				if structsPrefix == "" {
					continue
				}

				packetKind, found := strings.CutPrefix(typeSpec.Name.Name, structsPrefix)
				if !found || packetKind == "" {
					continue
				}

				directive := []string{"p2p:payload", packetKind}
				directive = append(directive, []string(prefixPayloadFlags)...)
				packetKind, payloadInfo := parsePayloadStruct(typeSpec, structType, directive)

				if _, ok := prefixMatchedPayloads[packetKind]; ok {
					exitWithMsg(fmt.Sprintf("Error parsing directives: line %d: re-declared payload for packet kind %q", getLine(structType), packetKind))
				}
				prefixMatchedPayloads[packetKind] = payloadInfo
			}
		}
	}

	for kind, packet := range packets {
		if packet.kind == "" {
			assert.NotNil(packet.payload)
			exitWithMsg(fmt.Sprintf("Error parsing directives: line %d: %q was not declared as packet kind", packet.payload.line, kind))
		}
	}
	for kind, payload := range prefixMatchedPayloads {
		packet, ok := packets[kind]
		if !ok {
			exitWithMsg(fmt.Sprintf("Error parsing directives: line %d: %q was not declared as packet kind", payload.line, kind))
		}
		if packet.payload != nil {
			exitWithMsg(fmt.Sprintf("Error parsing directives: line %d: re-declared payload for packet kind %q", max(payload.line, packet.payload.line), kind))
		}
		packet.payload = &payload
		packets[kind] = packet
	}

	content := &bytes.Buffer{}

	content.WriteString("// Code generated by \"go run github.com/MarcosTypeAP/go-p2p/cmd/genpackets " + strings.Join(os.Args[1:], " ") + "\"; DO NOT EDIT.\n\n")
	content.WriteString("package " + sourcePackage)
	content.WriteString("\n\n")

	content.WriteString("import (\n")
	for _, pkg := range imports {
		content.WriteString(pkg.Name + " \"" + pkg.PkgPath + "\"\n")
	}
	content.WriteString(`"fmt"` + "\n")
	content.WriteString(")\n\n")

	if registerNames {
		content.WriteString("func init() {\n")
		for _, kind := range packetsOrder {
			packet := packets[kind]
			if !packet.registerName {
				continue
			}
			content.WriteString("p2p.RegisterPacketKindName(" + kind + ", \"" + kind + "\")\n")
		}
		content.WriteString("}\n\n")
	}

	for _, packetKind := range packetsOrder {
		packet, ok := packets[packetKind]
		assert.True(ok)

		if !packet.exclude.affects(ScopeBuilder) {
			withPayload := packet.payload != nil && !packet.payload.exclude.affects(ScopeBuilder)
			useRef := packet.payload != nil && packet.payload.useRef.affects(ScopeBuilder)

			content.WriteString("// Builder for packet kind " + packetKind + "\n")
			content.WriteString("func Build" + toPascalCase(packetKind) + "(buf []byte")

			if withPayload {
				if useRef {
					content.WriteString(", payload *" + packet.payload.typeName)
				} else {
					content.WriteString(", payload " + packet.payload.typeName)
				}
			}

			content.WriteString(") p2p.Packet {\n")

			flags := "p2p.FlagNone"
			if packet.needAck {
				flags = "p2p.FlagNeedAck"
			}

			if withPayload {
				fmt.Fprintf(content, "builder := p2p.NewPacketBuilder(buf, %s, %s)\n", packetKind, flags)
				for _, field := range packet.payload.fields {
					if !packet.payload.exportType.affects(field.name) {
						continue
					}
					content.WriteString("\n// Write payload." + field.name + " of type " + typeToString(field.typ) + "\n")
					content.WriteString(getBuildFieldStatement(field.typ, "payload."+field.name))
					content.WriteString("\n")
				}
				content.WriteString("return builder.Build()\n")
			} else {
				fmt.Fprintf(content, "return p2p.NewPacketBuilder(buf, %s, %s).Build()\n", packetKind, flags)
			}

			content.WriteString("}\n\n")
		}

		if !packet.exclude.affects(ScopeParser) {
			withPayload := packet.payload != nil && !packet.payload.exclude.affects(ScopeParser)
			useRef := packet.payload.useRef.affects(ScopeParser)
			alloc := !useRef || packet.payload.alloc

			content.WriteString("// Parser for packet kind " + packetKind + "\n")
			content.WriteString("//\n// Panics if the given packet's kind is not " + packetKind + "\n")
			content.WriteString("func Parse" + toPascalCase(packetKind) + "(packet p2p.Packet")

			if withPayload {
				if useRef {
					content.WriteString(", payload *" + packet.payload.typeName + ") (err error) {\n")
				} else {
					content.WriteString(") (payload " + packet.payload.typeName + ", err error) {\n")
				}
			} else {
				content.WriteString(") error {\n")
			}

			content.WriteString(
				"if packet.Kind() != " + packetKind + " {" +
					`panic("wrong packet's kind: want ` + packetKind + `, got " + packet.Kind().String())` +
					"}\n\n")

			if withPayload {
				content.WriteString("parser := p2p.NewPacketParser(packet)\n")
				content.WriteString("var length uint16; _ = length\n")
				for _, field := range packet.payload.fields {
					if !packet.payload.exportType.affects(field.name) {
						continue
					}
					content.WriteString("\n// Parse payload." + field.name + " of type " + typeToString(field.typ) + "\n")
					content.WriteString(getParseFieldStatement(field.typ, "payload."+field.name, alloc))
					content.WriteString("\n")
				}
				content.WriteString("err = parser.Err(); return")
			} else {
				content.WriteString("return p2p.NewPacketParser(packet).Err()\n")
			}

			content.WriteString("}\n\n")
		}
	}

	finalContent, err := format.Source(content.Bytes())
	assert.NoError(err, "formatting generated file")

	// Or just use os.Chmod
	if err := os.Remove(genFilepath); err != nil && !errors.Is(err, os.ErrNotExist) {
		exitWithMsg(fmt.Sprintf("Error deleting old generated code file: %v", err))
	}
	genFile, err := os.OpenFile(genFilepath, os.O_WRONLY|os.O_CREATE, 0o400)
	if err != nil {
		exitWithMsg(fmt.Sprintf("Error creating the generated code file: %v", err))
	}
	defer func() {
		_ = genFile.Close()
	}()

	n, err := genFile.Write(finalContent)
	if err != nil {
		exitWithMsg(fmt.Sprintf("Error writing to the generated code file: %v", err))
	}
	assert.Equal(n, len(finalContent))
	genFile.Truncate(int64(n))
}

var basicKindToString = [...]string{
	types.Bool:    "Bool",
	types.Int:     "Int",
	types.Int8:    "Int8",
	types.Int16:   "Int16",
	types.Int32:   "Int32",
	types.Int64:   "Int64",
	types.Uint:    "Uint",
	types.Uint8:   "Uint8",
	types.Uint16:  "Uint16",
	types.Uint32:  "Uint32",
	types.Uint64:  "Uint64",
	types.Float32: "Float32",
	types.Float64: "Float64",
	types.String:  "String",

	types.UntypedBool:   "Bool",
	types.UntypedInt:    "Int",
	types.UntypedRune:   "Rune",
	types.UntypedFloat:  "Float",
	types.UntypedString: "String",
}

func getBuildFieldStatement(typ types.Type, varName string) string {
	return _getBuildFieldStatement(typ, varName, true)
}

func _getBuildFieldStatement(typ types.Type, varName string, useUnexported bool) string {
	switch t := typ.(type) {
	case *types.Basic:
		switch t.Kind() {
		case types.Bool, types.UntypedBool,
			types.Int, types.Int8, types.Int16, types.Int32, types.Int64, types.UntypedInt,
			types.Uint, types.Uint8, types.Uint16, types.Uint32, types.Uint64,
			types.Float32, types.Float64, types.UntypedFloat,
			types.String, types.UntypedString,
			types.UntypedRune:
			return "builder.Write" + basicKindToString[t.Kind()] + "(" + varName + ")"
		}

	case *types.Named:
		useUnexported = t.Obj().Pkg().Name() == "main"
		underType := t.Underlying()
		if _, ok := underType.(*types.Basic); ok {
			return _getBuildFieldStatement(underType, typeToString(underType)+"("+varName+")", useUnexported)
		}
		return _getBuildFieldStatement(underType, varName, useUnexported)

	case *types.Alias:
		return _getBuildFieldStatement(t.Underlying(), varName, false)

	case *types.Pointer:
		return _getBuildFieldStatement(t.Elem(), "*"+varName, false)

	case *types.Array:
		stmt := _getBuildFieldStatement(t.Elem(), varName+"[i]", false)
		return "for i := range " + fmt.Sprint(t.Len()) + " { " + stmt + " }"

	case *types.Slice:
		stmt := _getBuildFieldStatement(t.Elem(), varName+"[i]", false)
		return "builder.WriteUint16(uint16(len(" + varName + "))); for i := range " + varName + " { " + stmt + " }"

	case *types.Map:
		keyStmt := _getBuildFieldStatement(t.Key(), "k", false)
		valueStmt := _getBuildFieldStatement(t.Elem(), "v", false)
		return "builder.WriteUint16(uint16(len(" + varName + "))); for k, v := range " + varName + " { " + keyStmt + "; " + valueStmt + " }"

	case *types.Struct:
		str := strings.Builder{}
		for field := range t.Fields() {
			if !useUnexported && !field.Exported() {
				continue
			}
			str.WriteString(_getBuildFieldStatement(field.Type(), varName+"."+field.Name(), useUnexported) + ";")
		}
		return str.String()
	}

	panic(fmt.Errorf("unsupported type: %v", strings.TrimPrefix(typ.String(), "types.")))
}

func getParseFieldStatement(typ types.Type, varName string, alloc bool) string {
	return _getParseFieldStatement(typ, varName, "=", alloc, true)
}

func _getParseFieldStatement(typ types.Type, varName string, equal string, alloc bool, useUnexported bool) string {
	switch t := typ.(type) {
	case *types.Basic:
		switch t.Kind() {
		case types.Bool, types.UntypedBool,
			types.Int, types.Int8, types.Int16, types.Int32, types.Int64, types.UntypedInt,
			types.Uint, types.Uint8, types.Uint16, types.Uint32, types.Uint64,
			types.Float32, types.Float64, types.UntypedFloat,
			types.String, types.UntypedString,
			types.UntypedRune:
			return varName + " " + equal + " parser.Parse" + basicKindToString[t.Kind()] + "()"
		}

	case *types.Named:
		useUnexported = t.Obj().Pkg().Name() == "main"

		if basicType, ok := t.Underlying().(*types.Basic); ok {
			stmt := _getParseFieldStatement(basicType, varName, equal, alloc, useUnexported)
			return strings.Replace(stmt, equal, equal+typeToString(t)+"(", 1) + ")"
		}
		return _getParseFieldStatement(t.Underlying(), varName, equal, alloc, useUnexported)

	case *types.Alias:
		return _getParseFieldStatement(t.Rhs(), varName, equal, alloc, false)

	case *types.Pointer:
		if isSupported(t.Elem()) {
			return _getParseFieldStatement(t.Elem(), "*"+varName, equal, alloc, false)
		}

	case *types.Array:
		stmt := _getParseFieldStatement(t.Elem(), varName+"[i]", equal, alloc, false)
		return "for i := range " + fmt.Sprint(t.Len()) + " { " + stmt + " }"

	case *types.Slice:
		length := "length = parser.ParseUint16()"
		var parse string
		if basicType, ok := t.Elem().(*types.Basic); ok && basicType.Kind() == types.Byte {
			parse = "copy(" + varName + ", parser.GetBytes(int(length)))"
		} else {
			stmt := _getParseFieldStatement(t.Elem(), varName+"[i]", equal, alloc, false)
			parse = "for i := range length { " + stmt + " }"
		}
		if alloc {
			return length + ";" + varName + " = make(" + typeToString(t) + ", length); " + parse
		}
		err := `fmt.Errorf("\"` + varName + `\" does not have enough capacity: got %d, needed %d", cap(` + varName + `), length)`
		check := "if cap(" + varName + ") < int(length) { parser.SetErr(" + err + ") }"
		return length + ";" + check + " else { " + varName + " = " + varName + "[:length]; " + parse + "}"

	case *types.Map:
		keyStmt := _getParseFieldStatement(t.Key(), "k", ":=", alloc, false)
		valueStmt := _getParseFieldStatement(t.Elem(), "v", ":=", alloc, false)
		parse := "length = parser.ParseUint16(); for range length { " + keyStmt + "; " + valueStmt + "; " + varName + "[k] = v }"
		if alloc {
			return varName + " = make(" + typeToString(t) + ", length); " + parse
		}
		return "clear(" + varName + "); " + parse

	case *types.Struct:
		str := strings.Builder{}
		for field := range t.Fields() {
			if !useUnexported && !field.Exported() {
				continue
			}
			str.WriteString(_getParseFieldStatement(field.Type(), varName+"."+field.Name(), "=", alloc, useUnexported) + ";")
		}
		return str.String()
	}

	panic(fmt.Errorf("unsupported type: %v", strings.TrimPrefix(typ.String(), "types.")))
}

func isSupported(typ types.Type) bool {
	switch t := typ.(type) {
	case *types.Basic:
	case *types.Named:
		return isSupported(t.Underlying())
	case *types.Alias:
		return isSupported(t.Underlying())
	case *types.Pointer:
		return isSupported(t.Elem())
	case *types.Array:
		return isSupported(t.Elem())
	case *types.Slice:
		return isSupported(t.Elem())
	case *types.Map:
		return isSupported(t.Key()) && isSupported(t.Elem())
	case *types.Struct:
		for field := range t.Fields() {
			if !isSupported(field.Type()) {
				return false
			}
		}
	default:
		return false
	}
	return true
}

func typeToString(typ types.Type) string {
	switch t := typ.(type) {
	case *types.Basic:
		return t.Name()

	case *types.Named:
		if t.Obj().Pkg().Name() == "main" {
			return t.Obj().Name()
		} else {
			return t.Obj().Pkg().Name() + "." + t.Obj().Name()
		}

	case *types.Alias:
		return t.Obj().Name()

	case *types.Pointer:
		return "*" + typeToString(t.Elem())

	case *types.Array:
		return fmt.Sprintf("[%d]", t.Len()) + typeToString(t.Elem())

	case *types.Slice:
		return "[]" + typeToString(t.Elem())

	case *types.Map:
		return "map[" + typeToString(t.Key()) + "]" + typeToString(t.Elem())

	case *types.Struct:
		if t.NumFields() == 0 {
			return "struct{}"
		}

		str := strings.Builder{}
		str.WriteString("struct{")
		for i := range t.NumFields() {
			if i > 0 {
				str.WriteString("; ")
			}
			field := t.Field(i)
			if field.Embedded() {
				str.WriteString(field.Name())
			} else {
				str.WriteString(field.Name() + " " + typeToString(field.Type()))
			}
		}
		str.WriteString("}")
		return str.String()
	}

	panic(fmt.Errorf("type not implemented: %v", typ))
}

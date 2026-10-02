// protoc-gen-temporalvalidate generates typed Server validators from rule symbols.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"go.temporal.io/api/temporalvalidate/rules"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"
)

const outputFile = "temporalvalidate/validation/validator_gen.go"

type function struct {
	name   string
	field  *protogen.Field
	active bool
}

func main() {
	descriptor := flag.String("descriptor-set", "", "generate from a descriptor set instead of protoc stdin")
	check := flag.Bool("check", false, "fail if generated validators are stale")
	flag.Parse()
	if *descriptor != "" {
		if err := runDescriptor(*descriptor, *check); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	protogen.Options{}.Run(generate)
}

func generate(plugin *protogen.Plugin) error {
	plugin.SupportedFeatures = uint64(pluginpb.CodeGeneratorResponse_FEATURE_PROTO3_OPTIONAL)
	options := map[string]protowire.Number{}
	for _, file := range plugin.Files {
		for _, extension := range file.Extensions {
			options[string(extension.Desc.FullName())] = protowire.Number(extension.Desc.Number())
		}
	}
	files := make([]protoreflect.FileDescriptor, 0, len(plugin.Files))
	if options["temporalvalidate.v1.rule"] == 0 {
		return errors.New("temporalvalidate.v1.rule is not defined")
	}
	for _, file := range plugin.Files {
		files = append(files, file.Desc)
	}
	catalog, err := rules.New(files)
	if err != nil {
		return err
	}
	functions := map[string]*function{}
	var visit func(*protogen.Message) error
	visit = func(message *protogen.Message) error {
		for _, field := range message.Fields {
			selected, err := catalog.Lookup(field.Desc)
			if err != nil {
				return err
			}
			for _, rule := range selected {
				functions[rule.Function] = &function{name: rule.Function, field: field}
			}
		}
		for _, child := range message.Messages {
			if err := visit(child); err != nil {
				return err
			}
		}
		return nil
	}
	for _, file := range plugin.Files {
		for _, message := range file.Messages {
			if err := visit(message); err != nil {
				return err
			}
		}
	}
	visited := map[protoreflect.FullName]bool{}
	audited := map[protoreflect.FullName]bool{}
	var auditCanonical func(*protogen.Message) error
	auditCanonical = func(message *protogen.Message) error {
		if audited[message.Desc.FullName()] {
			return nil
		}
		audited[message.Desc.FullName()] = true
		for _, field := range message.Fields {
			if err := catalog.Canonical(field.Desc); err != nil {
				return err
			}
			nested, err := catalog.AuditsChildren(field.Desc)
			if err != nil {
				return err
			}
			if nested && field.Message != nil {
				child := field.Message
				if field.Desc.IsMap() {
					child = field.Message.Fields[1].Message
				}
				if child != nil {
					if err := auditCanonical(child); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	var enroll func(*protogen.Message) error
	enroll = func(message *protogen.Message) error {
		if visited[message.Desc.FullName()] {
			return nil
		}
		visited[message.Desc.FullName()] = true
		for _, field := range message.Fields {
			selected, err := catalog.Lookup(field.Desc)
			if err != nil {
				return err
			}
			for _, rule := range selected {
				functions[rule.Function].active = true
			}
			if field.Message != nil {
				if err := enroll(field.Message); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, file := range plugin.Files {
		if !file.Generate || !strings.HasPrefix(file.Desc.Path(), "temporal/api/") {
			continue
		}
		for _, service := range file.Services {
			for _, method := range service.Methods {
				enabled, err := optionEnabled(method.Desc.Options(), options["temporalvalidate.v1.rpc_validation"])
				if err != nil {
					return err
				}
				if !enabled {
					continue
				}
				if method.Desc.IsStreamingClient() || method.Desc.IsStreamingServer() {
					return fmt.Errorf("%s: validation only supports unary RPCs", method.Desc.FullName())
				}
				for _, message := range []*protogen.Message{method.Input, method.Output} {
					if err := auditCanonical(message); err != nil {
						return err
					}
					if err := enroll(message); err != nil {
						return err
					}
				}
			}
		}
	}
	var names []string
	for name, function := range functions {
		if function.active {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	emit(plugin, functions, names)
	return nil
}

func optionBytes(message proto.Message, number protowire.Number) ([][]byte, error) {
	data, err := proto.Marshal(message)
	if err != nil {
		return nil, err
	}
	var values [][]byte
	for len(data) != 0 {
		tag, kind, length := protowire.ConsumeTag(data)
		if length < 0 {
			return nil, protowire.ParseError(length)
		}
		data = data[length:]
		if tag == number {
			if kind != protowire.BytesType {
				return nil, fmt.Errorf("option %d must be length-delimited", number)
			}
			value, length := protowire.ConsumeBytes(data)
			if length < 0 {
				return nil, protowire.ParseError(length)
			}
			values = append(values, value)
		}
		length = protowire.ConsumeFieldValue(tag, kind, data)
		if length < 0 {
			return nil, protowire.ParseError(length)
		}
		data = data[length:]
	}
	return values, nil
}

func optionEnabled(message proto.Message, number protowire.Number) (bool, error) {
	values, err := optionBytes(message, number)
	if err != nil || len(values) == 0 {
		return false, err
	}
	data := values[0]
	for len(data) != 0 {
		tag, kind, length := protowire.ConsumeTag(data)
		if length < 0 {
			return false, protowire.ParseError(length)
		}
		data = data[length:]
		if tag == 1 && kind == protowire.VarintType {
			value, length := protowire.ConsumeVarint(data)
			if length < 0 {
				return false, protowire.ParseError(length)
			}
			return value != 0, nil
		}
		length = protowire.ConsumeFieldValue(tag, kind, data)
		if length < 0 {
			return false, protowire.ParseError(length)
		}
		data = data[length:]
	}
	return false, nil
}

func emit(plugin *protogen.Plugin, functions map[string]*function, names []string) {
	g := plugin.NewGeneratedFile(outputFile, "go.temporal.io/api/temporalvalidate/validation")
	g.P("// Code generated by protoc-gen-temporalvalidate. DO NOT EDIT.")
	g.P("package validation")
	g.P("// Validator is the implementation contract for functions used by enrolled RPCs.")
	g.P("type Validator[C any] interface {")
	for _, name := range names {
		g.P(name, "(C, ", goType(g, functions[name].field), ") error")
	}
	g.P("}")
	reflectPackage := protogen.GoImportPath("google.golang.org/protobuf/reflect/protoreflect")
	fmtPackage := protogen.GoImportPath("fmt")
	protoPackage := protogen.GoImportPath("google.golang.org/protobuf/proto")
	g.P("// Check detects stale generated code and incompatible annotations at startup.")
	g.P("func Check(name string, field ", reflectPackage.Ident("FieldDescriptor"), ") error {")
	g.P("switch name {")
	for _, name := range names {
		field := functions[name].field
		kindName := strings.ToUpper(field.Desc.Kind().String()[:1]) + field.Desc.Kind().String()[1:] + "Kind"
		g.P("case ", fmt.Sprintf("%q", name), ":")
		g.P("if field.IsList() || field.IsMap() || field.Kind() != ", reflectPackage.Ident(kindName), " { return ", fmtPackage.Ident("Errorf"), "(\"%s does not support field %s\", name, field.FullName()) }")
		if field.Message != nil {
			g.P("if field.Message().FullName() != ", fmt.Sprintf("%q", field.Message.Desc.FullName()), " { return ", fmtPackage.Ident("Errorf"), "(\"%s requires ", field.Message.Desc.FullName(), "\", name) }")
		}
		if field.Enum != nil {
			g.P("if field.Enum().FullName() != ", fmt.Sprintf("%q", field.Enum.Desc.FullName()), " { return ", fmtPackage.Ident("Errorf"), "(\"%s requires ", field.Enum.Desc.FullName(), "\", name) }")
		}
		g.P("return nil")
	}
	g.P("default: return ", fmtPackage.Ident("Errorf"), "(\"unknown validation function %q; regenerate API-Go\", name)")
	g.P("} }")
	g.P("// Invoke calls the inferred method without a manual registration map.")
	g.P("func Invoke[C any](validator Validator[C], ctx C, name string, field ", reflectPackage.Ident("FieldDescriptor"), ", value ", reflectPackage.Ident("Value"), ") error {")
	g.P("if err := Check(name, field); err != nil { return err }")
	g.P("switch name {")
	for _, name := range names {
		field := functions[name].field
		g.P("case ", fmt.Sprintf("%q", name), ":")
		if field.Message != nil {
			typ := g.QualifiedGoIdent(field.Message.GoIdent)
			g.P("if !value.Message().IsValid() { return validator.", name, "(ctx, nil) }")
			g.P("typed, ok := value.Message().Interface().(*", typ, ")")
			g.P("if !ok { typed = &", typ, "{}; ", protoPackage.Ident("Merge"), "(typed, value.Message().Interface()) }")
			g.P("return validator.", name, "(ctx, typed)")
		} else {
			g.P("return validator.", name, "(ctx, ", goValue(g, field), ")")
		}
	}
	g.P("default: return ", fmtPackage.Ident("Errorf"), "(\"unknown validation function %q\", name)")
	g.P("} }")
}

func goType(g *protogen.GeneratedFile, field *protogen.Field) string {
	if field.Message != nil {
		return "*" + g.QualifiedGoIdent(field.Message.GoIdent)
	}
	if field.Enum != nil {
		return g.QualifiedGoIdent(field.Enum.GoIdent)
	}
	switch field.Desc.Kind() {
	case protoreflect.StringKind:
		return "string"
	case protoreflect.BytesKind:
		return "[]byte"
	case protoreflect.BoolKind:
		return "bool"
	case protoreflect.FloatKind:
		return "float32"
	case protoreflect.DoubleKind:
		return "float64"
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return "int32"
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return "int64"
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return "uint32"
	default:
		return "uint64"
	}
}

func goValue(g *protogen.GeneratedFile, field *protogen.Field) string {
	switch field.Desc.Kind() {
	case protoreflect.StringKind:
		return "value.String()"
	case protoreflect.BytesKind:
		return "value.Bytes()"
	case protoreflect.BoolKind:
		return "value.Bool()"
	case protoreflect.FloatKind:
		return "float32(value.Float())"
	case protoreflect.DoubleKind:
		return "value.Float()"
	case protoreflect.EnumKind:
		return goType(g, field) + "(value.Enum())"
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return "int32(value.Int())"
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return "value.Int()"
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return "uint32(value.Uint())"
	default:
		return "value.Uint()"
	}
}

func runDescriptor(path string, check bool) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	set := &descriptorpb.FileDescriptorSet{}
	if err := proto.Unmarshal(data, set); err != nil {
		return err
	}
	request := &pluginpb.CodeGeneratorRequest{ProtoFile: set.File}
	for _, file := range set.File {
		if strings.HasPrefix(file.GetName(), "temporal/api/") || strings.HasPrefix(file.GetName(), "temporalvalidate/v1/") {
			request.FileToGenerate = append(request.FileToGenerate, file.GetName())
		}
	}
	plugin, err := (protogen.Options{}).New(request)
	if err != nil {
		return err
	}
	if err := generate(plugin); err != nil {
		return err
	}
	response := plugin.Response()
	if response.GetError() != "" {
		return errors.New(response.GetError())
	}
	generated := []byte(response.File[0].GetContent())
	if check {
		current, err := os.ReadFile(outputFile)
		if err != nil {
			return err
		}
		if !bytes.Equal(current, generated) {
			return fmt.Errorf("%s is stale; run make validation-functions", outputFile)
		}
		return nil
	}
	if err := os.MkdirAll("temporalvalidate/validation", 0755); err != nil {
		return err
	}
	return os.WriteFile(outputFile, generated, 0644)
}

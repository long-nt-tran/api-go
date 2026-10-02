package main

import (
	"bytes"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os/exec"
	"strings"
	"testing"

	temporalvalidatepb "go.temporal.io/api/temporalvalidate/v1"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"
)

func fixture(enabled bool) *pluginpb.CodeGeneratorRequest {
	annotated := func(name string, number protowire.Number, typ descriptorpb.FieldDescriptorProto_Type) *descriptorpb.FieldDescriptorProto {
		options := &descriptorpb.FieldOptions{}
		options.ProtoReflect().SetUnknown(protowire.AppendVarint(protowire.AppendTag(nil, number, protowire.VarintType), 1))
		return &descriptorpb.FieldDescriptorProto{Name: proto.String(name), Number: proto.Int32(1), Type: typ.Enum(), Options: options}
	}
	options := &descriptorpb.MethodOptions{}
	if enabled {
		proto.SetExtension(options, temporalvalidatepb.E_RpcValidation, &temporalvalidatepb.RPCValidation{Enabled: proto.Bool(true)})
	}
	file := &descriptorpb.FileDescriptorProto{
		Name: proto.String("temporal/api/test/v1/service.proto"), Package: proto.String("test"), Syntax: proto.String("proto3"),
		Options:    &descriptorpb.FileOptions{GoPackage: proto.String("example.com/test;test")},
		Dependency: []string{"temporalvalidate/v1/annotations.proto", "temporalvalidate/v1/rules.proto"},
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("Request"), Field: []*descriptorpb.FieldDescriptorProto{annotated("namespace", 901001, descriptorpb.FieldDescriptorProto_TYPE_STRING)}},
			{Name: proto.String("Response"), Field: []*descriptorpb.FieldDescriptorProto{annotated("count", 901100, descriptorpb.FieldDescriptorProto_TYPE_INT32)}},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{{Name: proto.String("Service"), Method: []*descriptorpb.MethodDescriptorProto{{Name: proto.String("Call"), InputType: proto.String(".test.Request"), OutputType: proto.String(".test.Response"), Options: options}}}},
	}
	rulesFile := protodesc.ToFileDescriptorProto(temporalvalidatepb.File_temporalvalidate_v1_rules_proto)
	count := &descriptorpb.FieldDescriptorProto{Name: proto.String("count"), Number: proto.Int32(901100), Extendee: proto.String(".google.protobuf.FieldOptions"), Type: descriptorpb.FieldDescriptorProto_TYPE_BOOL.Enum(), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Options: &descriptorpb.FieldOptions{}}
	proto.SetExtension(count.Options, temporalvalidatepb.E_Rule, &temporalvalidatepb.Rule{Function: proto.String("ValidateCount"), FieldType: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum()})
	rulesFile.Extension = append(rulesFile.Extension, count)
	return &pluginpb.CodeGeneratorRequest{ProtoFile: []*descriptorpb.FileDescriptorProto{
		protodesc.ToFileDescriptorProto(descriptorpb.File_google_protobuf_descriptor_proto),
		protodesc.ToFileDescriptorProto(temporalvalidatepb.File_temporalvalidate_v1_annotations_proto), file, rulesFile,
	}, FileToGenerate: []string{file.GetName()}}
}

func TestSymbolAndCanonicalGuards(t *testing.T) {
	for _, scenario := range []string{"missing canonical", "alternate symbol", "exemption", "empty exemption", "unenrolled", "duplicate canonical", "duplicate alias", "invalid alias", "missing metadata", "false symbol", "wrong wire type", "multiple rules"} {
		t.Run(scenario, func(t *testing.T) {
			request := fixture(true)
			service := request.ProtoFile[2]
			field := service.MessageType[0].Field[0]
			catalog := request.ProtoFile[3]
			extra := proto.Clone(catalog.Extension[7]).(*descriptorpb.FieldDescriptorProto)
			extra.Name = proto.String("alternate")
			extra.Number = proto.Int32(901101)
			definition := &temporalvalidatepb.Rule{Function: proto.String("ValidateAlternate"), FieldType: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()}
			proto.SetExtension(extra.Options, temporalvalidatepb.E_Rule, definition)
			expectError := true
			switch scenario {
			case "missing canonical":
				field.Options = nil
			case "alternate symbol":
				catalog.Extension = append(catalog.Extension, extra)
				field.Options = &descriptorpb.FieldOptions{}
				field.Options.ProtoReflect().SetUnknown(protowire.AppendVarint(protowire.AppendTag(nil, 901101, protowire.VarintType), 1))
			case "exemption":
				field.Options = &descriptorpb.FieldOptions{}
				proto.SetExtension(field.Options, temporalvalidatepb.E_CanonicalRuleIgnored, "different semantics")
				expectError = false
			case "empty exemption":
				field.Options = &descriptorpb.FieldOptions{}
				proto.SetExtension(field.Options, temporalvalidatepb.E_CanonicalRuleIgnored, " ")
			case "unenrolled":
				field.Options = nil
				service.Service[0].Method[0].Options = nil
				expectError = false
			case "duplicate canonical":
				definition.CanonicalFieldNames = []string{"namespace"}
				proto.SetExtension(extra.Options, temporalvalidatepb.E_Rule, definition)
				catalog.Extension = append(catalog.Extension, extra)
			case "duplicate alias":
				definition.CanonicalFieldNames = []string{"foo", "foo"}
				proto.SetExtension(extra.Options, temporalvalidatepb.E_Rule, definition)
				catalog.Extension = append(catalog.Extension, extra)
			case "invalid alias":
				definition.CanonicalFieldNames = []string{"bad-name"}
				proto.SetExtension(extra.Options, temporalvalidatepb.E_Rule, definition)
				catalog.Extension = append(catalog.Extension, extra)
			case "missing metadata":
				extra.Options = nil
				catalog.Extension = append(catalog.Extension, extra)
			case "false symbol":
				field.Options.ProtoReflect().SetUnknown(protowire.AppendVarint(protowire.AppendTag(nil, 901001, protowire.VarintType), 0))
			case "wrong wire type":
				field.Options.ProtoReflect().SetUnknown(protowire.AppendBytes(protowire.AppendTag(nil, 901001, protowire.BytesType), []byte("true")))
			case "multiple rules":
				catalog.Extension = append(catalog.Extension, extra)
				field.Options.ProtoReflect().SetUnknown(append(field.Options.ProtoReflect().GetUnknown(), protowire.AppendVarint(protowire.AppendTag(nil, 901101, protowire.VarintType), 1)...))
				expectError = false
			}
			content, err := generated(request)
			if (err != nil) != expectError {
				t.Fatalf("%s: %v", scenario, err)
			}
			if scenario == "multiple rules" && !strings.Contains(content, "ValidateAlternate(C, string)") {
				t.Fatal(content)
			}
		})
	}
}

func TestWrongServerSignatureDoesNotCompile(t *testing.T) {
	content, err := generated(fixture(true))
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(content, "type Validator[")
	end := start + strings.Index(content[start:], "\n}") + 2
	source := "package test\n" + content[start:end] + "\ntype implementation struct{}\nfunc(implementation) ValidateNamespace(int,int32)error{return nil}\nfunc(implementation) ValidateCount(int,int32)error{return nil}\nvar _ Validator[int] = implementation{}"
	set := token.NewFileSet()
	file, err := parser.ParseFile(set, "contract.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = new(types.Config).Check("test", set, []*ast.File{file}, nil); err == nil {
		t.Fatal("wrong signature compiled")
	}
}

func TestProtocProtocol(t *testing.T) {
	request, err := proto.Marshal(fixture(true))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "run", ".")
	command.Stdin = bytes.NewReader(request)
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	response := &pluginpb.CodeGeneratorResponse{}
	if err := proto.Unmarshal(output, response); err != nil {
		t.Fatal(err)
	}
	if response.GetError() != "" {
		t.Fatal(response.GetError())
	}
	if len(response.File) != 1 || response.File[0].GetName() != outputFile {
		t.Fatal(response)
	}
	if !strings.Contains(response.File[0].GetContent(), "ValidateNamespace(C, string)") {
		t.Fatal(response)
	}
}

func generated(request *pluginpb.CodeGeneratorRequest) (string, error) {
	plugin, err := (protogen.Options{}).New(request)
	if err != nil {
		return "", err
	}
	if err := generate(plugin); err != nil {
		return "", err
	}
	response := plugin.Response()
	if response.GetError() != "" {
		return "", errors.New(response.GetError())
	}
	return response.File[0].GetContent(), nil
}

func TestOneOptionCollectsRequestAndResponseFunctions(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		content, err := generated(fixture(enabled))
		if err != nil {
			t.Fatal(err)
		}
		for _, signature := range []string{"ValidateNamespace(C, string)", "ValidateCount(C, int32)"} {
			if strings.Contains(content, signature) != enabled {
				t.Fatal(content)
			}
		}
	}
}

func TestInvalidFunctions(t *testing.T) {
	for _, scenario := range []string{"name", "conflict", "repeated", "stream"} {
		t.Run(scenario, func(t *testing.T) {
			request := fixture(true)
			file := request.ProtoFile[2]
			field := file.MessageType[1].Field[0]
			switch scenario {
			case "name":
				proto.SetExtension(request.ProtoFile[3].Extension[7].Options, temporalvalidatepb.E_Rule, &temporalvalidatepb.Rule{Function: proto.String("validateCount"), FieldType: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum()})
			case "conflict":
				proto.SetExtension(request.ProtoFile[3].Extension[7].Options, temporalvalidatepb.E_Rule, &temporalvalidatepb.Rule{Function: proto.String("ValidateNamespace"), FieldType: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum()})
			case "repeated":
				field.Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
			case "stream":
				file.Service[0].Method[0].ServerStreaming = proto.Bool(true)
			}
			if _, err := generated(request); err == nil {
				t.Fatal("expected generation failure")
			}
		})
	}
}

func TestNestedFunctionAndExactMessageTypes(t *testing.T) {
	request := fixture(true)
	file := request.ProtoFile[2]
	field := file.MessageType[1].Field[0]
	field.Type = descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum()
	field.TypeName = proto.String(".test.Response")
	proto.SetExtension(request.ProtoFile[3].Extension[7].Options, temporalvalidatepb.E_Rule, &temporalvalidatepb.Rule{Function: proto.String("ValidateMessage"), FieldType: descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(), TypeName: proto.String("test.Request")})
	if _, err := generated(request); err == nil || !strings.Contains(err.Error(), "singular message test.Request") {
		t.Fatalf("got %v", err)
	}
	proto.SetExtension(request.ProtoFile[3].Extension[7].Options, temporalvalidatepb.E_Rule, &temporalvalidatepb.Rule{Function: proto.String("ValidateMessage"), FieldType: descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(), TypeName: proto.String("test.Response")})
	file.MessageType[0].Field[0].Options = nil
	file.MessageType[0].Field[0].Name = proto.String("children")
	file.MessageType[0].Field[0].Type = descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum()
	file.MessageType[0].Field[0].TypeName = proto.String(".test.Response")
	file.MessageType[0].Field[0].Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
	content, err := generated(request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "ValidateMessage(C, *test.Response)") {
		t.Fatal(content)
	}
}

func TestMissingMethodDoesNotCompile(t *testing.T) {
	content, err := generated(fixture(true))
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(content, "type Validator[")
	end := start + strings.Index(content[start:], "\n}") + 2
	contract := content[start:end]
	for _, complete := range []bool{false, true} {
		source := "package test\n" + contract + "\ntype implementation struct{}\nfunc(implementation) ValidateNamespace(int, string) error { return nil }\n"
		if complete {
			source += "func(implementation) ValidateCount(int, int32) error { return nil }\n"
		}
		source += "var _ Validator[int] = implementation{}\n"
		set := token.NewFileSet()
		file, err := parser.ParseFile(set, "contract.go", source, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, err = new(types.Config).Check("test", set, []*ast.File{file}, nil)
		if (err == nil) != complete {
			t.Fatalf("complete=%v: %v", complete, err)
		}
	}
}

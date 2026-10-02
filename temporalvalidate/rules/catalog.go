// Package rules resolves declared validation symbols from protobuf descriptors.
package rules

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const metadataName = "temporalvalidate.v1.rule"

var functionName = regexp.MustCompile(`^Validate[A-Z][A-Za-z0-9_]*$`)
var fieldName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

type Rule struct {
	Symbol          protoreflect.FullName
	Function        string
	Kind            protoreflect.Kind
	TypeName        protoreflect.FullName
	CanonicalFields []string
	Number          protoreflect.FieldNumber
}

type Catalog struct {
	rules     []Rule
	canonical map[string]Rule
	exemption protoreflect.FieldNumber
	ignored   protoreflect.FieldNumber
	nested    protoreflect.FieldNumber
	metadata  protoreflect.FieldNumber
}

// New includes imports, so callers may supply only the files they validate.
func New(files []protoreflect.FileDescriptor) (*Catalog, error) {
	catalog := &Catalog{canonical: map[string]Rule{}}
	seen := map[string]bool{}
	var all []protoreflect.FileDescriptor
	var visit func(protoreflect.FileDescriptor)
	visit = func(file protoreflect.FileDescriptor) {
		if seen[file.Path()] {
			return
		}
		seen[file.Path()] = true
		all = append(all, file)
		for i := range file.Imports().Len() {
			visit(file.Imports().Get(i).FileDescriptor)
		}
	}
	for _, file := range files {
		visit(file)
	}
	var metadata protoreflect.FieldNumber
	var declarations []protoreflect.ExtensionDescriptor
	var messages func(protoreflect.MessageDescriptors)
	messages = func(list protoreflect.MessageDescriptors) {
		for i := range list.Len() {
			message := list.Get(i)
			for j := range message.Extensions().Len() {
				declarations = append(declarations, message.Extensions().Get(j))
			}
			messages(message.Messages())
		}
	}
	for _, file := range all {
		for i := range file.Extensions().Len() {
			declarations = append(declarations, file.Extensions().Get(i))
		}
		messages(file.Messages())
	}
	for _, extension := range declarations {
		switch string(extension.FullName()) {
		case metadataName:
			metadata = extension.Number()
		case "temporalvalidate.v1.canonical_rule_ignored":
			catalog.exemption = extension.Number()
		case "temporalvalidate.v1.field_coverage_ignored":
			catalog.ignored = extension.Number()
		case "temporalvalidate.v1.validate_nested":
			catalog.nested = extension.Number()
		}
	}
	functions := map[string]Rule{}
	catalog.metadata = metadata
	for _, extension := range declarations {
		values, err := optionValues(extension.Options(), metadata, protowire.BytesType)
		if err != nil {
			return nil, err
		}
		if len(values) == 0 {
			if strings.HasPrefix(string(extension.FullName()), "temporalvalidate.v1.") && extension.ContainingMessage().FullName() == "google.protobuf.FieldOptions" && extension.Kind() == protoreflect.BoolKind && extension.Name() != "validate_nested" {
				return nil, fmt.Errorf("%s: validator symbol is missing rule metadata", extension.FullName())
			}
			continue
		}
		if len(values) != 1 || extension.Kind() != protoreflect.BoolKind || extension.Cardinality() != protoreflect.Optional || extension.ContainingMessage().FullName() != "google.protobuf.FieldOptions" {
			return nil, fmt.Errorf("%s: rule requires an optional bool FieldOptions extension", extension.FullName())
		}
		rule, err := parse(values[0])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", extension.FullName(), err)
		}
		rule.Symbol, rule.Number = extension.FullName(), extension.Number()
		if previous, ok := functions[rule.Function]; ok {
			return nil, fmt.Errorf("%s: function %s is already owned by %s; reuse that symbol", rule.Symbol, rule.Function, previous.Symbol)
		}
		functions[rule.Function] = rule
		for _, name := range rule.CanonicalFields {
			if previous, ok := catalog.canonical[name]; ok {
				return nil, fmt.Errorf("%s: canonical field %q is already claimed by %s", rule.Symbol, name, previous.Symbol)
			}
			catalog.canonical[name] = rule
		}
		catalog.rules = append(catalog.rules, rule)
	}
	sort.Slice(catalog.rules, func(i, j int) bool { return catalog.rules[i].Symbol < catalog.rules[j].Symbol })
	return catalog, nil
}

func parse(data []byte) (Rule, error) {
	var rule Rule
	seen := map[protowire.Number]bool{}
	for len(data) != 0 {
		number, kind, length := protowire.ConsumeTag(data)
		if length < 0 {
			return rule, protowire.ParseError(length)
		}
		data = data[length:]
		if number != 4 && seen[number] {
			return rule, fmt.Errorf("rule metadata field %d is repeated", number)
		}
		seen[number] = true
		switch number {
		case 1, 3, 4:
			if kind != protowire.BytesType {
				return rule, fmt.Errorf("rule metadata field %d must be a string", number)
			}
			value, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return rule, protowire.ParseError(n)
			}
			switch number {
			case 1:
				rule.Function = string(value)
			case 3:
				rule.TypeName = protoreflect.FullName(value)
			case 4:
				rule.CanonicalFields = append(rule.CanonicalFields, string(value))
			}
			data = data[n:]
		case 2:
			if kind != protowire.VarintType {
				return rule, fmt.Errorf("rule field_type must be an enum")
			}
			value, n := protowire.ConsumeVarint(data)
			if n < 0 {
				return rule, protowire.ParseError(n)
			}
			if value < 1 || value > 18 || value == 10 {
				return rule, fmt.Errorf("rule requires a scalar or message field_type")
			}
			if value < 1 || value > 18 || value == 10 {
				return rule, fmt.Errorf("rule requires a scalar or message field_type")
			}
			rule.Kind = protoreflect.Kind(value)
			data = data[n:]
		default:
			return rule, fmt.Errorf("unknown rule metadata field %d", number)
		}
	}
	if !functionName.MatchString(rule.Function) {
		return rule, fmt.Errorf("rule function must name one exported Validate* method")
	}
	if rule.Kind < protoreflect.DoubleKind || rule.Kind > protoreflect.Sint64Kind || rule.Kind == protoreflect.GroupKind {
		return rule, fmt.Errorf("rule requires a scalar or message field_type")
	}
	named := rule.Kind == protoreflect.MessageKind || rule.Kind == protoreflect.EnumKind
	if named != (rule.TypeName != "") || rule.TypeName != "" && !rule.TypeName.IsValid() {
		return rule, fmt.Errorf("message/enum rules require an exact type_name without a leading dot; scalar rules must omit it")
	}
	aliases := map[string]bool{}
	for _, name := range rule.CanonicalFields {
		if !fieldName.MatchString(name) || aliases[name] {
			return rule, fmt.Errorf("invalid or duplicate canonical field name %q", name)
		}
		aliases[name] = true
	}
	return rule, nil
}

func (r Rule) Check(field protoreflect.FieldDescriptor) error {
	typeName := protoreflect.FullName("")
	if field.Message() != nil {
		typeName = field.Message().FullName()
	}
	if field.Enum() != nil {
		typeName = field.Enum().FullName()
	}
	if field.IsList() || field.IsMap() || field.Kind() != r.Kind || typeName != r.TypeName {
		return fmt.Errorf("%s requires a singular %s %s field, got %s", r.Symbol, r.Kind, r.TypeName, field.FullName())
	}
	return nil
}

func (c *Catalog) Lookup(field protoreflect.FieldDescriptor) ([]Rule, error) {
	metadata, err := optionValues(field.Options(), c.metadata, protowire.BytesType)
	if err != nil {
		return nil, err
	}
	if len(metadata) != 0 {
		return nil, fmt.Errorf("%s: rule metadata belongs on a bool FieldOptions extension", field.FullName())
	}
	var selected []Rule
	for _, rule := range c.rules {
		values, err := optionValues(field.Options(), rule.Number, protowire.VarintType)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", field.FullName(), err)
		}
		if len(values) == 0 {
			continue
		}
		if len(values) != 1 || len(values[0]) != 1 || values[0][0] != 1 {
			return nil, fmt.Errorf("%s: (%s) must be true when set", field.FullName(), rule.Symbol)
		}
		if err := rule.Check(field); err != nil {
			return nil, err
		}
		selected = append(selected, rule)
	}
	return selected, nil
}

func (c *Catalog) AuditsChildren(field protoreflect.FieldDescriptor) (bool, error) {
	values, err := optionValues(field.Options(), c.nested, protowire.VarintType)
	if err != nil {
		return false, err
	}
	if len(values) == 0 {
		return false, nil
	}
	if len(values) != 1 || len(values[0]) != 1 || values[0][0] != 1 {
		return false, fmt.Errorf("%s: validate_nested must be true", field.FullName())
	}
	return true, nil
}

// Canonical is an authoring check. It does not disable runtime validation.
func (c *Catalog) Canonical(field protoreflect.FieldDescriptor) error {
	exemptions, err := optionValues(field.Options(), c.exemption, protowire.BytesType)
	if err != nil {
		return err
	}
	if len(exemptions) != 0 {
		if len(exemptions) != 1 || strings.TrimSpace(string(exemptions[0])) == "" {
			return fmt.Errorf("%s: canonical_rule_ignored requires a reason", field.FullName())
		}
		return nil
	}
	// A full-field coverage exclusion also delegates canonical checks.
	exclusions, err := optionValues(field.Options(), c.ignored, protowire.BytesType)
	if err != nil {
		return err
	}
	if len(exclusions) == 1 && strings.TrimSpace(string(exclusions[0])) != "" {
		return nil
	}
	rule, ok := c.canonical[string(field.Name())]
	if !ok || field.Kind() != rule.Kind {
		return nil
	}
	typeName := protoreflect.FullName("")
	if field.Message() != nil {
		typeName = field.Message().FullName()
	}
	if field.Enum() != nil {
		typeName = field.Enum().FullName()
	}
	if typeName != rule.TypeName {
		return nil
	}
	selected, err := c.Lookup(field)
	if err != nil {
		return err
	}
	for _, selected := range selected {
		if selected.Symbol == rule.Symbol {
			return nil
		}
	}
	return fmt.Errorf("%s: use canonical rule (%s) = true; add canonical_rule_ignored with a reason for different semantics", field.FullName(), rule.Symbol)
}

func optionValues(message proto.Message, number protoreflect.FieldNumber, expected protowire.Type) ([][]byte, error) {
	if number == 0 || message == nil {
		return nil, nil
	}
	data, err := proto.Marshal(message)
	if err != nil {
		return nil, err
	}
	var values [][]byte
	for len(data) != 0 {
		tag, kind, n := protowire.ConsumeTag(data)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		data = data[n:]
		if tag == protowire.Number(number) {
			if kind != expected {
				return nil, fmt.Errorf("option %d has the wrong wire type", number)
			}
			if kind == protowire.BytesType {
				value, n := protowire.ConsumeBytes(data)
				if n < 0 {
					return nil, protowire.ParseError(n)
				}
				values = append(values, value)
			} else {
				value, n := protowire.ConsumeVarint(data)
				if n < 0 {
					return nil, protowire.ParseError(n)
				}
				values = append(values, protowire.AppendVarint(nil, value))
			}
		}
		n = protowire.ConsumeFieldValue(tag, kind, data)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		data = data[n:]
	}
	return values, nil
}

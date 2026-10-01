package main

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

func TestInventoryIncludesAllAPIServices(t *testing.T) {
	set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{
		{Name: proto.String("temporal/api/newservice/v1/service.proto"), Options: &descriptorpb.FileOptions{GoPackage: proto.String("go.temporal.io/api/newservice/v1;newservice")}, Service: []*descriptorpb.ServiceDescriptorProto{{Name: proto.String("First")}, {Name: proto.String("Second")}}},
		{Name: proto.String("example.proto"), Service: []*descriptorpb.ServiceDescriptorProto{{Name: proto.String("Example")}}},
	}}
	generated, err := generate(set)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"go.temporal.io/api/newservice/v1", "File_temporal_api_newservice_v1_service_proto.Services().Get(0)", "File_temporal_api_newservice_v1_service_proto.Services().Get(1)"} {
		if !strings.Contains(string(generated), expected) {
			t.Fatalf("missing service inventory entry: %s", expected)
		}
	}
	if strings.Contains(string(generated), "Example") {
		t.Fatal("example service must not enter the API inventory")
	}
	set.File[0].Options = nil
	if _, err := generate(set); err == nil {
		t.Fatal("missing go_package must fail generation")
	}
}

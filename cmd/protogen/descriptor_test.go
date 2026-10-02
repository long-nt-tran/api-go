package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDescriptorOnlyGenerationDoesNotRewriteGo(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc is not installed")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "example.proto"), []byte("syntax = \"proto3\"; package example; message Example { string value = 1; }"), 0600); err != nil {
		t.Fatal(err)
	}
	goFile := filepath.Join(dir, "example.pb.go")
	original := []byte("do not parse or rewrite this existing Go file\n")
	if err := os.WriteFile(goFile, original, 0600); err != nil {
		t.Fatal(err)
	}
	err := compileProtos(context.Background(), genConfig{
		rootDirs: []string{dir}, includes: []string{dir}, outputDir: dir,
		outputDescriptorPath: filepath.Join(dir, "descriptor_set.pb"), enums: map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile(goFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != string(original) {
		t.Fatal("descriptor generation changed a Go file")
	}
}

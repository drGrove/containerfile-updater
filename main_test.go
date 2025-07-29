// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Helper function to disable logging during tests
func disableLogging() func() {
	originalOutput := log.Writer()
	log.SetOutput(io.Discard)
	return func() {
		log.SetOutput(originalOutput)
	}
}

// MockDigestFetcher implements digest fetching for tests
type MockDigestFetcher struct {
	digests map[string]string
	errors  map[string]error
}

func NewMockDigestFetcher() *MockDigestFetcher {
	return &MockDigestFetcher{
		digests: make(map[string]string),
		errors:  make(map[string]error),
	}
}

func (m *MockDigestFetcher) SetDigest(image, digest string) {
	m.digests[image] = digest
}

func (m *MockDigestFetcher) SetError(image string, err error) {
	m.errors[image] = err
}

// Override the fetchImageDigest method for testing
func (du *ContainerfileUpdater) mockFetchImageDigest(ctx context.Context, imageRef *ImageReference, fetcher *MockDigestFetcher) (string, error) {
	var fullRef string
	if imageRef.Registry == "docker.io" {
		fullRef = fmt.Sprintf("%s:%s", imageRef.Repository, imageRef.Tag)
	} else {
		fullRef = fmt.Sprintf("%s/%s:%s", imageRef.Registry, imageRef.Repository, imageRef.Tag)
	}

	if err, hasError := fetcher.errors[fullRef]; hasError {
		return "", err
	}

	if digest, hasDigest := fetcher.digests[fullRef]; hasDigest {
		return digest, nil
	}

	return "sha256:default-test-digest", nil
}

func TestParseImageReference(t *testing.T) {
	restore := disableLogging()
	defer restore()

	updater := NewContainerfileUpdater("test")

	tests := []struct {
		name     string
		input    string
		expected ImageReference
	}{
		{
			name:  "Docker Hub official image",
			input: "ubuntu:20.04",
			expected: ImageReference{
				Registry:   "docker.io",
				Repository: "library/ubuntu",
				Tag:        "20.04",
				Original:   "ubuntu:20.04",
			},
		},
		{
			name:  "Docker Hub user image",
			input: "stagex/core-filesystem:latest",
			expected: ImageReference{
				Registry:   "docker.io",
				Repository: "stagex/core-filesystem",
				Tag:        "latest",
				Original:   "stagex/core-filesystem:latest",
			},
		},
		{
			name:  "GCR image",
			input: "gcr.io/distroless/static:nonroot",
			expected: ImageReference{
				Registry:   "gcr.io",
				Repository: "distroless/static",
				Tag:        "nonroot",
				Original:   "gcr.io/distroless/static:nonroot",
			},
		},
		{
			name:  "ECR image",
			input: "123456789012.dkr.ecr.us-east-1.amazonaws.com/my-repo:v1.0",
			expected: ImageReference{
				Registry:   "123456789012.dkr.ecr.us-east-1.amazonaws.com",
				Repository: "my-repo",
				Tag:        "v1.0",
				Original:   "123456789012.dkr.ecr.us-east-1.amazonaws.com/my-repo:v1.0",
			},
		},
		{
			name:  "Private registry with port",
			input: "registry.company.com:5000/app:latest",
			expected: ImageReference{
				Registry:   "registry.company.com:5000",
				Repository: "app",
				Tag:        "latest",
				Original:   "registry.company.com:5000/app:latest",
			},
		},
		{
			name:  "Localhost registry",
			input: "localhost:5000/myapp:dev",
			expected: ImageReference{
				Registry:   "localhost:5000",
				Repository: "myapp",
				Tag:        "dev",
				Original:   "localhost:5000/myapp:dev",
			},
		},
		{
			name:  "Image with existing digest",
			input: "ubuntu@sha256:86ac87f73641c920fb42cc9612d4fb57b5626b56bd8368b316b5d4f2df5e49c5",
			expected: ImageReference{
				Registry:   "docker.io",
				Repository: "library/ubuntu",
				Tag:        "latest",
				Digest:     "sha256:86ac87f73641c920fb42cc9612d4fb57b5626b56bd8368b316b5d4f2df5e49c5",
				Original:   "ubuntu@sha256:86ac87f73641c920fb42cc9612d4fb57b5626b56bd8368b316b5d4f2df5e49c5",
			},
		},
		{
			name:  "Image without tag defaults to latest",
			input: "alpine",
			expected: ImageReference{
				Registry:   "docker.io",
				Repository: "library/alpine",
				Tag:        "latest",
				Original:   "alpine",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := updater.parseImageReference(tt.input)
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if result.Registry != tt.expected.Registry {
				t.Errorf("Registry: got %s, want %s", result.Registry, tt.expected.Registry)
			}
			if result.Repository != tt.expected.Repository {
				t.Errorf("Repository: got %s, want %s", result.Repository, tt.expected.Repository)
			}
			if result.Tag != tt.expected.Tag {
				t.Errorf("Tag: got %s, want %s", result.Tag, tt.expected.Tag)
			}
			if result.Digest != tt.expected.Digest {
				t.Errorf("Digest: got %s, want %s", result.Digest, tt.expected.Digest)
			}
		})
	}
}

func TestExtractFromCommands(t *testing.T) {
	restore := disableLogging()
	defer restore()

	containerfileContent := `FROM ubuntu:20.04 AS base
RUN apt-get update

FROM node:16-alpine AS builder
COPY . .

FROM scratch AS empty
COPY myapp /

FROM base
COPY --from=builder /app/dist /app

FROM scratch
COPY --from=base /app /

FROM stagex/core-filesystem:latest
FROM gcr.io/distroless/static:nonroot AS runtime

FROM runtime
COPY --from=builder /app /final-app
`

	// Create temporary containerfile
	tmpDir := t.TempDir()
	containerfilePath := filepath.Join(tmpDir, "Containerfile")
	err := os.WriteFile(containerfilePath, []byte(containerfileContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create test containerfile: %v", err)
	}

	updater := NewContainerfileUpdater(containerfilePath)

	// Parse containerfile
	result, err := updater.parseContainerfile()
	if err != nil {
		t.Fatalf("Failed to parse containerfile: %v", err)
	}

	// Extract FROM commands
	fromCommands, err := updater.extractFromCommands(result.AST)
	if err != nil {
		t.Fatalf("Failed to extract FROM commands: %v", err)
	}

	// Expected: should only extract actual image references, not stage refs or scratch
	expectedImages := []string{
		"ubuntu:20.04",
		"node:16-alpine",
		"stagex/core-filesystem:latest",
		"gcr.io/distroless/static:nonroot",
	}

	if len(fromCommands) != len(expectedImages) {
		t.Fatalf("Expected %d FROM commands, got %d", len(expectedImages), len(fromCommands))
	}

	for i, cmd := range fromCommands {
		if cmd.Image.Original != expectedImages[i] {
			t.Errorf("FROM command %d: got %s, want %s", i, cmd.Image.Original, expectedImages[i])
		}
	}

	// Verify build stages were collected
	expectedStages := []string{"base", "builder", "empty", "runtime"}
	for _, stage := range expectedStages {
		if !updater.buildStages[strings.ToLower(stage)] {
			t.Errorf("Build stage %s was not collected", stage)
		}
	}
}

func TestBuildStageDetection(t *testing.T) {
	tests := []struct {
		name              string
		containerfileContent string
		expectedStages    []string
		expectedFroms     []string
	}{
		{
			name: "Simple multi-stage build",
			containerfileContent: `FROM ubuntu:20.04 AS base
FROM base
FROM node:16 AS builder
FROM builder`,
			expectedStages: []string{"base", "builder"},
			expectedFroms:  []string{"ubuntu:20.04", "node:16"},
		},
		{
			name: "Case insensitive stage references",
			containerfileContent: `FROM ubuntu:20.04 AS Base
FROM BASE
FROM Base`,
			expectedStages: []string{"base"},
			expectedFroms:  []string{"ubuntu:20.04"},
		},
		{
			name: "Scratch handling",
			containerfileContent: `FROM scratch
FROM ubuntu:20.04
FROM SCRATCH AS empty
FROM scratch`,
			expectedStages: []string{"empty"},
			expectedFroms:  []string{"ubuntu:20.04"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			containerfilePath := filepath.Join(tmpDir, "Containerfile")
			err := os.WriteFile(containerfilePath, []byte(tt.containerfileContent), 0644)
			if err != nil {
				t.Fatalf("Failed to create test containerfile: %v", err)
			}

			updater := NewContainerfileUpdater(containerfilePath)
			result, err := updater.parseContainerfile()
			if err != nil {
				t.Fatalf("Failed to parse containerfile: %v", err)
			}

			fromCommands, err := updater.extractFromCommands(result.AST)
			if err != nil {
				t.Fatalf("Failed to extract FROM commands: %v", err)
			}

			// Check stages
			for _, stage := range tt.expectedStages {
				if !updater.buildStages[strings.ToLower(stage)] {
					t.Errorf("Expected stage %s to be collected", stage)
				}
			}

			// Check FROM commands
			if len(fromCommands) != len(tt.expectedFroms) {
				t.Fatalf("Expected %d FROM commands, got %d", len(tt.expectedFroms), len(fromCommands))
			}

			for i, expected := range tt.expectedFroms {
				if fromCommands[i].Image.Original != expected {
					t.Errorf("FROM command %d: got %s, want %s", i, fromCommands[i].Image.Original, expected)
				}
			}
		})
	}
}

func TestContainerfileReconstruction(t *testing.T) {
	restore := disableLogging()
	defer restore()

	originalContent := `# This is a test Containerfile
FROM ubuntu:20.04 AS base
RUN apt-get update

FROM node:16-alpine AS builder
COPY . .
RUN npm install

FROM base
COPY --from=builder /app/dist /app

FROM stagex/core-filesystem:latest
ENV APP_ENV=production
`

	expectedContent := `# This is a test Containerfile
FROM library/ubuntu@sha256:test-ubuntu-digest AS base
RUN apt-get update

FROM library/node@sha256:test-node-digest AS builder
COPY . .
RUN npm install

FROM base
COPY --from=builder /app/dist /app

FROM stagex/core-filesystem@sha256:test-stagex-digest
ENV APP_ENV=production
`

	tmpDir := t.TempDir()
	containerfilePath := filepath.Join(tmpDir, "Containerfile")
	err := os.WriteFile(containerfilePath, []byte(originalContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create test containerfile: %v", err)
	}

	updater := NewContainerfileUpdater(containerfilePath)

	// Mock the digest fetching
	fetcher := NewMockDigestFetcher()
	fetcher.SetDigest("library/ubuntu:20.04", "sha256:test-ubuntu-digest")
	fetcher.SetDigest("library/node:16-alpine", "sha256:test-node-digest")
	fetcher.SetDigest("stagex/core-filesystem:latest", "sha256:test-stagex-digest")

	// Parse and extract FROM commands
	result, err := updater.parseContainerfile()
	if err != nil {
		t.Fatalf("Failed to parse containerfile: %v", err)
	}

	fromCommands, err := updater.extractFromCommands(result.AST)
	if err != nil {
		t.Fatalf("Failed to extract FROM commands: %v", err)
	}

	// Update with mock digests
	for _, cmd := range fromCommands {
		digest, err := updater.mockFetchImageDigest(context.Background(), cmd.Image, fetcher)
		if err != nil {
			t.Fatalf("Failed to fetch mock digest: %v", err)
		}
		cmd.Image.Digest = digest
	}

	// Reconstruct containerfile
	err = updater.reconstructAndWriteContainerfile(result, fromCommands)
	if err != nil {
		t.Fatalf("Failed to reconstruct containerfile: %v", err)
	}

	// Read the updated content
	updatedContent, err := os.ReadFile(containerfilePath)
	if err != nil {
		t.Fatalf("Failed to read updated containerfile: %v", err)
	}

	updatedStr := strings.TrimSpace(string(updatedContent))
	expectedStr := strings.TrimSpace(expectedContent)

	if updatedStr != expectedStr {
		t.Errorf("Containerfile content mismatch.\nExpected:\n%s\n\nGot:\n%s", expectedStr, updatedStr)

		// Show line-by-line diff for debugging
		expectedLines := strings.Split(expectedStr, "\n")
		actualLines := strings.Split(updatedStr, "\n")

		maxLines := len(expectedLines)
		if len(actualLines) > maxLines {
			maxLines = len(actualLines)
		}

		for i := 0; i < maxLines; i++ {
			var expectedLine, actualLine string
			if i < len(expectedLines) {
				expectedLine = expectedLines[i]
			}
			if i < len(actualLines) {
				actualLine = actualLines[i]
			}

			if expectedLine != actualLine {
				t.Errorf("Line %d differs:\n  Expected: %q\n  Actual:   %q", i+1, expectedLine, actualLine)
			}
		}
	}

	// Check that backup was created
	backupPath := containerfilePath + ".backup"
	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		t.Error("Backup file was not created")
	}
}

func TestErrorHandling(t *testing.T) {
	restore := disableLogging()
	defer restore()

	tests := []struct {
		name              string
		containerfileContent string
		shouldError       bool
		errorContains     string
	}{
		{
			name:              "Empty containerfile",
			containerfileContent: "",
			shouldError:       true, // BuildKit parser returns error for empty files
			errorContains:     "file with no instructions",
		},
		{
			name: "Malformed FROM command",
			containerfileContent: `FROM
RUN echo "test"`,
			shouldError: false, // Should log warning but continue
		},
		{
			name: "Valid containerfile",
			containerfileContent: `FROM ubuntu:20.04
RUN echo "test"`,
			shouldError: false,
		},
		{
			name: "Containerfile with just comments",
			containerfileContent: `# This is a comment
# Another comment`,
			shouldError: true, // BuildKit treats this as empty
			errorContains: "file with no instructions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			containerfilePath := filepath.Join(tmpDir, "Containerfile")
			err := os.WriteFile(containerfilePath, []byte(tt.containerfileContent), 0644)
			if err != nil {
				t.Fatalf("Failed to create test containerfile: %v", err)
			}

			updater := NewContainerfileUpdater(containerfilePath)
			result, err := updater.parseContainerfile()

			if tt.shouldError && err == nil {
				t.Errorf("Expected error but got none")
			}

			if !tt.shouldError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}

			if tt.errorContains != "" && err != nil && !strings.Contains(err.Error(), tt.errorContains) {
				t.Errorf("Expected error to contain '%s', got: %v", tt.errorContains, err)
			}

			if err == nil {
				// Try to extract FROM commands
				_, err = updater.extractFromCommands(result.AST)
				if tt.shouldError && err == nil {
					t.Errorf("Expected error during FROM extraction but got none")
				}
			}
		})
	}
}

func TestNonExistentContainerfile(t *testing.T) {
	restore := disableLogging()
	defer restore()

	updater := NewContainerfileUpdater("/nonexistent/Containerfile")
	err := updater.UpdateContainerfileWithLatestDigests()

	if err == nil {
		t.Error("Expected error for nonexistent containerfile")
	}

	if !strings.Contains(err.Error(), "failed to parse Containerfile") {
		t.Errorf("Expected parse error, got: %v", err)
	}
}

func TestBuildKitParserIntegration(t *testing.T) {
	restore := disableLogging()
	defer restore()

	containerfileContent := `# syntax=docker/containerfile:1
FROM ubuntu:20.04 AS base
RUN apt-get update && apt-get install -y curl

FROM node:16-alpine AS builder
WORKDIR /app
COPY package*.json ./
RUN npm ci --only=production

FROM base
COPY --from=builder /app /app
EXPOSE 3000
CMD ["node", "/app/server.js"]
`

	tmpDir := t.TempDir()
	containerfilePath := filepath.Join(tmpDir, "Containerfile")
	err := os.WriteFile(containerfilePath, []byte(containerfileContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create test containerfile: %v", err)
	}

	updater := NewContainerfileUpdater(containerfilePath)

	// Test that BuildKit parser can handle the containerfile
	result, err := updater.parseContainerfile()
	if err != nil {
		t.Fatalf("BuildKit parser failed: %v", err)
	}

	if result.AST == nil {
		t.Error("BuildKit parser returned nil AST")
	}

	// Verify we can extract FROM commands
	fromCommands, err := updater.extractFromCommands(result.AST)
	if err != nil {
		t.Fatalf("Failed to extract FROM commands: %v", err)
	}

	expectedFromCount := 2 // ubuntu and node, not base reference
	if len(fromCommands) != expectedFromCount {
		t.Errorf("Expected %d FROM commands, got %d", expectedFromCount, len(fromCommands))
	}
}

func TestComplexMultiStageContainerfile(t *testing.T) {
	restore := disableLogging()
	defer restore()

	containerfileContent := `FROM golang:1.19-alpine AS go-builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o main .

FROM node:16-alpine AS js-builder
WORKDIR /frontend
COPY frontend/package*.json ./
RUN npm ci
COPY frontend/ .
RUN npm run build

FROM alpine:latest AS certs
RUN apk --no-cache add ca-certificates

FROM scratch AS final
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=go-builder /app/main /main
COPY --from=js-builder /frontend/dist /static
EXPOSE 8080
ENTRYPOINT ["/main"]
`

	tmpDir := t.TempDir()
	containerfilePath := filepath.Join(tmpDir, "Containerfile")
	err := os.WriteFile(containerfilePath, []byte(containerfileContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create test containerfile: %v", err)
	}

	updater := NewContainerfileUpdater(containerfilePath)
	result, err := updater.parseContainerfile()
	if err != nil {
		t.Fatalf("Failed to parse complex containerfile: %v", err)
	}

	fromCommands, err := updater.extractFromCommands(result.AST)
	if err != nil {
		t.Fatalf("Failed to extract FROM commands: %v", err)
	}

	// Should extract: golang, node, alpine (not scratch, certs, go-builder, js-builder, final)
	expectedImages := []string{
		"golang:1.19-alpine",
		"node:16-alpine",
		"alpine:latest",
	}

	if len(fromCommands) != len(expectedImages) {
		t.Fatalf("Expected %d image FROM commands, got %d", len(expectedImages), len(fromCommands))
	}

	for i, expected := range expectedImages {
		if fromCommands[i].Image.Original != expected {
			t.Errorf("FROM command %d: got %s, want %s", i, fromCommands[i].Image.Original, expected)
		}
	}

	// Verify all build stages were collected
	expectedStages := []string{"go-builder", "js-builder", "certs", "final"}
	for _, stage := range expectedStages {
		if !updater.buildStages[strings.ToLower(stage)] {
			t.Errorf("Build stage %s was not collected", stage)
		}
	}
}

// Benchmark tests
func BenchmarkParseImageReference(b *testing.B) {
	updater := NewContainerfileUpdater("test")
	testImages := []string{
		"ubuntu:20.04",
		"gcr.io/distroless/static:nonroot",
		"123456789012.dkr.ecr.us-east-1.amazonaws.com/my-repo:v1.0",
		"stagex/core-filesystem:latest",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, img := range testImages {
			_, err := updater.parseImageReference(img)
			if err != nil {
				b.Fatalf("Unexpected error: %v", err)
			}
		}
	}
}

func BenchmarkExtractFromCommands(b *testing.B) {
	containerfileContent := `FROM ubuntu:20.04 AS base
FROM node:16-alpine AS builder
FROM base
FROM stagex/core-filesystem:latest
FROM gcr.io/distroless/static:nonroot AS runtime
FROM runtime`

	tmpDir := b.TempDir()
	containerfilePath := filepath.Join(tmpDir, "Containerfile")
	err := os.WriteFile(containerfilePath, []byte(containerfileContent), 0644)
	if err != nil {
		b.Fatalf("Failed to create test containerfile: %v", err)
	}

	updater := NewContainerfileUpdater(containerfilePath)
	result, err := updater.parseContainerfile()
	if err != nil {
		b.Fatalf("Failed to parse containerfile: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Reset build stages for each iteration
		updater.buildStages = make(map[string]bool)
		_, err := updater.extractFromCommands(result.AST)
		if err != nil {
			b.Fatalf("Failed to extract FROM commands: %v", err)
		}
	}
}

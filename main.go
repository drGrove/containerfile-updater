// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	// BuildKit dockerfile parser
	"github.com/moby/buildkit/frontend/dockerfile/parser"

	// Container registry client
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// ContainerfileUpdater handles parsing and updating Containerfiles with latest digests
type ContainerfileUpdater struct {
	containerfilePath string
	timeout        time.Duration
	buildStages    map[string]bool // Track build stage aliases
}

// ImageReference represents a parsed image reference from a FROM command
type ImageReference struct {
	Registry   string // Registry hostname (e.g., "docker.io", "gcr.io")
	Repository string // Repository name (e.g., "library/ubuntu", "google/pause")
	Tag        string // Tag name (e.g., "latest", "20.04")
	Digest     string // SHA256 digest (if already present)
	Original   string // Original reference string
}

// NewContainerfileUpdater creates a new ContainerfileUpdater instance
func NewContainerfileUpdater(containerfilePath string) *ContainerfileUpdater {
	return &ContainerfileUpdater{
		containerfilePath: containerfilePath,
		timeout:        30 * time.Second,
		buildStages:    make(map[string]bool),
	}
}

// UpdateContainerfileWithLatestDigests is the main entry point
func (du *ContainerfileUpdater) UpdateContainerfileWithLatestDigests() error {
	log.Printf("Processing Containerfile: %s", du.containerfilePath)

	// Step 1: Parse Containerfile using BuildKit parser
	result, err := du.parseContainerfile()
	if err != nil {
		return fmt.Errorf("failed to parse Containerfile: %w", err)
	}

	// Step 2: Extract FROM commands from AST
	fromCommands, err := du.extractFromCommands(result.AST)
	if err != nil {
		return fmt.Errorf("failed to extract FROM commands: %w", err)
	}

	if len(fromCommands) == 0 {
		log.Println("No FROM commands found in Containerfile")
		return nil
	}

	log.Printf("Found %d FROM command(s)", len(fromCommands))

	// Step 3: Update FROM commands with latest digests
	updatedCommands, err := du.updateFromCommandsWithDigests(fromCommands)
	if err != nil {
		return fmt.Errorf("failed to update FROM commands with digests: %w", err)
	}

	// Step 4: Reconstruct and write updated Containerfile
	err = du.reconstructAndWriteContainerfile(result, updatedCommands)
	if err != nil {
		return fmt.Errorf("failed to write updated Containerfile: %w", err)
	}

	log.Printf("Successfully updated Containerfile: %s", du.containerfilePath)
	return nil
}

// parseContainerfile uses BuildKit parser to parse the Containerfile into AST
func (du *ContainerfileUpdater) parseContainerfile() (*parser.Result, error) {
	file, err := os.Open(du.containerfilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open Containerfile: %w", err)
	}
	defer file.Close()

	// Parse using BuildKit containerfile parser
	result, err := parser.Parse(file)
	if err != nil {
		return nil, fmt.Errorf("failed to parse Containerfile with BuildKit parser: %w", err)
	}

	// Print any parser warnings
	for _, warning := range result.Warnings {
		log.Printf("Parser warning: %s", warning.Short)
	}

	return result, nil
}

// FromCommand represents a FROM command found in the AST
type FromCommand struct {
	Node      *parser.Node
	Image     *ImageReference
	LineStart int
	LineEnd   int
}

// extractFromCommands traverses the AST to find all FROM commands
func (du *ContainerfileUpdater) extractFromCommands(ast *parser.Node) ([]*FromCommand, error) {
	var fromCommands []*FromCommand

	// First pass: collect all build stage aliases
	for _, child := range ast.Children {
		if strings.ToLower(child.Value) == "from" {
			du.collectBuildStageAlias(child)
		}
	}

	// Second pass: process FROM commands, skipping stage references
	for _, child := range ast.Children {
		if strings.ToLower(child.Value) == "from" {
			log.Printf("Found FROM command at line %d-%d: %s", child.StartLine, child.EndLine, child.Original)

			// Extract image reference from FROM command
			imageRef, isStageRef, err := du.parseFromCommand(child)
			if err != nil {
				log.Printf("Warning: failed to parse FROM command: %v", err)
				continue
			}

			if isStageRef {
				log.Printf("Skipping FROM command that references build stage or special image: %s", imageRef.Original)
				continue
			}

			fromCommands = append(fromCommands, &FromCommand{
				Node:      child,
				Image:     imageRef,
				LineStart: child.StartLine,
				LineEnd:   child.EndLine,
			})
		}
	}

	return fromCommands, nil
}

// collectBuildStageAlias extracts build stage aliases from FROM commands
func (du *ContainerfileUpdater) collectBuildStageAlias(node *parser.Node) {
	if node.Next == nil {
		return
	}

	// Look for AS keyword and collect the alias
	current := node.Next
	for current.Next != nil {
		current = current.Next
		if strings.ToLower(current.Value) == "as" {
			// Found AS clause, get the alias if present
			if current.Next != nil {
				alias := current.Next.Value
				du.buildStages[strings.ToLower(alias)] = true
				log.Printf("Collected build stage alias: %s", alias)
			}
			break
		}
	}
}

// parseFromCommand extracts the image reference from a FROM command node
func (du *ContainerfileUpdater) parseFromCommand(node *parser.Node) (*ImageReference, bool, error) {
	if node.Next == nil {
		return nil, false, fmt.Errorf("FROM command missing image reference")
	}

	// Get the image reference string (first argument after FROM)
	imageStr := node.Next.Value
	if imageStr == "" {
		return nil, false, fmt.Errorf("empty image reference in FROM command")
	}

	// Check if this references a build stage
	if du.buildStages[strings.ToLower(imageStr)] {
		// This is a stage reference, return it but mark as stage reference
		return &ImageReference{Original: imageStr}, true, nil
	}

	// Check if this is the special "scratch" base image
	if strings.ToLower(imageStr) == "scratch" {
		// scratch is a special empty base image, not a real registry image
		return &ImageReference{Original: imageStr}, true, nil
	}

	// Check if this is a multi-stage build with AS clause
	// We need to extract just the image reference, ignoring everything after AS
	current := node.Next
	var asAlias string

	// Look for AS keyword in subsequent nodes
	for current.Next != nil {
		current = current.Next
		if strings.ToLower(current.Value) == "as" {
			// Found AS clause, get the alias if present
			if current.Next != nil {
				asAlias = current.Next.Value
				log.Printf("Found multi-stage build alias: %s", asAlias)
			}
			break
		}
	}

	// Parse only the image reference part (before AS)
	imageRef, err := du.parseImageReference(imageStr)
	if err != nil {
		return nil, false, err
	}

	return imageRef, false, nil
}

// parseImageReference parses an image reference string into components
func (du *ContainerfileUpdater) parseImageReference(imageRef string) (*ImageReference, error) {
	// Handle digest references (image@sha256:...)
	if strings.Contains(imageRef, "@sha256:") {
		parts := strings.Split(imageRef, "@")
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid digest reference format: %s", imageRef)
		}

		baseRef := parts[0]
		digest := parts[1]

		// Parse the base reference
		parsed, err := du.parseImageReference(baseRef)
		if err != nil {
			return nil, err
		}
		parsed.Digest = digest
		parsed.Original = imageRef
		return parsed, nil
	}

	// Split registry/repository:tag
	var registry, repository, tag string

	// Check if it includes a registry hostname
	// A registry hostname must contain a "." or ":" or be "localhost"
	registryRegex := regexp.MustCompile(`^([a-zA-Z0-9.-]+(?::[0-9]+)?)/(.+)`)

	if match := registryRegex.FindStringSubmatch(imageRef); match != nil {
		potentialRegistry := match[1]
		remainder := match[2]

		// Check if this is actually a registry hostname
		// Must contain "." or ":" or be "localhost"
		if strings.Contains(potentialRegistry, ".") ||
		   strings.Contains(potentialRegistry, ":") ||
		   potentialRegistry == "localhost" {
			registry = potentialRegistry

			// Split repository and tag from remainder
			if colonIndex := strings.LastIndex(remainder, ":"); colonIndex != -1 {
				repository = remainder[:colonIndex]
				tag = remainder[colonIndex+1:]
			} else {
				repository = remainder
				tag = "latest"
			}
		} else {
			// This is not a registry, treat as Docker Hub image
			registry = "docker.io"

			// Split repository and tag from full imageRef
			if colonIndex := strings.LastIndex(imageRef, ":"); colonIndex != -1 {
				repository = imageRef[:colonIndex]
				tag = imageRef[colonIndex+1:]
			} else {
				repository = imageRef
				tag = "latest"
			}

			// Add library/ prefix for official images (single component names)
			if !strings.Contains(repository, "/") {
				repository = "library/" + repository
			}
		}
	} else {
		// No slash found, must be Docker Hub
		registry = "docker.io"

		// Split repository and tag
		if colonIndex := strings.LastIndex(imageRef, ":"); colonIndex != -1 {
			repository = imageRef[:colonIndex]
			tag = imageRef[colonIndex+1:]
		} else {
			repository = imageRef
			tag = "latest"
		}

		// Add library/ prefix for official images (single component names)
		if !strings.Contains(repository, "/") {
			repository = "library/" + repository
		}
	}

	return &ImageReference{
		Registry:   registry,
		Repository: repository,
		Tag:        tag,
		Original:   imageRef,
	}, nil
}

// updateFromCommandsWithDigests fetches latest digests for each FROM command
func (du *ContainerfileUpdater) updateFromCommandsWithDigests(fromCommands []*FromCommand) ([]*FromCommand, error) {
	ctx, cancel := context.WithTimeout(context.Background(), du.timeout)
	defer cancel()

	for _, cmd := range fromCommands {
		// Always fetch latest digest, even if one already exists
		log.Printf("Fetching latest digest for %s/%s:%s from %s", cmd.Image.Registry, cmd.Image.Repository, cmd.Image.Tag, cmd.Image.Registry)

		digest, err := du.fetchImageDigest(ctx, cmd.Image)
		if err != nil {
			log.Printf("Warning: failed to fetch digest for %s: %v", cmd.Image.Original, err)
			continue
		}

		log.Printf("Found latest digest for %s: %s", cmd.Image.Original, digest)
		cmd.Image.Digest = digest
	}

	return fromCommands, nil
}

// fetchImageDigest fetches the manifest digest for an image reference
func (du *ContainerfileUpdater) fetchImageDigest(ctx context.Context, imageRef *ImageReference) (string, error) {
	// Construct full image reference
	var fullRef string
	if imageRef.Registry == "docker.io" {
		// Docker Hub shorthand
		fullRef = fmt.Sprintf("%s:%s", imageRef.Repository, imageRef.Tag)
	} else {
		fullRef = fmt.Sprintf("%s/%s:%s", imageRef.Registry, imageRef.Repository, imageRef.Tag)
	}

	// Parse reference using go-containerregistry
	ref, err := name.ParseReference(fullRef)
	if err != nil {
		return "", fmt.Errorf("failed to parse reference %s: %w", fullRef, err)
	}

	// Set up authentication (uses Docker config by default)
	options := []remote.Option{
		remote.WithAuthFromKeychain(authn.DefaultKeychain),
		remote.WithContext(ctx),
	}

	// Get manifest descriptor to obtain digest
	descriptor, err := remote.Get(ref, options...)
	if err != nil {
		return "", fmt.Errorf("failed to fetch manifest for %s: %w", fullRef, err)
	}

	return descriptor.Digest.String(), nil
}

// reconstructAndWriteContainerfile rebuilds the Containerfile with updated FROM commands
func (du *ContainerfileUpdater) reconstructAndWriteContainerfile(result *parser.Result, updatedCommands []*FromCommand) error {
	// Read original Containerfile lines
	file, err := os.Open(du.containerfilePath)
	if err != nil {
		return fmt.Errorf("failed to open original Containerfile: %w", err)
	}
	defer file.Close()

	var originalLines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		originalLines = append(originalLines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("failed to read original Containerfile: %w", err)
	}

	// Create map of line numbers to updated FROM commands
	updateMap := make(map[int]*FromCommand)
	for _, cmd := range updatedCommands {
		// Only update if we successfully fetched a digest
		if cmd.Image.Digest != "" {
			updateMap[cmd.LineStart] = cmd
		}
	}

	// Build new Containerfile content
	var newLines []string
	for i, line := range originalLines {
		lineNum := i + 1 // Line numbers are 1-based

		if cmd, shouldUpdate := updateMap[lineNum]; shouldUpdate {
			// Construct new FROM line with digest
			var newImageRef string
			if cmd.Image.Registry == "docker.io" {
				// Use Docker Hub shorthand format
				newImageRef = fmt.Sprintf("%s@%s", cmd.Image.Repository, cmd.Image.Digest)
			} else {
				// Use full registry format
				newImageRef = fmt.Sprintf("%s/%s@%s", cmd.Image.Registry, cmd.Image.Repository, cmd.Image.Digest)
			}

			// Replace the FROM line, preserving any aliases or flags
			originalLine := line
			// Simple replacement of the image reference part
			updatedLine := strings.Replace(originalLine, cmd.Image.Original, newImageRef, 1)
			newLines = append(newLines, updatedLine)

			log.Printf("Updated line %d: %s -> %s", lineNum, originalLine, updatedLine)
		} else {
			newLines = append(newLines, line)
		}
	}

	// Write updated Containerfile
	return du.writeContainerfile(newLines)
}

// writeContainerfile writes the updated content back to the Containerfile
func (du *ContainerfileUpdater) writeContainerfile(lines []string) error {
	// Create backup of original file
	backupPath := du.containerfilePath + ".backup"
	if err := du.copyFile(du.containerfilePath, backupPath); err != nil {
		log.Printf("Warning: failed to create backup: %v", err)
	} else {
		log.Printf("Created backup: %s", backupPath)
	}

	// Write updated content
	file, err := os.Create(du.containerfilePath)
	if err != nil {
		return fmt.Errorf("failed to create updated Containerfile: %w", err)
	}
	defer file.Close()

	writer := bufio.NewWriter(file)
	for _, line := range lines {
		if _, err := writer.WriteString(line + "\n"); err != nil {
			return fmt.Errorf("failed to write line to Containerfile: %w", err)
		}
	}

	if err := writer.Flush(); err != nil {
		return fmt.Errorf("failed to flush Containerfile: %w", err)
	}

	return nil
}

// copyFile creates a copy of the source file
func (du *ContainerfileUpdater) copyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destFile.Close()

	_, err = io.Copy(destFile, sourceFile)
	return err
}

// main function demonstrating usage
func main() {
	if len(os.Args) < 2 {
		fmt.Printf("Usage: %s <containerfile-path>\n", filepath.Base(os.Args[0]))
		fmt.Println("Example: ./containerfile-updater ./Containerfile")
		os.Exit(1)
	}

	containerfilePath := os.Args[1]

	// Check if Containerfile exists
	if _, err := os.Stat(containerfilePath); os.IsNotExist(err) {
		log.Fatalf("Containerfile not found: %s", containerfilePath)
	}

	// Create updater and process the Containerfile
	updater := NewContainerfileUpdater(containerfilePath)
	if err := updater.UpdateContainerfileWithLatestDigests(); err != nil {
		log.Fatalf("Failed to update Containerfile: %v", err)
	}
}

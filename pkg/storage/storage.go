package storage

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"

	"k8s-backup/pkg/types"
)

type Storage interface {
	SaveBackup(ctx context.Context, metadata *types.BackupMetadata, resources []types.ResourceWithContent) error
	LoadBackup(ctx context.Context, backupPath string) (*types.BackupManifest, []types.ResourceWithContent, error)
	ListBackups() ([]*types.BackupMetadata, error)
	DeleteBackup(backupPath string) error
	GetBackupPath(backupName string) string
}

type LocalStorage struct {
	basePath string
}

func NewLocalStorage(basePath string) *LocalStorage {
	return &LocalStorage{basePath: basePath}
}

// SaveBackup saves a backup to the local filesystem
func (s *LocalStorage) SaveBackup(ctx context.Context, metadata *types.BackupMetadata, resources []types.ResourceWithContent) error {
	// Create backup directory
	backupDir := filepath.Join(s.basePath, metadata.Name)
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return fmt.Errorf("failed to create backup directory: %w", err)
	}

	// Update metadata with final path
	metadata.BackupPath = backupDir

	// Create manifest
	manifest := &types.BackupManifest{
		Metadata:  *metadata,
		Resources: make([]types.ResourceInfo, len(resources)),
	}

	// Batch directory creation and file operations for efficiency
	dirMap := make(map[string]bool)
	totalSize := int64(0)

	for i, resource := range resources {
		// Determine file path within backup
		var relativePath string
		if resource.Info.Namespace != "" {
			relativePath = filepath.Join(resource.Info.Namespace,
				fmt.Sprintf("%s-%s.yaml", strings.ToLower(resource.Info.Kind), resource.Info.Name))
		} else {
			relativePath = filepath.Join("cluster",
				fmt.Sprintf("%s-%s.yaml", strings.ToLower(resource.Info.Kind), resource.Info.Name))
		}

		// Batch directory creation
		dir := filepath.Dir(filepath.Join(backupDir, relativePath))
		if !dirMap[dir] {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", dir, err)
			}
			dirMap[dir] = true
		}

		// Update resource info and manifest first
		resourceInfo := resource.Info
		resourceInfo.RelativePath = relativePath
		manifest.Resources[i] = resourceInfo
		totalSize += int64(len(resource.Content))

		// Check context cancellation before I/O
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}

	// Write all files in a second pass for better I/O efficiency
	for i, resource := range resources {
		relativePath := manifest.Resources[i].RelativePath
		fullPath := filepath.Join(backupDir, relativePath)
		if err := os.WriteFile(fullPath, resource.Content, 0644); err != nil {
			return fmt.Errorf("failed to write resource file %s: %w", relativePath, err)
		}
	}

	// Update metadata with final size
	manifest.Metadata.Size = totalSize

	// Save manifest
	manifestPath := filepath.Join(backupDir, types.ManifestFileName)
	manifestData, err := yaml.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("failed to marshal manifest: %w", err)
	}

	if err := os.WriteFile(manifestPath, manifestData, 0644); err != nil {
		return fmt.Errorf("failed to write manifest: %w", err)
	}

	// Optionally compress backup
	if metadata.Compress {
		if err := s.compressBackup(backupDir); err != nil {
			return fmt.Errorf("failed to compress backup: %w", err)
		}
	}

	return nil
}

// LoadBackup loads a backup from the local filesystem
func (s *LocalStorage) LoadBackup(ctx context.Context, backupPath string) (*types.BackupManifest, []types.ResourceWithContent, error) {
	// Handle compressed backups
	isCompressed := strings.HasSuffix(backupPath, ".tar.gz")
	actualPath := backupPath

	if isCompressed {
		// Extract compressed backup to temporary directory
		tempDir, err := s.extractBackup(backupPath)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to extract backup: %w", err)
		}
		defer os.RemoveAll(tempDir) // Cleanup temp directory
		actualPath = tempDir
	}

	// Load manifest
	manifestPath := filepath.Join(actualPath, types.ManifestFileName)
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read manifest: %w", err)
	}

	var manifest types.BackupManifest
	if err := yaml.Unmarshal(manifestData, &manifest); err != nil {
		return nil, nil, fmt.Errorf("failed to unmarshal manifest: %w", err)
	}

	// Load resource files
	resources := make([]types.ResourceWithContent, 0, len(manifest.Resources))
	for _, resourceInfo := range manifest.Resources {
		resourcePath := filepath.Join(actualPath, resourceInfo.RelativePath)
		content, err := os.ReadFile(resourcePath)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to read resource file %s: %w", resourceInfo.RelativePath, err)
		}

		resource := types.ResourceWithContent{
			Content: content,
			Info:    resourceInfo,
		}
		resources = append(resources, resource)

		// Check context cancellation
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
	}

	return &manifest, resources, nil
}

// ListBackups returns all available backups in the local storage
func (s *LocalStorage) ListBackups() ([]*types.BackupMetadata, error) {
	// Ensure base path exists
	if _, err := os.Stat(s.basePath); os.IsNotExist(err) {
		return []*types.BackupMetadata{}, nil
	}

	entries, err := os.ReadDir(s.basePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read backup directory: %w", err)
	}

	var backups []*types.BackupMetadata
	for _, entry := range entries {
		if entry.IsDir() {
			// Directory-based backup
			manifestPath := filepath.Join(s.basePath, entry.Name(), types.ManifestFileName)
			if metadata, err := s.loadMetadataFromManifest(manifestPath); err == nil {
				backups = append(backups, metadata)
			}
		} else if strings.HasSuffix(entry.Name(), ".tar.gz") {
			// Compressed backup
			backupPath := filepath.Join(s.basePath, entry.Name())
			if metadata, err := s.loadMetadataFromCompressed(backupPath); err == nil {
				backups = append(backups, metadata)
			}
		}
	}

	return backups, nil
}

// DeleteBackup removes a backup from local storage
func (s *LocalStorage) DeleteBackup(backupPath string) error {
	return os.RemoveAll(backupPath)
}

// GetBackupPath returns the full path for a backup
func (s *LocalStorage) GetBackupPath(backupName string) string {
	return filepath.Join(s.basePath, backupName)
}

// compressBackup creates a gzipped tar archive of the backup directory
func (s *LocalStorage) compressBackup(backupDir string) error {
	archiveName := backupDir + ".tar.gz"

	// Create archive file
	archiveFile, err := os.Create(archiveName)
	if err != nil {
		return fmt.Errorf("failed to create archive file: %w", err)
	}
	defer archiveFile.Close()

	// Create gzip writer
	gzipWriter := gzip.NewWriter(archiveFile)
	defer gzipWriter.Close()

	// Create tar writer
	tarWriter := tar.NewWriter(gzipWriter)
	defer tarWriter.Close()

	// Walk through backup directory and add files to archive
	err = filepath.Walk(backupDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Get relative path within backup
		relPath, err := filepath.Rel(backupDir, path)
		if err != nil {
			return err
		}

		// Skip root directory
		if relPath == "." {
			return nil
		}

		// Create tar header
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = relPath

		// Write header
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}

		// Write file content if it's a regular file
		if info.Mode().IsRegular() {
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()

			_, err = io.Copy(tarWriter, file)
			return err
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to create archive: %w", err)
	}

	// Remove original directory after successful compression
	return os.RemoveAll(backupDir)
}

// extractBackup extracts a compressed backup to a temporary directory
func (s *LocalStorage) extractBackup(archivePath string) (string, error) {
	// Create temporary directory
	tempDir, err := os.MkdirTemp("", "k8s-backup-extract-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp directory: %w", err)
	}

	// Open archive file
	archiveFile, err := os.Open(archivePath)
	if err != nil {
		os.RemoveAll(tempDir)
		return "", fmt.Errorf("failed to open archive: %w", err)
	}
	defer archiveFile.Close()

	// Create gzip reader
	gzipReader, err := gzip.NewReader(archiveFile)
	if err != nil {
		os.RemoveAll(tempDir)
		return "", fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer gzipReader.Close()

	// Create tar reader
	tarReader := tar.NewReader(gzipReader)

	// Extract files
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			os.RemoveAll(tempDir)
			return "", fmt.Errorf("failed to read tar header: %w", err)
		}

		// Full path for extracted file
		extractPath := filepath.Join(tempDir, header.Name)

		// Ensure directory exists
		if err := os.MkdirAll(filepath.Dir(extractPath), 0755); err != nil {
			os.RemoveAll(tempDir)
			return "", fmt.Errorf("failed to create directory: %w", err)
		}

		// Extract file content
		if header.Typeflag == tar.TypeReg {
			outFile, err := os.Create(extractPath)
			if err != nil {
				os.RemoveAll(tempDir)
				return "", fmt.Errorf("failed to create file: %w", err)
			}

			_, err = io.Copy(outFile, tarReader)
			outFile.Close()
			if err != nil {
				os.RemoveAll(tempDir)
				return "", fmt.Errorf("failed to extract file: %w", err)
			}
		}
	}

	return tempDir, nil
}

// loadMetadataFromManifest loads backup metadata from a manifest file
func (s *LocalStorage) loadMetadataFromManifest(manifestPath string) (*types.BackupMetadata, error) {
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, err
	}

	var manifest types.BackupManifest
	if err := yaml.Unmarshal(manifestData, &manifest); err != nil {
		return nil, err
	}

	return &manifest.Metadata, nil
}

// loadMetadataFromCompressed loads backup metadata from a compressed backup
func (s *LocalStorage) loadMetadataFromCompressed(archivePath string) (*types.BackupMetadata, error) {
	// This is a simplified version - in practice, you might want to read just the manifest
	// without extracting the entire archive
	tempDir, err := s.extractBackup(archivePath)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tempDir)

	manifestPath := filepath.Join(tempDir, types.ManifestFileName)
	metadata, err := s.loadMetadataFromManifest(manifestPath)
	if err != nil {
		return nil, err
	}

	// Update path to point to the compressed archive
	metadata.BackupPath = archivePath

	// Get file size
	if stat, err := os.Stat(archivePath); err == nil {
		metadata.Size = stat.Size()
	}

	return metadata, nil
}

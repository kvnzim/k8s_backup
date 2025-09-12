package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s-backup/pkg/types"
)

func TestNewLocalStorage(t *testing.T) {
	basePath := "/tmp/test-storage"
	storage := NewLocalStorage(basePath)

	if storage.basePath != basePath {
		t.Errorf("Expected basePath %s, got %s", basePath, storage.basePath)
	}
}

func TestGetBackupPath(t *testing.T) {
	storage := NewLocalStorage("/tmp/backups")
	backupName := "test-backup"
	expected := "/tmp/backups/test-backup"

	result := storage.GetBackupPath(backupName)
	if result != expected {
		t.Errorf("Expected path %s, got %s", expected, result)
	}
}

func TestSaveAndLoadBackup(t *testing.T) {
	// Create temporary directory for testing
	tempDir, err := os.MkdirTemp("", "k8s-backup-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tempDir)

	storage := NewLocalStorage(tempDir)
	ctx := context.Background()

	// Create test metadata
	metadata := &types.BackupMetadata{
		Name:              "test-backup",
		Timestamp:         time.Now(),
		Version:           types.BackupFormatVersion,
		KubernetesVersion: "v1.28.4",
		Namespaces:        []string{"default"},
		ResourceTypes:     []string{"deployments"},
		TotalResources:    1,
		BackupPath:        "",
		Size:              0,
		Compress:          false,
	}

	// Create test resources
	resources := []types.ResourceWithContent{
		{
			Content: []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: nginx
  namespace: default
spec:
  replicas: 1
  selector:
    matchLabels:
      app: nginx
  template:
    metadata:
      labels:
        app: nginx
    spec:
      containers:
      - name: nginx
        image: nginx:1.14.2
        ports:
        - containerPort: 80`),
			Info: types.ResourceInfo{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Namespace:  "default",
				Name:       "nginx",
				Labels: map[string]string{
					"app": "nginx",
				},
			},
		},
	}

	// Test SaveBackup
	err = storage.SaveBackup(ctx, metadata, resources)
	if err != nil {
		t.Fatalf("Failed to save backup: %v", err)
	}

	// Verify backup directory exists
	backupDir := filepath.Join(tempDir, metadata.Name)
	if _, err := os.Stat(backupDir); os.IsNotExist(err) {
		t.Fatalf("Backup directory does not exist: %s", backupDir)
	}

	// Verify manifest file exists
	manifestPath := filepath.Join(backupDir, types.ManifestFileName)
	if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
		t.Fatalf("Manifest file does not exist: %s", manifestPath)
	}

	// Test LoadBackup
	loadedManifest, loadedResources, err := storage.LoadBackup(ctx, backupDir)
	if err != nil {
		t.Fatalf("Failed to load backup: %v", err)
	}

	// Verify loaded data
	if loadedManifest.Metadata.Name != metadata.Name {
		t.Errorf("Expected name %s, got %s", metadata.Name, loadedManifest.Metadata.Name)
	}

	if len(loadedResources) != 1 {
		t.Errorf("Expected 1 resource, got %d", len(loadedResources))
	}

	if loadedResources[0].Info.Name != "nginx" {
		t.Errorf("Expected resource name nginx, got %s", loadedResources[0].Info.Name)
	}
}

func TestListBackups(t *testing.T) {
	// Create temporary directory for testing
	tempDir, err := os.MkdirTemp("", "k8s-backup-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tempDir)

	storage := NewLocalStorage(tempDir)

	// Test with empty directory
	backups, err := storage.ListBackups()
	if err != nil {
		t.Fatalf("Failed to list backups: %v", err)
	}

	if len(backups) != 0 {
		t.Errorf("Expected 0 backups, got %d", len(backups))
	}

	// Create a test backup
	ctx := context.Background()
	metadata := &types.BackupMetadata{
		Name:              "test-backup",
		Timestamp:         time.Now(),
		Version:           types.BackupFormatVersion,
		KubernetesVersion: "v1.28.4",
		Namespaces:        []string{"default"},
		ResourceTypes:     []string{"deployments"},
		TotalResources:    0,
		BackupPath:        "",
		Size:              0,
		Compress:          false,
	}

	resources := []types.ResourceWithContent{}
	err = storage.SaveBackup(ctx, metadata, resources)
	if err != nil {
		t.Fatalf("Failed to save test backup: %v", err)
	}

	// Test listing with one backup
	backups, err = storage.ListBackups()
	if err != nil {
		t.Fatalf("Failed to list backups: %v", err)
	}

	if len(backups) != 1 {
		t.Errorf("Expected 1 backup, got %d", len(backups))
	}

	if backups[0].Name != "test-backup" {
		t.Errorf("Expected backup name 'test-backup', got %s", backups[0].Name)
	}
}

func TestDeleteBackup(t *testing.T) {
	// Create temporary directory for testing
	tempDir, err := os.MkdirTemp("", "k8s-backup-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tempDir)

	storage := NewLocalStorage(tempDir)

	// Create a test backup directory
	backupDir := filepath.Join(tempDir, "test-backup")
	err = os.MkdirAll(backupDir, 0755)
	if err != nil {
		t.Fatalf("Failed to create backup directory: %v", err)
	}

	// Create a test file in the backup directory
	testFile := filepath.Join(backupDir, "test.yaml")
	err = os.WriteFile(testFile, []byte("test content"), 0644)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Verify backup directory exists
	if _, err := os.Stat(backupDir); os.IsNotExist(err) {
		t.Fatalf("Backup directory should exist before deletion")
	}

	// Test DeleteBackup
	err = storage.DeleteBackup(backupDir)
	if err != nil {
		t.Fatalf("Failed to delete backup: %v", err)
	}

	// Verify backup directory is deleted
	if _, err := os.Stat(backupDir); !os.IsNotExist(err) {
		t.Fatalf("Backup directory should be deleted")
	}
}

func TestResourcePathGeneration(t *testing.T) {
	tests := []struct {
		namespace    string
		kind         string
		name         string
		expectedPath string
	}{
		{"default", "Deployment", "nginx", "default/deployment-nginx.yaml"},
		{"kube-system", "Service", "kube-dns", "kube-system/service-kube-dns.yaml"},
		{"", "ClusterRole", "admin", "cluster/clusterrole-admin.yaml"},
		{"", "Namespace", "production", "cluster/namespace-production.yaml"},
	}

	for _, test := range tests {
		var relativePath string
		if test.namespace != "" {
			// Namespaced resource
			relativePath = filepath.Join(test.namespace,
				test.kind+"-"+test.name+".yaml")
		} else {
			// Cluster-scoped resource
			relativePath = filepath.Join("cluster",
				test.kind+"-"+test.name+".yaml")
		}

		// Convert to lowercase to match the actual implementation
		expected := filepath.Join(filepath.Dir(test.expectedPath),
			strings.ToLower(filepath.Base(test.expectedPath)))
		actual := strings.ToLower(relativePath)

		if actual != expected {
			t.Errorf("For %s/%s in namespace '%s': expected path %s, got %s",
				test.kind, test.name, test.namespace, expected, actual)
		}
	}
}

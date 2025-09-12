package types

import (
	"testing"
	"time"
)

func TestGetResourceOrder(t *testing.T) {
	tests := []struct {
		kind     string
		expected int
	}{
		{"Namespace", 6},                        // Index 6 in ResourceOrder
		{"Secret", 7},                           // Index 7 in ResourceOrder
		{"ConfigMap", 8},                        // Index 8 in ResourceOrder
		{"Deployment", 16},                      // Index 16 in ResourceOrder
		{"Service", 14},                         // Index 14 in ResourceOrder
		{"UnknownResource", len(ResourceOrder)}, // Should return high value for unknown
	}

	for _, test := range tests {
		result := GetResourceOrder(test.kind)
		if result != test.expected {
			t.Errorf("GetResourceOrder(%s) = %d, expected %d", test.kind, result, test.expected)
		}
	}
}

func TestIsClusterScoped(t *testing.T) {
	tests := []struct {
		resourceType string
		expected     bool
	}{
		{"clusterroles", true},
		{"namespaces", true},
		{"persistentvolumes", true},
		{"deployments", false},
		{"services", false},
		{"secrets", false},
	}

	for _, test := range tests {
		result := IsClusterScoped(test.resourceType)
		if result != test.expected {
			t.Errorf("IsClusterScoped(%s) = %t, expected %t", test.resourceType, result, test.expected)
		}
	}
}

func TestBackupMetadata(t *testing.T) {
	metadata := BackupMetadata{
		Name:              "test-backup",
		Timestamp:         time.Now(),
		Version:           BackupFormatVersion,
		KubernetesVersion: "v1.28.4",
		Namespaces:        []string{"default", "kube-system"},
		ResourceTypes:     []string{"deployments", "services"},
		TotalResources:    10,
		BackupPath:        "/tmp/backup",
		Size:              1024,
		Compress:          true,
	}

	if metadata.Name != "test-backup" {
		t.Errorf("Expected name 'test-backup', got %s", metadata.Name)
	}

	if metadata.Version != BackupFormatVersion {
		t.Errorf("Expected version %s, got %s", BackupFormatVersion, metadata.Version)
	}

	if len(metadata.Namespaces) != 2 {
		t.Errorf("Expected 2 namespaces, got %d", len(metadata.Namespaces))
	}

	if !metadata.Compress {
		t.Error("Expected Compress to be true")
	}
}

func TestBackupOptions(t *testing.T) {
	options := BackupOptions{
		Namespaces:           []string{"app1", "app2"},
		ResourceTypes:        []string{"deployments", "services"},
		ExcludeNamespaces:    []string{"kube-system"},
		ExcludeResourceTypes: []string{"secrets"},
		OutputPath:           "./backups",
		BackupName:           "my-backup",
		Compress:             true,
	}

	if len(options.Namespaces) != 2 {
		t.Errorf("Expected 2 namespaces, got %d", len(options.Namespaces))
	}

	if options.BackupName != "my-backup" {
		t.Errorf("Expected backup name 'my-backup', got %s", options.BackupName)
	}

	if !options.Compress {
		t.Error("Expected Compress to be true")
	}
}

func TestRestoreOptions(t *testing.T) {
	options := RestoreOptions{
		BackupPath:        "./backups/test-backup",
		Namespaces:        []string{"app1"},
		ResourceTypes:     []string{"deployments"},
		DryRun:            true,
		Wait:              false,
		Timeout:           5 * time.Minute,
		OverwriteExisting: false,
	}

	if options.BackupPath != "./backups/test-backup" {
		t.Errorf("Expected backup path './backups/test-backup', got %s", options.BackupPath)
	}

	if !options.DryRun {
		t.Error("Expected DryRun to be true")
	}

	if options.Timeout != 5*time.Minute {
		t.Errorf("Expected timeout 5m, got %s", options.Timeout)
	}
}

func TestResourceInfo(t *testing.T) {
	info := ResourceInfo{
		APIVersion:   "apps/v1",
		Kind:         "Deployment",
		Namespace:    "default",
		Name:         "nginx",
		RelativePath: "default/deployment-nginx.yaml",
		Labels: map[string]string{
			"app": "nginx",
		},
		Annotations: map[string]string{
			"deployment.kubernetes.io/revision": "1",
		},
	}

	if info.APIVersion != "apps/v1" {
		t.Errorf("Expected APIVersion 'apps/v1', got %s", info.APIVersion)
	}

	if info.Kind != "Deployment" {
		t.Errorf("Expected Kind 'Deployment', got %s", info.Kind)
	}

	if info.Labels["app"] != "nginx" {
		t.Errorf("Expected label app=nginx, got %s", info.Labels["app"])
	}
}

func TestProgress(t *testing.T) {
	progress := Progress{
		Total:     100,
		Completed: 50,
		Current:   "Processing deployments",
		Errors:    []error{},
	}

	if progress.Total != 100 {
		t.Errorf("Expected Total 100, got %d", progress.Total)
	}

	if progress.Completed != 50 {
		t.Errorf("Expected Completed 50, got %d", progress.Completed)
	}

	if progress.Current != "Processing deployments" {
		t.Errorf("Expected Current 'Processing deployments', got %s", progress.Current)
	}

	if len(progress.Errors) != 0 {
		t.Errorf("Expected 0 errors, got %d", len(progress.Errors))
	}
}

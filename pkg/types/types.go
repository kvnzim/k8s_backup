package types

import (
	"time"

	"k8s.io/apimachinery/pkg/runtime"
)

// BackupMetadata contains information about a backup
type BackupMetadata struct {
	Name              string    `json:"name" yaml:"name"`
	Timestamp         time.Time `json:"timestamp" yaml:"timestamp"`
	Version           string    `json:"version" yaml:"version"`
	KubernetesVersion string    `json:"kubernetesVersion" yaml:"kubernetesVersion"`
	Namespaces        []string  `json:"namespaces" yaml:"namespaces"`
	ResourceTypes     []string  `json:"resourceTypes" yaml:"resourceTypes"`
	TotalResources    int       `json:"totalResources" yaml:"totalResources"`
	BackupPath        string    `json:"backupPath" yaml:"backupPath"`
	Size              int64     `json:"size" yaml:"size"`
	Compress          bool      `json:"compress" yaml:"compress"`
}

// BackupOptions contains configuration for backup operations
type BackupOptions struct {
	Namespaces           []string
	ResourceTypes        []string
	ExcludeNamespaces    []string
	ExcludeResourceTypes []string
	OutputPath           string
	BackupName           string
	Compress             bool
}

// RestoreOptions contains configuration for restore operations
type RestoreOptions struct {
	BackupPath        string
	Namespaces        []string
	ResourceTypes     []string
	DryRun            bool
	Wait              bool
	Timeout           time.Duration
	OverwriteExisting bool
}

// ResourceInfo contains metadata about a backed up resource
type ResourceInfo struct {
	APIVersion   string            `json:"apiVersion" yaml:"apiVersion"`
	Kind         string            `json:"kind" yaml:"kind"`
	Namespace    string            `json:"namespace" yaml:"namespace"`
	Name         string            `json:"name" yaml:"name"`
	RelativePath string            `json:"relativePath" yaml:"relativePath"`
	Labels       map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
	Annotations  map[string]string `json:"annotations,omitempty" yaml:"annotations,omitempty"`
}

// BackupManifest contains the complete manifest of a backup
type BackupManifest struct {
	Metadata  BackupMetadata `json:"metadata" yaml:"metadata"`
	Resources []ResourceInfo `json:"resources" yaml:"resources"`
}

// ResourceOrder defines the dependency-ordered restore sequence
var ResourceOrder = []string{
	"CustomResourceDefinition", "ClusterRole", "ClusterRoleBinding",
	"PersistentVolume", "StorageClass", "VolumeSnapshotClass",
	"Namespace",
	"Secret", "ConfigMap", "ServiceAccount", "Role", "RoleBinding", "PersistentVolumeClaim",
	"NetworkPolicy", "Service", "Endpoints",
	"Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob", "ReplicaSet", "Pod",
	"Ingress", "HorizontalPodAutoscaler", "PodDisruptionBudget", "Event",
}

type Progress struct {
	Total     int
	Completed int
	Current   string
	Errors    []error
}

type ProgressCallback func(progress Progress)

type ResourceWithContent struct {
	Object  runtime.Object
	Content []byte
	Info    ResourceInfo
}

// Constants for backup format version and default paths
const (
	BackupFormatVersion = "v1"
	DefaultBackupDir    = "./backups"
	ManifestFileName    = "manifest.yaml"
)

var SupportedResourceTypes = []string{
	"namespaces", "clusterroles", "clusterrolebindings", "persistentvolumes",
	"deployments", "services", "configmaps", "secrets", "persistentvolumeclaims",
	"serviceaccounts", "roles", "rolebindings", "ingresses", "networkpolicies",
	"horizontalpodautoscalers", "poddisruptionbudgets", "statefulsets",
	"daemonsets", "jobs", "cronjobs", "replicasets", "pods",
}

// IsClusterScoped returns true if the resource type is cluster-scoped
func IsClusterScoped(resourceType string) bool {
	clusterScopedTypes := map[string]bool{
		"clusterroles":              true,
		"clusterrolebindings":       true,
		"persistentvolumes":         true,
		"namespaces":                true,
		"customresourcedefinitions": true,
		"storageclasses":            true,
		"volumesnapshotclasses":     true,
	}
	return clusterScopedTypes[resourceType]
}

var resourceOrderMap = make(map[string]int)

func init() {
	for i, kind := range ResourceOrder {
		resourceOrderMap[kind] = i
	}
}

func GetResourceOrder(kind string) int {
	if order, exists := resourceOrderMap[kind]; exists {
		return order
	}
	return len(ResourceOrder)
}

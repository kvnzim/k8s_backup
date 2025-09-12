package restore

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/yaml"

	"k8s-backup/pkg/k8s"
	"k8s-backup/pkg/storage"
	"k8s-backup/pkg/types"
)

// RestoreResult contains the results of a restore operation
type RestoreResult struct {
	ProcessedResources int
	SkippedResources   int
	Namespaces         []string
	ResourceTypes      []string
	Errors             []error
	Duration           time.Duration
}

// Manager handles restore operations
type Manager struct {
	k8sClient *k8s.Client
	storage   storage.Storage
}

// NewManager creates a new restore manager
func NewManager(k8sClient *k8s.Client, storage storage.Storage) *Manager {
	return &Manager{
		k8sClient: k8sClient,
		storage:   storage,
	}
}

// RestoreBackup performs a restore operation with the given options
func (m *Manager) RestoreBackup(ctx context.Context, options *types.RestoreOptions, progressCallback types.ProgressCallback) (*RestoreResult, error) {
	startTime := time.Now()
	log.Printf("Starting restore from: %s", options.BackupPath)

	// Load backup
	manifest, resources, err := m.storage.LoadBackup(ctx, options.BackupPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load backup: %w", err)
	}

	log.Printf("Loaded backup: %s (%d resources)", manifest.Metadata.Name, len(resources))

	// Filter resources based on options
	filteredResources := m.filterResources(resources, options)
	log.Printf("Filtered to %d resources for restore", len(filteredResources))

	if len(filteredResources) == 0 {
		return &RestoreResult{
			ProcessedResources: 0,
			SkippedResources:   len(resources),
			Duration:           time.Since(startTime),
		}, nil
	}

	// Sort resources by dependency order
	sortedResources := m.sortResourcesByDependency(filteredResources)

	// Initialize progress tracking
	progress := types.Progress{
		Total:     len(sortedResources),
		Completed: 0,
		Current:   "Starting restore...",
		Errors:    []error{},
	}

	result := &RestoreResult{
		ProcessedResources: 0,
		SkippedResources:   len(resources) - len(filteredResources),
		Namespaces:         []string{},
		ResourceTypes:      []string{},
		Errors:             []error{},
		Duration:           0,
	}

	// Track namespaces and resource types
	namespacesSet := sets.NewString()
	resourceTypesSet := sets.NewString()

	// Apply resources in dependency order
	for i, resource := range sortedResources {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}

		progress.Current = fmt.Sprintf("Restoring %s/%s", resource.Info.Kind, resource.Info.Name)
		progress.Completed = i
		if progressCallback != nil {
			progressCallback(progress)
		}

		// Parse the resource object
		obj, err := m.parseResourceObject(resource.Content)
		if err != nil {
			err = fmt.Errorf("failed to parse %s/%s: %w", resource.Info.Kind, resource.Info.Name, err)
			result.Errors = append(result.Errors, err)
			progress.Errors = append(progress.Errors, err)
			continue
		}

		// Apply the resource
		if !options.DryRun && m.k8sClient != nil {
			err = m.k8sClient.ApplyResource(ctx, obj, resource.Info.Namespace, options.DryRun)
			if err != nil {
				if !options.OverwriteExisting && strings.Contains(err.Error(), "already exists") {
					log.Printf("Skipping existing resource: %s/%s", resource.Info.Kind, resource.Info.Name)
					result.SkippedResources++
					continue
				}

				err = fmt.Errorf("failed to apply %s/%s: %w", resource.Info.Kind, resource.Info.Name, err)
				result.Errors = append(result.Errors, err)
				progress.Errors = append(progress.Errors, err)
				continue
			}
		}

		// Track success
		result.ProcessedResources++
		namespacesSet.Insert(resource.Info.Namespace)
		resourceTypesSet.Insert(strings.ToLower(resource.Info.Kind))

		// Wait for resource to be ready if requested
		if options.Wait && !options.DryRun && m.k8sClient != nil {
			if err := m.waitForResourceReady(ctx, obj, resource.Info, options.Timeout); err != nil {
				log.Printf("Warning: Resource %s/%s may not be fully ready: %v", resource.Info.Kind, resource.Info.Name, err)
			}
		}
	}

	// Final progress update
	progress.Completed = len(sortedResources)
	progress.Current = "Restore completed"
	if progressCallback != nil {
		progressCallback(progress)
	}

	// Finalize result
	result.Namespaces = namespacesSet.List()
	result.ResourceTypes = resourceTypesSet.List()
	result.Duration = time.Since(startTime)

	sort.Strings(result.Namespaces)
	sort.Strings(result.ResourceTypes)

	log.Printf("Restore completed: %d processed, %d skipped, %d errors",
		result.ProcessedResources, result.SkippedResources, len(result.Errors))

	return result, nil
}

// filterResources filters resources based on restore options
func (m *Manager) filterResources(resources []types.ResourceWithContent, options *types.RestoreOptions) []types.ResourceWithContent {
	// Create filter sets
	namespacesFilter := sets.NewString(options.Namespaces...)
	resourceTypesFilter := sets.NewString(options.ResourceTypes...)

	var filtered []types.ResourceWithContent

	for _, resource := range resources {
		// Filter by namespace if specified
		if namespacesFilter.Len() > 0 {
			if resource.Info.Namespace == "" {
				// Cluster-scoped resource - include if no namespace filter or if "cluster" is specified
				if !namespacesFilter.Has("") && !namespacesFilter.Has("cluster") {
					continue
				}
			} else {
				// Namespaced resource - include only if namespace matches
				if !namespacesFilter.Has(resource.Info.Namespace) {
					continue
				}
			}
		}

		// Filter by resource type if specified
		if resourceTypesFilter.Len() > 0 {
			resourceType := strings.ToLower(resource.Info.Kind)
			// Also check plural forms
			resourceTypePlural := m.getResourceTypePlural(resourceType)
			if !resourceTypesFilter.Has(resourceType) && !resourceTypesFilter.Has(resourceTypePlural) {
				continue
			}
		}

		filtered = append(filtered, resource)
	}

	return filtered
}

// sortResourcesByDependency sorts resources by their dependency order
func (m *Manager) sortResourcesByDependency(resources []types.ResourceWithContent) []types.ResourceWithContent {
	// Create a copy to avoid modifying the original slice
	sorted := make([]types.ResourceWithContent, len(resources))
	copy(sorted, resources)

	// Sort by dependency order
	sort.Slice(sorted, func(i, j int) bool {
		orderI := types.GetResourceOrder(sorted[i].Info.Kind)
		orderJ := types.GetResourceOrder(sorted[j].Info.Kind)

		if orderI != orderJ {
			return orderI < orderJ
		}

		// If same order, sort by namespace then name
		if sorted[i].Info.Namespace != sorted[j].Info.Namespace {
			return sorted[i].Info.Namespace < sorted[j].Info.Namespace
		}

		return sorted[i].Info.Name < sorted[j].Info.Name
	})

	return sorted
}

// parseResourceObject parses YAML content into a Kubernetes runtime.Object
func (m *Manager) parseResourceObject(content []byte) (runtime.Object, error) {
	// Parse YAML to determine the resource type
	var obj map[string]interface{}
	if err := yaml.Unmarshal(content, &obj); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	apiVersion, ok := obj["apiVersion"].(string)
	if !ok {
		return nil, fmt.Errorf("missing or invalid apiVersion")
	}

	kind, ok := obj["kind"].(string)
	if !ok {
		return nil, fmt.Errorf("missing or invalid kind")
	}

	// Create the appropriate object type based on kind
	runtimeObj, err := m.createRuntimeObject(apiVersion, kind)
	if err != nil {
		return nil, fmt.Errorf("failed to create runtime object for %s: %w", kind, err)
	}

	// Unmarshal into the typed object
	if err := yaml.Unmarshal(content, runtimeObj); err != nil {
		return nil, fmt.Errorf("failed to unmarshal into %s: %w", kind, err)
	}

	return runtimeObj, nil
}

// createRuntimeObject creates an appropriate runtime.Object based on apiVersion and kind
func (m *Manager) createRuntimeObject(apiVersion, kind string) (runtime.Object, error) {
	// Use unstructured.Unstructured for all resource types
	// This is the recommended approach for dynamic resource handling
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(apiVersion)
	obj.SetKind(kind)
	return obj, nil
}

// waitForResourceReady waits for a resource to become ready (simplified implementation)
func (m *Manager) waitForResourceReady(ctx context.Context, obj runtime.Object, info types.ResourceInfo, timeout time.Duration) error {
	// This is a simplified implementation - in practice, you would implement
	// specific readiness checks for different resource types

	// For now, just wait a short time for basic resources to be created
	shortTimeout := 30 * time.Second
	if timeout < shortTimeout {
		shortTimeout = timeout
	}

	select {
	case <-time.After(shortTimeout):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// getResourceTypePlural returns the plural form of a resource type
func (m *Manager) getResourceTypePlural(resourceType string) string {
	pluralMapping := map[string]string{
		"namespace":             "namespaces",
		"secret":                "secrets",
		"configmap":             "configmaps",
		"service":               "services",
		"serviceaccount":        "serviceaccounts",
		"persistentvolume":      "persistentvolumes",
		"persistentvolumeclaim": "persistentvolumeclaims",
		"deployment":            "deployments",
		"statefulset":           "statefulsets",
		"daemonset":             "daemonsets",
		"job":                   "jobs",
		"cronjob":               "cronjobs",
		"role":                  "roles",
		"rolebinding":           "rolebindings",
		"clusterrole":           "clusterroles",
		"clusterrolebinding":    "clusterrolebindings",
		"ingress":               "ingresses",
		"networkpolicy":         "networkpolicies",
		"poddisruptionbudget":   "poddisruptionbudgets",
		"storageclass":          "storageclasses",
	}

	if plural, exists := pluralMapping[resourceType]; exists {
		return plural
	}

	// Default: add 's' to the end
	return resourceType + "s"
}

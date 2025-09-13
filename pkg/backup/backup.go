package backup

import (
	"context"
	"fmt"
	"log"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"

	"k8s-backup/pkg/k8s"
	"k8s-backup/pkg/storage"
	"k8s-backup/pkg/types"
)

// Manager handles backup operations
type Manager struct {
	k8sClient *k8s.Client
	storage   storage.Storage
}

// NewManager creates a new backup manager
func NewManager(k8sClient *k8s.Client, storage storage.Storage) *Manager {
	return &Manager{
		k8sClient: k8sClient,
		storage:   storage,
	}
}

// CreateBackup performs a backup operation with the given options
func (m *Manager) CreateBackup(ctx context.Context, options *types.BackupOptions, progressCallback types.ProgressCallback) (*types.BackupMetadata, error) {
	log.Printf("Starting backup: %s", options.BackupName)

	// Get Kubernetes version
	k8sVersion, err := m.k8sClient.GetServerVersion()
	if err != nil {
		return nil, fmt.Errorf("failed to get Kubernetes version: %w", err)
	}

	// Get all namespaces to determine what to backup
	namespacesToBackup, err := m.getNamespacesToBackup(ctx, options)
	if err != nil {
		return nil, fmt.Errorf("failed to determine namespaces to backup: %w", err)
	}

	// Get resource types to backup
	resourceTypesToBackup := m.getResourceTypesToBackup(options)

	// Estimate total resources for better memory allocation
	estimatedTotal := m.estimateResourceCount(len(namespacesToBackup), resourceTypesToBackup)

	// Pre-allocate slices for better memory efficiency
	allResources := make([]types.ResourceWithContent, 0, estimatedTotal)
	var errors []error

	progress := types.Progress{
		Total:     estimatedTotal,
		Completed: 0,
		Current:   "Starting backup...",
		Errors:    []error{},
	}

	if progressCallback != nil {
		progressCallback(progress)
	}

	// Use channels for concurrent processing
	resourceChan := make(chan []types.ResourceWithContent, len(namespacesToBackup)+1)
	errorChan := make(chan []error, len(namespacesToBackup)+1)
	var wg sync.WaitGroup

	// Backup cluster-scoped resources concurrently
	wg.Add(1)
	go func() {
		defer wg.Done()
		clusterResources, clusterErrors := m.backupClusterScopedResources(ctx, resourceTypesToBackup)
		resourceChan <- clusterResources
		errorChan <- clusterErrors
	}()

	// Backup namespaced resources concurrently
	for _, ns := range namespacesToBackup {
		wg.Add(1)
		go func(namespace string) {
			defer wg.Done()
			if ctx.Err() != nil {
				return
			}
			nsResources, nsErrors := m.backupNamespacedResources(ctx, namespace, resourceTypesToBackup)
			resourceChan <- nsResources
			errorChan <- nsErrors
		}(ns)
	}

	// Wait for all goroutines and close channels
	go func() {
		wg.Wait()
		close(resourceChan)
		close(errorChan)
	}()

	// Collect results
	completed := 0
	for {
		select {
		case resources, ok := <-resourceChan:
			if !ok {
				resourceChan = nil
			} else {
				allResources = append(allResources, resources...)
				completed += len(resources)
				progress.Completed = completed
				progress.Current = fmt.Sprintf("Collected %d resources", completed)
				if progressCallback != nil {
					progressCallback(progress)
				}
			}
		case errs, ok := <-errorChan:
			if !ok {
				errorChan = nil
			} else {
				errors = append(errors, errs...)
			}
		}

		if resourceChan == nil && errorChan == nil {
			break
		}
	}

	// Create backup metadata
	metadata := &types.BackupMetadata{
		Name:              options.BackupName,
		Timestamp:         time.Now(),
		Version:           types.BackupFormatVersion,
		KubernetesVersion: k8sVersion,
		Namespaces:        namespacesToBackup,
		ResourceTypes:     resourceTypesToBackup,
		TotalResources:    len(allResources),
		BackupPath:        "",
		Size:              0,
		Compress:          options.Compress,
	}

	// Save backup
	progress.Current = "Saving backup files..."
	if progressCallback != nil {
		progressCallback(progress)
	}

	err = m.storage.SaveBackup(ctx, metadata, allResources)
	if err != nil {
		return nil, fmt.Errorf("failed to save backup: %w", err)
	}

	// Final progress report
	progress.Current = "Backup completed"
	if progressCallback != nil {
		progressCallback(progress)
	}

	log.Printf("Backup completed: %s (%d resources)", metadata.Name, metadata.TotalResources)

	if len(errors) > 0 {
		log.Printf("Backup completed with %d warnings", len(errors))
	}

	return metadata, nil
}

// estimateResourceCount provides a rough estimate for memory pre-allocation
func (m *Manager) estimateResourceCount(namespaceCount int, resourceTypes []string) int {
	// Rough estimates based on typical cluster sizes
	clusterScopedCount := 0
	namespacedCount := 0

	for _, rt := range resourceTypes {
		if types.IsClusterScoped(rt) {
			switch rt {
			case "namespaces":
				clusterScopedCount += namespaceCount + 10 // system namespaces
			case "clusterroles", "clusterrolebindings":
				clusterScopedCount += 50 // typical cluster roles
			default:
				clusterScopedCount += 20 // other cluster resources
			}
		} else {
			// Estimate per namespace
			switch rt {
			case "deployments", "services":
				namespacedCount += 10 // typical apps per namespace
			case "configmaps", "secrets":
				namespacedCount += 20 // more config resources
			default:
				namespacedCount += 5 // other resources
			}
		}
	}

	estimated := clusterScopedCount + (namespacedCount * namespaceCount)
	if estimated < 100 {
		return 100 // minimum allocation
	}
	return estimated
}

// getNamespacesToBackup determines which namespaces to include in the backup
func (m *Manager) getNamespacesToBackup(ctx context.Context, options *types.BackupOptions) ([]string, error) {
	// Get all namespaces
	allNamespaces, err := m.k8sClient.Clientset().CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	var namespacesToBackup []string
	excludeMap := make(map[string]bool)
	for _, ns := range options.ExcludeNamespaces {
		excludeMap[ns] = true
	}

	// If specific namespaces are specified, use only those
	if len(options.Namespaces) > 0 {
		for _, ns := range options.Namespaces {
			if !excludeMap[ns] {
				namespacesToBackup = append(namespacesToBackup, ns)
			}
		}
	} else {
		// Include all namespaces except excluded ones
		for _, ns := range allNamespaces.Items {
			if !excludeMap[ns.Name] {
				namespacesToBackup = append(namespacesToBackup, ns.Name)
			}
		}
	}

	sort.Strings(namespacesToBackup)
	return namespacesToBackup, nil
}

// getResourceTypesToBackup determines which resource types to include
func (m *Manager) getResourceTypesToBackup(options *types.BackupOptions) []string {
	excludeMap := make(map[string]bool)
	for _, rt := range options.ExcludeResourceTypes {
		excludeMap[rt] = true
	}

	var resourceTypes []string
	if len(options.ResourceTypes) > 0 {
		// Use specified resource types
		for _, rt := range options.ResourceTypes {
			if !excludeMap[rt] {
				resourceTypes = append(resourceTypes, rt)
			}
		}
	} else {
		// Use all supported resource types
		for _, rt := range types.SupportedResourceTypes {
			if !excludeMap[rt] {
				resourceTypes = append(resourceTypes, rt)
			}
		}
	}

	return resourceTypes
}

func (m *Manager) backupClusterScopedResources(ctx context.Context, resourceTypes []string) ([]types.ResourceWithContent, []error) {
	resources := make([]types.ResourceWithContent, 0, 100) // Pre-allocate
	var errors []error

	// Create a map for faster lookups
	typeMap := make(map[string]bool, len(resourceTypes))
	for _, rt := range resourceTypes {
		if types.IsClusterScoped(rt) {
			typeMap[rt] = true
		}
	}

	// Process only requested cluster-scoped resource types
	if typeMap["namespaces"] {
		if res, err := m.backupNamespaces(ctx); err != nil {
			errors = append(errors, fmt.Errorf("failed to backup namespaces: %w", err))
		} else {
			resources = append(resources, res...)
		}
	}

	if typeMap["persistentvolumes"] {
		if res, err := m.backupPersistentVolumes(ctx); err != nil {
			errors = append(errors, fmt.Errorf("failed to backup persistent volumes: %w", err))
		} else {
			resources = append(resources, res...)
		}
	}

	if typeMap["clusterroles"] {
		if res, err := m.backupClusterRoles(ctx); err != nil {
			errors = append(errors, fmt.Errorf("failed to backup cluster roles: %w", err))
		} else {
			resources = append(resources, res...)
		}
	}

	if typeMap["clusterrolebindings"] {
		if res, err := m.backupClusterRoleBindings(ctx); err != nil {
			errors = append(errors, fmt.Errorf("failed to backup cluster role bindings: %w", err))
		} else {
			resources = append(resources, res...)
		}
	}

	if typeMap["storageclasses"] {
		if res, err := m.backupStorageClasses(ctx); err != nil {
			errors = append(errors, fmt.Errorf("failed to backup storage classes: %w", err))
		} else {
			resources = append(resources, res...)
		}
	}

	return resources, errors
}

func (m *Manager) backupNamespacedResources(ctx context.Context, namespace string, resourceTypes []string) ([]types.ResourceWithContent, []error) {
	resources := make([]types.ResourceWithContent, 0, 50) // Pre-allocate
	var errors []error

	// Create backup function map for efficient lookups
	backupFuncs := map[string]func(context.Context, string) ([]types.ResourceWithContent, error){
		"deployments":            m.backupDeployments,
		"services":               m.backupServices,
		"configmaps":             m.backupConfigMaps,
		"secrets":                m.backupSecrets,
		"persistentvolumeclaims": m.backupPersistentVolumeClaims,
		"serviceaccounts":        m.backupServiceAccounts,
		"roles":                  m.backupRoles,
		"rolebindings":           m.backupRoleBindings,
		"ingresses":              m.backupIngresses,
		"networkpolicies":        m.backupNetworkPolicies,
		"statefulsets":           m.backupStatefulSets,
		"daemonsets":             m.backupDaemonSets,
		"jobs":                   m.backupJobs,
		"cronjobs":               m.backupCronJobs,
		"poddisruptionbudgets":   m.backupPodDisruptionBudgets,
	}

	// Process only requested namespaced resource types
	for _, resourceType := range resourceTypes {
		if types.IsClusterScoped(resourceType) {
			continue
		}

		if backupFunc, exists := backupFuncs[resourceType]; exists {
			if res, err := backupFunc(ctx, namespace); err != nil {
				errors = append(errors, fmt.Errorf("failed to backup %s in %s: %w", resourceType, namespace, err))
			} else {
				resources = append(resources, res...)
			}
		}
	}

	return resources, errors
}

func (m *Manager) convertToResourceWithContent(obj runtime.Object, namespace, kind string) (types.ResourceWithContent, error) {
	// Clean up the object for backup (remove runtime fields)
	m.cleanObject(obj)

	// Get API info for this kind
	apiInfo := m.getAPIInfo(kind)

	// Set the ObjectKind properly (client-go objects don't have this populated by default)
	obj.GetObjectKind().SetGroupVersionKind(schema.GroupVersionKind{
		Group:   apiInfo.Group,
		Version: apiInfo.Version,
		Kind:    kind,
	})

	// Convert to YAML
	content, err := yaml.Marshal(obj)
	if err != nil {
		return types.ResourceWithContent{}, fmt.Errorf("failed to marshal to YAML: %w", err)
	}

	// Get object metadata
	metaObj, ok := obj.(metav1.Object)
	if !ok {
		return types.ResourceWithContent{}, fmt.Errorf("object does not implement metav1.Object")
	}

	info := types.ResourceInfo{
		APIVersion:  apiInfo.APIVersion,
		Kind:        kind,
		Namespace:   namespace,
		Name:        metaObj.GetName(),
		Labels:      metaObj.GetLabels(),
		Annotations: metaObj.GetAnnotations(),
	}

	return types.ResourceWithContent{
		Object:  obj,
		Content: content,
		Info:    info,
	}, nil
}

// APIInfo holds API version information for different resource types
type APIInfo struct {
	Group      string
	Version    string
	APIVersion string
}

// getAPIInfo returns API information for a given resource kind
func (m *Manager) getAPIInfo(kind string) APIInfo {
	// Define the mapping once, eliminating duplicate switch cases
	resourceMap := map[string]APIInfo{
		// Core resources (no group)
		"Namespace":             {Group: "", Version: "v1", APIVersion: "v1"},
		"Service":               {Group: "", Version: "v1", APIVersion: "v1"},
		"ConfigMap":             {Group: "", Version: "v1", APIVersion: "v1"},
		"Secret":                {Group: "", Version: "v1", APIVersion: "v1"},
		"PersistentVolumeClaim": {Group: "", Version: "v1", APIVersion: "v1"},
		"PersistentVolume":      {Group: "", Version: "v1", APIVersion: "v1"},
		"ServiceAccount":        {Group: "", Version: "v1", APIVersion: "v1"},

		// Apps group
		"Deployment":  {Group: "apps", Version: "v1", APIVersion: "apps/v1"},
		"StatefulSet": {Group: "apps", Version: "v1", APIVersion: "apps/v1"},
		"DaemonSet":   {Group: "apps", Version: "v1", APIVersion: "apps/v1"},

		// Batch group
		"Job":     {Group: "batch", Version: "v1", APIVersion: "batch/v1"},
		"CronJob": {Group: "batch", Version: "v1", APIVersion: "batch/v1"},

		// RBAC group
		"Role":               {Group: "rbac.authorization.k8s.io", Version: "v1", APIVersion: "rbac.authorization.k8s.io/v1"},
		"RoleBinding":        {Group: "rbac.authorization.k8s.io", Version: "v1", APIVersion: "rbac.authorization.k8s.io/v1"},
		"ClusterRole":        {Group: "rbac.authorization.k8s.io", Version: "v1", APIVersion: "rbac.authorization.k8s.io/v1"},
		"ClusterRoleBinding": {Group: "rbac.authorization.k8s.io", Version: "v1", APIVersion: "rbac.authorization.k8s.io/v1"},

		// Networking group
		"Ingress":       {Group: "networking.k8s.io", Version: "v1", APIVersion: "networking.k8s.io/v1"},
		"NetworkPolicy": {Group: "networking.k8s.io", Version: "v1", APIVersion: "networking.k8s.io/v1"},

		// Storage group
		"StorageClass": {Group: "storage.k8s.io", Version: "v1", APIVersion: "storage.k8s.io/v1"},

		// Policy group
		"PodDisruptionBudget": {Group: "policy", Version: "v1", APIVersion: "policy/v1"},
	}

	if info, exists := resourceMap[kind]; exists {
		return info
	}

	// Default to core/v1 for unknown resources
	return APIInfo{Group: "", Version: "v1", APIVersion: "v1"}
}

// cleanObject removes runtime fields that shouldn't be included in backups
func (m *Manager) cleanObject(obj runtime.Object) {
	if metaObj, ok := obj.(metav1.Object); ok {
		// Remove fields that are set by the cluster
		metaObj.SetResourceVersion("")
		metaObj.SetUID("")
		metaObj.SetGeneration(0)
		metaObj.SetSelfLink("")
		metaObj.SetCreationTimestamp(metav1.Time{})
		metaObj.SetDeletionTimestamp(nil)
		metaObj.SetDeletionGracePeriodSeconds(nil)

		// Remove managed fields
		metaObj.SetManagedFields(nil)

		// Clean up annotations
		annotations := metaObj.GetAnnotations()
		if annotations != nil {
			// Remove kubectl annotations that are added during apply
			delete(annotations, "kubectl.kubernetes.io/last-applied-configuration")

			// Remove deployment annotations that are runtime-specific
			for key := range annotations {
				if strings.HasPrefix(key, "deployment.kubernetes.io/") ||
					strings.HasPrefix(key, "autoscaling.alpha.kubernetes.io/") {
					delete(annotations, key)
				}
			}

			metaObj.SetAnnotations(annotations)
		}
	}
}

// Generic backup function - eliminates 300+ lines of repetitive code
func (m *Manager) backupResources(ctx context.Context, namespace, kind string, listFunc func() (interface{}, error), shouldSkip func(interface{}) bool) ([]types.ResourceWithContent, error) {
	result, err := listFunc()
	if err != nil {
		return nil, err
	}

	// Use reflection to handle different list types
	items := reflect.ValueOf(result).Elem().FieldByName("Items")
	if !items.IsValid() {
		return nil, fmt.Errorf("invalid list result for %s", kind)
	}

	resources := make([]types.ResourceWithContent, 0, items.Len())
	for i := 0; i < items.Len(); i++ {
		item := items.Index(i).Addr().Interface().(runtime.Object)

		// Apply skip logic if provided
		if shouldSkip != nil && shouldSkip(item) {
			continue
		}

		resource, err := m.convertToResourceWithContent(item, namespace, kind)
		if err != nil {
			return nil, fmt.Errorf("failed to convert %s: %w", kind, err)
		}
		resources = append(resources, resource)
	}

	return resources, nil
}

// Namespace backup function (cluster-scoped)
func (m *Manager) backupNamespaces(ctx context.Context) ([]types.ResourceWithContent, error) {
	return m.backupResources(ctx, "", "Namespace",
		func() (interface{}, error) {
			return m.k8sClient.Clientset().CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
		}, nil)
}

// Simple wrappers using generic function - replaces 40+ lines with 12 lines
func (m *Manager) backupDeployments(ctx context.Context, namespace string) ([]types.ResourceWithContent, error) {
	return m.backupResources(ctx, namespace, "Deployment",
		func() (interface{}, error) {
			return m.k8sClient.Clientset().AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
		}, nil)
}

func (m *Manager) backupServices(ctx context.Context, namespace string) ([]types.ResourceWithContent, error) {
	return m.backupResources(ctx, namespace, "Service",
		func() (interface{}, error) {
			return m.k8sClient.Clientset().CoreV1().Services(namespace).List(ctx, metav1.ListOptions{})
		}, nil)
}

func (m *Manager) backupConfigMaps(ctx context.Context, namespace string) ([]types.ResourceWithContent, error) {
	return m.backupResources(ctx, namespace, "ConfigMap",
		func() (interface{}, error) {
			return m.k8sClient.Clientset().CoreV1().ConfigMaps(namespace).List(ctx, metav1.ListOptions{})
		}, nil)
}

func (m *Manager) backupSecrets(ctx context.Context, namespace string) ([]types.ResourceWithContent, error) {
	return m.backupResources(ctx, namespace, "Secret",
		func() (interface{}, error) {
			return m.k8sClient.Clientset().CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{})
		},
		func(obj interface{}) bool {
			if secret, ok := obj.(*corev1.Secret); ok {
				return secret.Type == "kubernetes.io/service-account-token"
			}
			return false
		})
}

func (m *Manager) backupPersistentVolumeClaims(ctx context.Context, namespace string) ([]types.ResourceWithContent, error) {
	return m.backupResources(ctx, namespace, "PersistentVolumeClaim",
		func() (interface{}, error) {
			return m.k8sClient.Clientset().CoreV1().PersistentVolumeClaims(namespace).List(ctx, metav1.ListOptions{})
		}, nil)
}

func (m *Manager) backupPersistentVolumes(ctx context.Context) ([]types.ResourceWithContent, error) {
	return m.backupResources(ctx, "", "PersistentVolume",
		func() (interface{}, error) {
			return m.k8sClient.Clientset().CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
		}, nil)
}

func (m *Manager) backupServiceAccounts(ctx context.Context, namespace string) ([]types.ResourceWithContent, error) {
	return m.backupResources(ctx, namespace, "ServiceAccount",
		func() (interface{}, error) {
			return m.k8sClient.Clientset().CoreV1().ServiceAccounts(namespace).List(ctx, metav1.ListOptions{})
		},
		func(obj interface{}) bool {
			if sa, ok := obj.(*corev1.ServiceAccount); ok {
				return sa.Name == "default"
			}
			return false
		})
}

func (m *Manager) backupRoles(ctx context.Context, namespace string) ([]types.ResourceWithContent, error) {
	return m.backupResources(ctx, namespace, "Role",
		func() (interface{}, error) {
			return m.k8sClient.Clientset().RbacV1().Roles(namespace).List(ctx, metav1.ListOptions{})
		}, nil)
}

func (m *Manager) backupRoleBindings(ctx context.Context, namespace string) ([]types.ResourceWithContent, error) {
	return m.backupResources(ctx, namespace, "RoleBinding",
		func() (interface{}, error) {
			return m.k8sClient.Clientset().RbacV1().RoleBindings(namespace).List(ctx, metav1.ListOptions{})
		}, nil)
}

func (m *Manager) backupClusterRoles(ctx context.Context) ([]types.ResourceWithContent, error) {
	return m.backupResources(ctx, "", "ClusterRole",
		func() (interface{}, error) {
			return m.k8sClient.Clientset().RbacV1().ClusterRoles().List(ctx, metav1.ListOptions{})
		},
		func(obj interface{}) bool {
			if cr, ok := obj.(*rbacv1.ClusterRole); ok {
				return strings.HasPrefix(cr.Name, "system:")
			}
			return false
		})
}

func (m *Manager) backupClusterRoleBindings(ctx context.Context) ([]types.ResourceWithContent, error) {
	return m.backupResources(ctx, "", "ClusterRoleBinding",
		func() (interface{}, error) {
			return m.k8sClient.Clientset().RbacV1().ClusterRoleBindings().List(ctx, metav1.ListOptions{})
		},
		func(obj interface{}) bool {
			if crb, ok := obj.(*rbacv1.ClusterRoleBinding); ok {
				return strings.HasPrefix(crb.Name, "system:")
			}
			return false
		})
}

func (m *Manager) backupIngresses(ctx context.Context, namespace string) ([]types.ResourceWithContent, error) {
	return m.backupResources(ctx, namespace, "Ingress",
		func() (interface{}, error) {
			return m.k8sClient.Clientset().NetworkingV1().Ingresses(namespace).List(ctx, metav1.ListOptions{})
		}, nil)
}

func (m *Manager) backupNetworkPolicies(ctx context.Context, namespace string) ([]types.ResourceWithContent, error) {
	return m.backupResources(ctx, namespace, "NetworkPolicy",
		func() (interface{}, error) {
			return m.k8sClient.Clientset().NetworkingV1().NetworkPolicies(namespace).List(ctx, metav1.ListOptions{})
		}, nil)
}

// All remaining functions simplified to one-liners using generic function
func (m *Manager) backupStatefulSets(ctx context.Context, namespace string) ([]types.ResourceWithContent, error) {
	return m.backupResources(ctx, namespace, "StatefulSet",
		func() (interface{}, error) {
			return m.k8sClient.Clientset().AppsV1().StatefulSets(namespace).List(ctx, metav1.ListOptions{})
		}, nil)
}

func (m *Manager) backupDaemonSets(ctx context.Context, namespace string) ([]types.ResourceWithContent, error) {
	return m.backupResources(ctx, namespace, "DaemonSet",
		func() (interface{}, error) {
			return m.k8sClient.Clientset().AppsV1().DaemonSets(namespace).List(ctx, metav1.ListOptions{})
		}, nil)
}

func (m *Manager) backupJobs(ctx context.Context, namespace string) ([]types.ResourceWithContent, error) {
	return m.backupResources(ctx, namespace, "Job",
		func() (interface{}, error) {
			return m.k8sClient.Clientset().BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{})
		}, nil)
}

func (m *Manager) backupCronJobs(ctx context.Context, namespace string) ([]types.ResourceWithContent, error) {
	return m.backupResources(ctx, namespace, "CronJob",
		func() (interface{}, error) {
			return m.k8sClient.Clientset().BatchV1().CronJobs(namespace).List(ctx, metav1.ListOptions{})
		}, nil)
}

func (m *Manager) backupStorageClasses(ctx context.Context) ([]types.ResourceWithContent, error) {
	return m.backupResources(ctx, "", "StorageClass",
		func() (interface{}, error) {
			return m.k8sClient.Clientset().StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
		}, nil)
}

func (m *Manager) backupPodDisruptionBudgets(ctx context.Context, namespace string) ([]types.ResourceWithContent, error) {
	return m.backupResources(ctx, namespace, "PodDisruptionBudget",
		func() (interface{}, error) {
			return m.k8sClient.Clientset().PolicyV1().PodDisruptionBudgets(namespace).List(ctx, metav1.ListOptions{})
		}, nil)
}

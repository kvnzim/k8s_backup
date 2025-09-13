package backup

import (
	"context"
	"fmt"
	"log"
	"reflect"
	"sort"
	"strings"
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

	// Backup cluster-scoped resources first
	clusterResources, clusterErrors := m.backupClusterScopedResources(ctx, resourceTypesToBackup)
	allResources = append(allResources, clusterResources...)
	errors = append(errors, clusterErrors...)
	m.updateProgress(&progress, len(clusterResources), fmt.Sprintf("Backed up %d cluster resources", len(clusterResources)), progressCallback)

	// Backup namespaced resources
	for _, ns := range namespacesToBackup {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		nsResources, nsErrors := m.backupNamespacedResources(ctx, ns, resourceTypesToBackup)
		allResources = append(allResources, nsResources...)
		errors = append(errors, nsErrors...)
		m.updateProgress(&progress, len(allResources), fmt.Sprintf("Backed up namespace: %s (%d resources)", ns, len(nsResources)), progressCallback)
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
	m.updateProgress(&progress, progress.Completed, "Saving backup files...", progressCallback)

	err = m.storage.SaveBackup(ctx, metadata, allResources)
	if err != nil {
		return nil, fmt.Errorf("failed to save backup: %w", err)
	}

	// Final progress report
	m.updateProgress(&progress, progress.Completed, "Backup completed", progressCallback)

	log.Printf("Backup completed: %s (%d resources)", metadata.Name, metadata.TotalResources)

	if len(errors) > 0 {
		log.Printf("Backup completed with %d warnings", len(errors))
	}

	return metadata, nil
}

// updateProgress centralizes progress update logic
func (m *Manager) updateProgress(progress *types.Progress, completed int, message string, callback types.ProgressCallback) {
	progress.Completed = completed
	progress.Current = message
	if callback != nil {
		callback(*progress)
	}
}

// estimateResourceCount provides memory pre-allocation estimates
func (m *Manager) estimateResourceCount(namespaceCount int, resourceTypes []string) int {
	estimates := map[string]int{
		// Cluster-scoped resources
		"namespaces":          namespaceCount + 10,
		"clusterroles":        50,
		"clusterrolebindings": 50,
		"persistentvolumes":   20,
		"storageclasses":      20,
		// Namespaced resources (per namespace)
		"deployments":            10,
		"services":               10,
		"configmaps":             20,
		"secrets":                20,
		"persistentvolumeclaims": 5,
		"serviceaccounts":        5,
		"roles":                  5,
		"rolebindings":           5,
		"ingresses":              5,
		"networkpolicies":        5,
		"statefulsets":           5,
		"daemonsets":             5,
		"jobs":                   5,
		"cronjobs":               5,
		"poddisruptionbudgets":   5,
	}

	total := 0
	for _, rt := range resourceTypes {
		if estimate, exists := estimates[rt]; exists {
			if types.IsClusterScoped(rt) {
				total += estimate
			} else {
				total += estimate * namespaceCount
			}
		} else {
			// Default estimate for unknown resources
			if types.IsClusterScoped(rt) {
				total += 10
			} else {
				total += 5 * namespaceCount
			}
		}
	}

	if total < 100 {
		return 100
	}
	return total
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
	resources := make([]types.ResourceWithContent, 0, 100)
	var errors []error

	// Map resource types to their backup functions
	clusterBackupFuncs := map[string]func(context.Context) ([]types.ResourceWithContent, error){
		"namespaces":          m.backupNamespaces,
		"persistentvolumes":   m.backupPersistentVolumes,
		"clusterroles":        m.backupClusterRoles,
		"clusterrolebindings": m.backupClusterRoleBindings,
		"storageclasses":      m.backupStorageClasses,
	}

	for _, rt := range resourceTypes {
		if !types.IsClusterScoped(rt) {
			continue
		}

		if backupFunc, exists := clusterBackupFuncs[rt]; exists {
			if res, err := backupFunc(ctx); err != nil {
				errors = append(errors, fmt.Errorf("failed to backup %s: %w", rt, err))
			} else {
				resources = append(resources, res...)
			}
		}
	}

	return resources, errors
}

func (m *Manager) backupNamespacedResources(ctx context.Context, namespace string, resourceTypes []string) ([]types.ResourceWithContent, []error) {
	resources := make([]types.ResourceWithContent, 0, 50)
	var errors []error

	namespacedBackupFuncs := map[string]func(context.Context, string) ([]types.ResourceWithContent, error){
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

	for _, resourceType := range resourceTypes {
		if types.IsClusterScoped(resourceType) {
			continue
		}

		if backupFunc, exists := namespacedBackupFuncs[resourceType]; exists {
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

// getAPIInfo returns API version information for Kubernetes resource kinds
func (m *Manager) getAPIInfo(kind string) APIInfo {
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

// backupResources is a generic function for backing up any Kubernetes resource type
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

// Cluster-scoped resource backup functions
func (m *Manager) backupNamespaces(ctx context.Context) ([]types.ResourceWithContent, error) {
	return m.backupResources(ctx, "", "Namespace",
		func() (interface{}, error) {
			return m.k8sClient.Clientset().CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
		}, nil)
}

// Namespaced resource backup functions
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

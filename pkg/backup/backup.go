package backup

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

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
	allNamespaces, err := m.k8sClient.GetNamespaces(ctx)
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

	// Get the proper apiVersion for this kind
	apiVersion := m.getAPIVersionForKind(kind)

	// Set the ObjectKind properly (client-go objects don't have this populated by default)
	obj.GetObjectKind().SetGroupVersionKind(schema.GroupVersionKind{
		Group:   m.getGroupForKind(kind),
		Version: m.getVersionForKind(kind),
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
		APIVersion:  apiVersion,
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

// getAPIVersionForKind returns the proper API version for a given resource kind
func (m *Manager) getAPIVersionForKind(kind string) string {
	switch kind {
	case "Namespace", "Service", "ConfigMap", "Secret", "PersistentVolumeClaim", "PersistentVolume", "ServiceAccount":
		return "v1"
	case "Deployment", "StatefulSet", "DaemonSet":
		return "apps/v1"
	case "Job":
		return "batch/v1"
	case "CronJob":
		return "batch/v1"
	case "Role", "RoleBinding", "ClusterRole", "ClusterRoleBinding":
		return "rbac.authorization.k8s.io/v1"
	case "Ingress", "NetworkPolicy":
		return "networking.k8s.io/v1"
	case "StorageClass":
		return "storage.k8s.io/v1"
	case "PodDisruptionBudget":
		return "policy/v1"
	default:
		return "v1"
	}
}

// getGroupForKind returns the API group for a given resource kind
func (m *Manager) getGroupForKind(kind string) string {
	switch kind {
	case "Namespace", "Service", "ConfigMap", "Secret", "PersistentVolumeClaim", "PersistentVolume", "ServiceAccount":
		return ""
	case "Deployment", "StatefulSet", "DaemonSet":
		return "apps"
	case "Job", "CronJob":
		return "batch"
	case "Role", "RoleBinding", "ClusterRole", "ClusterRoleBinding":
		return "rbac.authorization.k8s.io"
	case "Ingress", "NetworkPolicy":
		return "networking.k8s.io"
	case "StorageClass":
		return "storage.k8s.io"
	case "PodDisruptionBudget":
		return "policy"
	default:
		return ""
	}
}

// getVersionForKind returns the API version (without group) for a given resource kind
func (m *Manager) getVersionForKind(kind string) string {
	switch kind {
	case "Namespace", "Service", "ConfigMap", "Secret", "PersistentVolumeClaim", "PersistentVolume", "ServiceAccount":
		return "v1"
	case "Deployment", "StatefulSet", "DaemonSet":
		return "v1"
	case "Job", "CronJob":
		return "v1"
	case "Role", "RoleBinding", "ClusterRole", "ClusterRoleBinding":
		return "v1"
	case "Ingress", "NetworkPolicy":
		return "v1"
	case "StorageClass":
		return "v1"
	case "PodDisruptionBudget":
		return "v1"
	default:
		return "v1"
	}
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

// Individual resource backup functions
func (m *Manager) backupNamespaces(ctx context.Context) ([]types.ResourceWithContent, error) {
	namespaces, err := m.k8sClient.GetNamespaces(ctx)
	if err != nil {
		return nil, err
	}

	var resources []types.ResourceWithContent
	for _, ns := range namespaces.Items {
		resource, err := m.convertToResourceWithContent(&ns, "", "Namespace")
		if err != nil {
			return nil, err
		}
		resources = append(resources, resource)
	}

	return resources, nil
}

func (m *Manager) backupDeployments(ctx context.Context, namespace string) ([]types.ResourceWithContent, error) {
	deployments, err := m.k8sClient.GetDeployments(ctx, namespace)
	if err != nil {
		return nil, err
	}

	resources := make([]types.ResourceWithContent, 0, len(deployments.Items))
	for _, deployment := range deployments.Items {
		resource, err := m.convertToResourceWithContent(&deployment, namespace, "Deployment")
		if err != nil {
			return nil, err
		}
		resources = append(resources, resource)
	}

	return resources, nil
}

func (m *Manager) backupServices(ctx context.Context, namespace string) ([]types.ResourceWithContent, error) {
	services, err := m.k8sClient.GetServices(ctx, namespace)
	if err != nil {
		return nil, err
	}

	resources := make([]types.ResourceWithContent, 0, len(services.Items))
	for _, service := range services.Items {
		resource, err := m.convertToResourceWithContent(&service, namespace, "Service")
		if err != nil {
			return nil, err
		}
		resources = append(resources, resource)
	}

	return resources, nil
}

func (m *Manager) backupConfigMaps(ctx context.Context, namespace string) ([]types.ResourceWithContent, error) {
	configMaps, err := m.k8sClient.GetConfigMaps(ctx, namespace)
	if err != nil {
		return nil, err
	}

	var resources []types.ResourceWithContent
	for _, cm := range configMaps.Items {
		resource, err := m.convertToResourceWithContent(&cm, namespace, "ConfigMap")
		if err != nil {
			return nil, err
		}
		resources = append(resources, resource)
	}

	return resources, nil
}

func (m *Manager) backupSecrets(ctx context.Context, namespace string) ([]types.ResourceWithContent, error) {
	secrets, err := m.k8sClient.GetSecrets(ctx, namespace)
	if err != nil {
		return nil, err
	}

	var resources []types.ResourceWithContent
	for _, secret := range secrets.Items {
		// Skip service account tokens (they will be regenerated)
		if secret.Type == "kubernetes.io/service-account-token" {
			continue
		}

		resource, err := m.convertToResourceWithContent(&secret, namespace, "Secret")
		if err != nil {
			return nil, err
		}
		resources = append(resources, resource)
	}

	return resources, nil
}

func (m *Manager) backupPersistentVolumeClaims(ctx context.Context, namespace string) ([]types.ResourceWithContent, error) {
	pvcs, err := m.k8sClient.GetPersistentVolumeClaims(ctx, namespace)
	if err != nil {
		return nil, err
	}

	var resources []types.ResourceWithContent
	for _, pvc := range pvcs.Items {
		resource, err := m.convertToResourceWithContent(&pvc, namespace, "PersistentVolumeClaim")
		if err != nil {
			return nil, err
		}
		resources = append(resources, resource)
	}

	return resources, nil
}

func (m *Manager) backupPersistentVolumes(ctx context.Context) ([]types.ResourceWithContent, error) {
	pvs, err := m.k8sClient.GetPersistentVolumes(ctx)
	if err != nil {
		return nil, err
	}

	var resources []types.ResourceWithContent
	for _, pv := range pvs.Items {
		resource, err := m.convertToResourceWithContent(&pv, "", "PersistentVolume")
		if err != nil {
			return nil, err
		}
		resources = append(resources, resource)
	}

	return resources, nil
}

func (m *Manager) backupServiceAccounts(ctx context.Context, namespace string) ([]types.ResourceWithContent, error) {
	serviceAccounts, err := m.k8sClient.GetServiceAccounts(ctx, namespace)
	if err != nil {
		return nil, err
	}

	var resources []types.ResourceWithContent
	for _, sa := range serviceAccounts.Items {
		// Skip default service account (it will be created automatically)
		if sa.Name == "default" {
			continue
		}

		resource, err := m.convertToResourceWithContent(&sa, namespace, "ServiceAccount")
		if err != nil {
			return nil, err
		}
		resources = append(resources, resource)
	}

	return resources, nil
}

func (m *Manager) backupRoles(ctx context.Context, namespace string) ([]types.ResourceWithContent, error) {
	roles, err := m.k8sClient.GetRoles(ctx, namespace)
	if err != nil {
		return nil, err
	}

	var resources []types.ResourceWithContent
	for _, role := range roles.Items {
		resource, err := m.convertToResourceWithContent(&role, namespace, "Role")
		if err != nil {
			return nil, err
		}
		resources = append(resources, resource)
	}

	return resources, nil
}

func (m *Manager) backupRoleBindings(ctx context.Context, namespace string) ([]types.ResourceWithContent, error) {
	roleBindings, err := m.k8sClient.GetRoleBindings(ctx, namespace)
	if err != nil {
		return nil, err
	}

	var resources []types.ResourceWithContent
	for _, rb := range roleBindings.Items {
		resource, err := m.convertToResourceWithContent(&rb, namespace, "RoleBinding")
		if err != nil {
			return nil, err
		}
		resources = append(resources, resource)
	}

	return resources, nil
}

func (m *Manager) backupClusterRoles(ctx context.Context) ([]types.ResourceWithContent, error) {
	clusterRoles, err := m.k8sClient.GetClusterRoles(ctx)
	if err != nil {
		return nil, err
	}

	var resources []types.ResourceWithContent
	for _, cr := range clusterRoles.Items {
		// Skip system cluster roles
		if strings.HasPrefix(cr.Name, "system:") {
			continue
		}

		resource, err := m.convertToResourceWithContent(&cr, "", "ClusterRole")
		if err != nil {
			return nil, err
		}
		resources = append(resources, resource)
	}

	return resources, nil
}

func (m *Manager) backupClusterRoleBindings(ctx context.Context) ([]types.ResourceWithContent, error) {
	clusterRoleBindings, err := m.k8sClient.GetClusterRoleBindings(ctx)
	if err != nil {
		return nil, err
	}

	var resources []types.ResourceWithContent
	for _, crb := range clusterRoleBindings.Items {
		// Skip system cluster role bindings
		if strings.HasPrefix(crb.Name, "system:") {
			continue
		}

		resource, err := m.convertToResourceWithContent(&crb, "", "ClusterRoleBinding")
		if err != nil {
			return nil, err
		}
		resources = append(resources, resource)
	}

	return resources, nil
}

func (m *Manager) backupIngresses(ctx context.Context, namespace string) ([]types.ResourceWithContent, error) {
	ingresses, err := m.k8sClient.GetIngresses(ctx, namespace)
	if err != nil {
		return nil, err
	}

	var resources []types.ResourceWithContent
	for _, ingress := range ingresses.Items {
		resource, err := m.convertToResourceWithContent(&ingress, namespace, "Ingress")
		if err != nil {
			return nil, err
		}
		resources = append(resources, resource)
	}

	return resources, nil
}

func (m *Manager) backupNetworkPolicies(ctx context.Context, namespace string) ([]types.ResourceWithContent, error) {
	networkPolicies, err := m.k8sClient.GetNetworkPolicies(ctx, namespace)
	if err != nil {
		return nil, err
	}

	var resources []types.ResourceWithContent
	for _, np := range networkPolicies.Items {
		resource, err := m.convertToResourceWithContent(&np, namespace, "NetworkPolicy")
		if err != nil {
			return nil, err
		}
		resources = append(resources, resource)
	}

	return resources, nil
}

func (m *Manager) backupStatefulSets(ctx context.Context, namespace string) ([]types.ResourceWithContent, error) {
	statefulSets, err := m.k8sClient.GetStatefulSets(ctx, namespace)
	if err != nil {
		return nil, err
	}

	var resources []types.ResourceWithContent
	for _, sts := range statefulSets.Items {
		resource, err := m.convertToResourceWithContent(&sts, namespace, "StatefulSet")
		if err != nil {
			return nil, err
		}
		resources = append(resources, resource)
	}

	return resources, nil
}

func (m *Manager) backupDaemonSets(ctx context.Context, namespace string) ([]types.ResourceWithContent, error) {
	daemonSets, err := m.k8sClient.GetDaemonSets(ctx, namespace)
	if err != nil {
		return nil, err
	}

	var resources []types.ResourceWithContent
	for _, ds := range daemonSets.Items {
		resource, err := m.convertToResourceWithContent(&ds, namespace, "DaemonSet")
		if err != nil {
			return nil, err
		}
		resources = append(resources, resource)
	}

	return resources, nil
}

func (m *Manager) backupJobs(ctx context.Context, namespace string) ([]types.ResourceWithContent, error) {
	jobs, err := m.k8sClient.GetJobs(ctx, namespace)
	if err != nil {
		return nil, err
	}

	var resources []types.ResourceWithContent
	for _, job := range jobs.Items {
		resource, err := m.convertToResourceWithContent(&job, namespace, "Job")
		if err != nil {
			return nil, err
		}
		resources = append(resources, resource)
	}

	return resources, nil
}

func (m *Manager) backupCronJobs(ctx context.Context, namespace string) ([]types.ResourceWithContent, error) {
	cronJobs, err := m.k8sClient.GetCronJobs(ctx, namespace)
	if err != nil {
		return nil, err
	}

	var resources []types.ResourceWithContent
	for _, cj := range cronJobs.Items {
		resource, err := m.convertToResourceWithContent(&cj, namespace, "CronJob")
		if err != nil {
			return nil, err
		}
		resources = append(resources, resource)
	}

	return resources, nil
}

func (m *Manager) backupStorageClasses(ctx context.Context) ([]types.ResourceWithContent, error) {
	storageClasses, err := m.k8sClient.GetStorageClasses(ctx)
	if err != nil {
		return nil, err
	}

	var resources []types.ResourceWithContent
	for _, sc := range storageClasses.Items {
		resource, err := m.convertToResourceWithContent(&sc, "", "StorageClass")
		if err != nil {
			return nil, err
		}
		resources = append(resources, resource)
	}

	return resources, nil
}

func (m *Manager) backupPodDisruptionBudgets(ctx context.Context, namespace string) ([]types.ResourceWithContent, error) {
	pdbs, err := m.k8sClient.GetPodDisruptionBudgets(ctx, namespace)
	if err != nil {
		return nil, err
	}

	var resources []types.ResourceWithContent
	for _, pdb := range pdbs.Items {
		resource, err := m.convertToResourceWithContent(&pdb, namespace, "PodDisruptionBudget")
		if err != nil {
			return nil, err
		}
		resources = append(resources, resource)
	}

	return resources, nil
}

package k8s

import (
	"context"
	"fmt"
	"path/filepath"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

type Client struct {
	clientset *kubernetes.Clientset
}

func NewClient(kubeconfigPath string) (*Client, error) {
	var config *rest.Config
	var err error

	if kubeconfigPath == "" {
		config, err = rest.InClusterConfig()
		if err != nil {
			if home := homedir.HomeDir(); home != "" {
				kubeconfigPath = filepath.Join(home, ".kube", "config")
			}
		}
	}

	if config == nil {
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		if err != nil {
			return nil, fmt.Errorf("failed to build kubeconfig: %w", err)
		}
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create clientset: %w", err)
	}

	return &Client{clientset: clientset}, nil
}

func (c *Client) GetServerVersion() (string, error) {
	version, err := c.clientset.Discovery().ServerVersion()
	if err != nil {
		return "", fmt.Errorf("failed to get server version: %w", err)
	}
	return version.GitVersion, nil
}

func (c *Client) GetNamespaces(ctx context.Context) (*corev1.NamespaceList, error) {
	return c.clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
}

func (c *Client) GetDeployments(ctx context.Context, namespace string) (*appsv1.DeploymentList, error) {
	return c.clientset.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
}

func (c *Client) GetServices(ctx context.Context, namespace string) (*corev1.ServiceList, error) {
	return c.clientset.CoreV1().Services(namespace).List(ctx, metav1.ListOptions{})
}

func (c *Client) GetConfigMaps(ctx context.Context, namespace string) (*corev1.ConfigMapList, error) {
	return c.clientset.CoreV1().ConfigMaps(namespace).List(ctx, metav1.ListOptions{})
}

func (c *Client) GetSecrets(ctx context.Context, namespace string) (*corev1.SecretList, error) {
	return c.clientset.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{})
}

func (c *Client) GetPersistentVolumeClaims(ctx context.Context, namespace string) (*corev1.PersistentVolumeClaimList, error) {
	return c.clientset.CoreV1().PersistentVolumeClaims(namespace).List(ctx, metav1.ListOptions{})
}

func (c *Client) GetPersistentVolumes(ctx context.Context) (*corev1.PersistentVolumeList, error) {
	return c.clientset.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
}

func (c *Client) GetServiceAccounts(ctx context.Context, namespace string) (*corev1.ServiceAccountList, error) {
	return c.clientset.CoreV1().ServiceAccounts(namespace).List(ctx, metav1.ListOptions{})
}

func (c *Client) GetRoles(ctx context.Context, namespace string) (*rbacv1.RoleList, error) {
	return c.clientset.RbacV1().Roles(namespace).List(ctx, metav1.ListOptions{})
}

func (c *Client) GetRoleBindings(ctx context.Context, namespace string) (*rbacv1.RoleBindingList, error) {
	return c.clientset.RbacV1().RoleBindings(namespace).List(ctx, metav1.ListOptions{})
}

func (c *Client) GetClusterRoles(ctx context.Context) (*rbacv1.ClusterRoleList, error) {
	return c.clientset.RbacV1().ClusterRoles().List(ctx, metav1.ListOptions{})
}

func (c *Client) GetClusterRoleBindings(ctx context.Context) (*rbacv1.ClusterRoleBindingList, error) {
	return c.clientset.RbacV1().ClusterRoleBindings().List(ctx, metav1.ListOptions{})
}

func (c *Client) GetIngresses(ctx context.Context, namespace string) (*networkingv1.IngressList, error) {
	return c.clientset.NetworkingV1().Ingresses(namespace).List(ctx, metav1.ListOptions{})
}

func (c *Client) GetNetworkPolicies(ctx context.Context, namespace string) (*networkingv1.NetworkPolicyList, error) {
	return c.clientset.NetworkingV1().NetworkPolicies(namespace).List(ctx, metav1.ListOptions{})
}

func (c *Client) GetStatefulSets(ctx context.Context, namespace string) (*appsv1.StatefulSetList, error) {
	return c.clientset.AppsV1().StatefulSets(namespace).List(ctx, metav1.ListOptions{})
}

func (c *Client) GetDaemonSets(ctx context.Context, namespace string) (*appsv1.DaemonSetList, error) {
	return c.clientset.AppsV1().DaemonSets(namespace).List(ctx, metav1.ListOptions{})
}

func (c *Client) GetJobs(ctx context.Context, namespace string) (*batchv1.JobList, error) {
	return c.clientset.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{})
}

func (c *Client) GetCronJobs(ctx context.Context, namespace string) (*batchv1.CronJobList, error) {
	return c.clientset.BatchV1().CronJobs(namespace).List(ctx, metav1.ListOptions{})
}

func (c *Client) GetStorageClasses(ctx context.Context) (*storagev1.StorageClassList, error) {
	return c.clientset.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
}

func (c *Client) GetPodDisruptionBudgets(ctx context.Context, namespace string) (*policyv1.PodDisruptionBudgetList, error) {
	return c.clientset.PolicyV1().PodDisruptionBudgets(namespace).List(ctx, metav1.ListOptions{})
}

// IMPORTANT: ApplyResource not implemented - this is a backup-only tool
func (c *Client) ApplyResource(ctx context.Context, obj runtime.Object, namespace string, dryRun bool) error {
	return fmt.Errorf("ApplyResource not implemented - this tool is for backup only")
}

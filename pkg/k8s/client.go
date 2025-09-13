package k8s

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	"sigs.k8s.io/yaml"
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

// Clientset returns the underlying Kubernetes clientset for direct access
// This eliminates the need for 20+ wrapper methods that add no value
func (c *Client) Clientset() *kubernetes.Clientset {
	return c.clientset
}

// ApplyResource applies a Kubernetes resource to the cluster
func (c *Client) ApplyResource(ctx context.Context, obj runtime.Object, namespace string, dryRun bool) error {
	// Convert to unstructured if needed
	var unstruct *unstructured.Unstructured
	if u, ok := obj.(*unstructured.Unstructured); ok {
		unstruct = u
	} else {
		unstructObj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
		if err != nil {
			return fmt.Errorf("failed to convert object to unstructured: %w", err)
		}
		unstruct = &unstructured.Unstructured{Object: unstructObj}
	}

	if dryRun {
		return nil // Skip all operations in dry run
	}

	// Get resource info
	gvk := unstruct.GroupVersionKind()
	kind := gvk.Kind
	targetNamespace := unstruct.GetNamespace()
	if targetNamespace == "" && namespace != "" {
		targetNamespace = namespace
		unstruct.SetNamespace(targetNamespace)
	}

	// Use unified approach: marshal to YAML, unmarshal to typed object, then create/update
	return c.applyResource(ctx, unstruct, kind, targetNamespace)
}

// applyResource handles the actual resource application using a unified approach
func (c *Client) applyResource(ctx context.Context, unstruct *unstructured.Unstructured, kind, namespace string) error {
	// Convert unstructured to YAML
	yamlData, err := yaml.Marshal(unstruct.Object)
	if err != nil {
		return fmt.Errorf("failed to marshal resource to YAML: %w", err)
	}

	// Apply based on resource type using the same create/update pattern
	switch kind {
	case "Namespace":
		var ns corev1.Namespace
		if err := yaml.Unmarshal(yamlData, &ns); err != nil {
			return err
		}
		_, err = c.clientset.CoreV1().Namespaces().Create(ctx, &ns, metav1.CreateOptions{})
		if err != nil && strings.Contains(err.Error(), "already exists") {
			_, err = c.clientset.CoreV1().Namespaces().Update(ctx, &ns, metav1.UpdateOptions{})
		}
		return err

	case "Service":
		var svc corev1.Service
		if err := yaml.Unmarshal(yamlData, &svc); err != nil {
			return err
		}
		svc.Namespace = namespace
		_, err = c.clientset.CoreV1().Services(namespace).Create(ctx, &svc, metav1.CreateOptions{})
		if err != nil && strings.Contains(err.Error(), "already exists") {
			_, err = c.clientset.CoreV1().Services(namespace).Update(ctx, &svc, metav1.UpdateOptions{})
		}
		return err

	case "Deployment":
		var dep appsv1.Deployment
		if err := yaml.Unmarshal(yamlData, &dep); err != nil {
			return err
		}
		dep.Namespace = namespace
		_, err = c.clientset.AppsV1().Deployments(namespace).Create(ctx, &dep, metav1.CreateOptions{})
		if err != nil && strings.Contains(err.Error(), "already exists") {
			_, err = c.clientset.AppsV1().Deployments(namespace).Update(ctx, &dep, metav1.UpdateOptions{})
		}
		return err

	case "ConfigMap":
		var cm corev1.ConfigMap
		if err := yaml.Unmarshal(yamlData, &cm); err != nil {
			return err
		}
		cm.Namespace = namespace
		_, err = c.clientset.CoreV1().ConfigMaps(namespace).Create(ctx, &cm, metav1.CreateOptions{})
		if err != nil && strings.Contains(err.Error(), "already exists") {
			_, err = c.clientset.CoreV1().ConfigMaps(namespace).Update(ctx, &cm, metav1.UpdateOptions{})
		}
		return err

	case "Secret":
		var secret corev1.Secret
		if err := yaml.Unmarshal(yamlData, &secret); err != nil {
			return err
		}
		secret.Namespace = namespace
		_, err = c.clientset.CoreV1().Secrets(namespace).Create(ctx, &secret, metav1.CreateOptions{})
		if err != nil && strings.Contains(err.Error(), "already exists") {
			_, err = c.clientset.CoreV1().Secrets(namespace).Update(ctx, &secret, metav1.UpdateOptions{})
		}
		return err

	default:
		// Log unsupported types but don't fail
		fmt.Printf("Info: Skipping unsupported resource type: %s\n", kind)
		return nil
	}
}

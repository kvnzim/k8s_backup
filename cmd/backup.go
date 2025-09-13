package cmd

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"k8s-backup/pkg/backup"
	"k8s-backup/pkg/k8s"
	"k8s-backup/pkg/storage"
	"k8s-backup/pkg/types"
)

var (
	// Backup-specific flags
	backupName           string
	backupPath           string
	backupNamespaces     []string
	backupResourceTypes  []string
	excludeNamespaces    []string
	excludeResourceTypes []string
	compress             bool
)

// backupCmd represents the backup command
var backupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Create a backup of Kubernetes cluster resources",
	Long: `Create a backup of your Kubernetes cluster resources.

The backup command exports the current state of your cluster (namespaces, deployments, 
services, configmaps, secrets, PVCs, etc.) into organized backup files.

Examples:
  # Backup all resources from all namespaces
  k8s-backup backup

  # Backup specific namespaces
  k8s-backup backup --namespaces app1,app2

  # Backup only deployments and services
  k8s-backup backup --resource-types deployments,services

  # Create a named backup with compression
  k8s-backup backup --name my-backup --compress

  # Backup to a specific directory
  k8s-backup backup --output ./my-backups/`,

	Run: runBackup,
}

func init() {
	rootCmd.AddCommand(backupCmd)

	// Backup-specific flags
	backupCmd.Flags().StringVar(&backupName, "name", "", "name for the backup (default: auto-generated timestamp)")
	backupCmd.Flags().StringVarP(&backupPath, "output", "o", "./backups", "output directory for backups")
	backupCmd.Flags().StringSliceVar(&backupNamespaces, "namespaces", []string{}, "comma-separated list of namespaces to backup (default: all)")
	backupCmd.Flags().StringSliceVar(&backupResourceTypes, "resource-types", []string{}, "comma-separated list of resource types to backup (default: all supported)")
	backupCmd.Flags().StringSliceVar(&excludeNamespaces, "exclude-namespaces", []string{"kube-system", "kube-public", "kube-node-lease"}, "comma-separated list of namespaces to exclude")
	backupCmd.Flags().StringSliceVar(&excludeResourceTypes, "exclude-resource-types", []string{}, "comma-separated list of resource types to exclude")
	backupCmd.Flags().BoolVar(&compress, "compress", true, "compress backup files using gzip")
}

func runBackup(cmd *cobra.Command, args []string) {
	ctx := context.Background()

	// Generate backup name if not provided
	if backupName == "" {
		backupName = fmt.Sprintf("backup-%s", time.Now().Format("2006-01-02-15-04-05"))
	}

	if verbose {
		log.Printf("Starting backup: %s", backupName)
		log.Printf("Output path: %s", backupPath)
		if len(backupNamespaces) > 0 {
			log.Printf("Namespaces: %s", strings.Join(backupNamespaces, ", "))
		}
		if len(backupResourceTypes) > 0 {
			log.Printf("Resource types: %s", strings.Join(backupResourceTypes, ", "))
		}
	}

	// Initialize Kubernetes client
	client, err := k8s.NewClient(kubeconfig)
	if err != nil {
		log.Fatalf("Failed to create Kubernetes client: %v", err)
	}

	// Initialize storage
	storageBackend := storage.NewLocalStorage(backupPath)

	// Initialize backup manager
	backupManager := backup.NewManager(client, storageBackend)

	// Prepare backup options
	options := &types.BackupOptions{
		Namespaces:           backupNamespaces,
		ResourceTypes:        backupResourceTypes,
		ExcludeNamespaces:    excludeNamespaces,
		ExcludeResourceTypes: excludeResourceTypes,
		OutputPath:           backupPath,
		BackupName:           backupName,
		Compress:             compress,
	}

	// Progress callback
	progressCallback := func(progress types.Progress) {
		// Show progress every 10 items, when verbose, or when complete
		if verbose || progress.Completed%10 == 0 || progress.Completed == progress.Total {
			fmt.Printf("\rProgress: %d/%d - %s", progress.Completed, progress.Total, progress.Current)
			if progress.Completed == progress.Total {
				fmt.Println()
			}
		}
		// Log any errors
		for _, err := range progress.Errors {
			log.Printf("Warning: %v", err)
		}
	}

	// Perform backup
	metadata, err := backupManager.CreateBackup(ctx, options, progressCallback)
	if err != nil {
		log.Fatalf("Backup failed: %v", err)
	}

	// Print success message
	fmt.Printf("\n✅ Backup completed successfully!\n")
	fmt.Printf("Backup name: %s\n", metadata.Name)
	fmt.Printf("Resources backed up: %d\n", metadata.TotalResources)
	fmt.Printf("Namespaces: %s\n", strings.Join(metadata.Namespaces, ", "))
	fmt.Printf("Size: %.2f MB\n", float64(metadata.Size)/(1024*1024))
	fmt.Printf("Location: %s\n", metadata.BackupPath)
	fmt.Printf("Timestamp: %s\n", metadata.Timestamp.Format(time.RFC3339))

	if verbose {
		fmt.Printf("Resource types: %s\n", strings.Join(metadata.ResourceTypes, ", "))
		fmt.Printf("Kubernetes version: %s\n", metadata.KubernetesVersion)
	}
}

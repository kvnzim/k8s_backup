package cmd

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"k8s-backup/pkg/k8s"
	"k8s-backup/pkg/restore"
	"k8s-backup/pkg/storage"
	"k8s-backup/pkg/types"
)

var (
	// Restore-specific flags
	restoreBackupPath    string
	restoreNamespaces    []string
	restoreResourceTypes []string
	dryRun               bool
	waitForReady         bool
	restoreTimeout       time.Duration
	overwriteExisting    bool
)

// restoreCmd represents the restore command
var restoreCmd = &cobra.Command{
	Use:   "restore",
	Short: "Restore Kubernetes resources from a backup",
	Long: `Restore Kubernetes resources from a previously created backup.

The restore command reads backup files and reapplies the resources to bring your 
cluster back to its previous state. Resources are applied in dependency order 
to ensure proper restoration.

Examples:
  # Restore from the latest backup
  k8s-backup restore

  # Restore from a specific backup
  k8s-backup restore --backup ./backups/backup-2025-09-12-15-00-00

  # Restore only specific namespaces
  k8s-backup restore --namespaces app1,app2

  # Restore only specific resource types
  k8s-backup restore --resource-types deployments,services

  # Dry run to see what would be restored
  k8s-backup restore --dry-run

  # Wait for resources to become ready
  k8s-backup restore --wait --timeout 300s`,

	Run: runRestore,
}

func init() {
	rootCmd.AddCommand(restoreCmd)

	// Restore-specific flags
	restoreCmd.Flags().StringVar(&restoreBackupPath, "backup", "", "path to backup directory or archive (default: latest backup)")
	restoreCmd.Flags().StringSliceVar(&restoreNamespaces, "namespaces", []string{}, "comma-separated list of namespaces to restore (default: all from backup)")
	restoreCmd.Flags().StringSliceVar(&restoreResourceTypes, "resource-types", []string{}, "comma-separated list of resource types to restore (default: all from backup)")
	restoreCmd.Flags().BoolVar(&dryRun, "dry-run", false, "perform validation without applying changes")
	restoreCmd.Flags().BoolVar(&waitForReady, "wait", false, "wait for resources to become ready after creation")
	restoreCmd.Flags().DurationVar(&restoreTimeout, "timeout", 5*time.Minute, "timeout for waiting operations")
	restoreCmd.Flags().BoolVar(&overwriteExisting, "overwrite", false, "overwrite existing resources if they already exist")
}

func runRestore(cmd *cobra.Command, args []string) {
	ctx := context.Background()

	if verbose {
		log.Printf("Starting restore operation")
		if restoreBackupPath != "" {
			log.Printf("Backup path: %s", restoreBackupPath)
		} else {
			log.Printf("Using latest backup from: ./backups")
		}
		if len(restoreNamespaces) > 0 {
			log.Printf("Namespaces: %s", strings.Join(restoreNamespaces, ", "))
		}
		if len(restoreResourceTypes) > 0 {
			log.Printf("Resource types: %s", strings.Join(restoreResourceTypes, ", "))
		}
		if dryRun {
			log.Printf("Dry run mode enabled")
		}
	}

	// Initialize storage
	storageBackend := storage.NewLocalStorage("./backups")

	// Find backup path if not specified
	if restoreBackupPath == "" {
		backups, err := storageBackend.ListBackups()
		if err != nil {
			log.Fatalf("Failed to list backups: %v", err)
		}
		if len(backups) == 0 {
			log.Fatalf("No backups found in ./backups")
		}

		// Use the most recent backup
		var latest *types.BackupMetadata
		for _, backup := range backups {
			if latest == nil || backup.Timestamp.After(latest.Timestamp) {
				latest = backup
			}
		}
		restoreBackupPath = latest.BackupPath

		if verbose {
			log.Printf("Using latest backup: %s (created: %s)", latest.Name, latest.Timestamp.Format(time.RFC3339))
		}
	}

	// Initialize Kubernetes client (not needed for dry run validation)
	var client *k8s.Client
	if !dryRun {
		var err error
		client, err = k8s.NewClient(kubeconfig)
		if err != nil {
			log.Fatalf("Failed to create Kubernetes client: %v", err)
		}
	}

	// Initialize restore manager
	restoreManager := restore.NewManager(client, storageBackend)

	// Prepare restore options
	options := &types.RestoreOptions{
		BackupPath:        restoreBackupPath,
		Namespaces:        restoreNamespaces,
		ResourceTypes:     restoreResourceTypes,
		DryRun:            dryRun,
		Wait:              waitForReady,
		Timeout:           restoreTimeout,
		OverwriteExisting: overwriteExisting,
	}

	// Progress callback
	progressCallback := func(progress types.Progress) {
		if verbose || progress.Completed%5 == 0 || progress.Completed == progress.Total {
			fmt.Printf("\rProgress: %d/%d - %s", progress.Completed, progress.Total, progress.Current)
			if progress.Completed == progress.Total {
				fmt.Println() // New line when complete
			}
		}

		for _, err := range progress.Errors {
			log.Printf("Warning: %v", err)
		}
	}

	// Perform restore
	result, err := restoreManager.RestoreBackup(ctx, options, progressCallback)
	if err != nil {
		log.Fatalf("Restore failed: %v", err)
	}

	// Print success message
	if dryRun {
		fmt.Printf("\n✅ Dry run completed successfully!\n")
		fmt.Printf("Would restore %d resources\n", result.ProcessedResources)
	} else {
		fmt.Printf("\n✅ Restore completed successfully!\n")
		fmt.Printf("Resources restored: %d\n", result.ProcessedResources)
	}

	if len(result.Namespaces) > 0 {
		fmt.Printf("Namespaces: %s\n", strings.Join(result.Namespaces, ", "))
	}

	if len(result.ResourceTypes) > 0 {
		fmt.Printf("Resource types: %s\n", strings.Join(result.ResourceTypes, ", "))
	}

	if result.SkippedResources > 0 {
		fmt.Printf("Skipped resources: %d\n", result.SkippedResources)
	}

	if len(result.Errors) > 0 {
		fmt.Printf("Errors encountered: %d\n", len(result.Errors))
		if verbose {
			for _, err := range result.Errors {
				log.Printf("Error: %v", err)
			}
		}
	}

	if verbose {
		fmt.Printf("Duration: %s\n", result.Duration.String())
	}
}

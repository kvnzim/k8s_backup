package cmd

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"k8s-backup/pkg/storage"
	"k8s-backup/pkg/types"
)

var (
	// List-specific flags
	listPath   string
	showDetail bool
	sortBy     string
)

// listCmd represents the list command
var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List available backups",
	Long: `List all available backups with their metadata.

Shows backup names, creation timestamps, sizes, and included namespaces/resources.

Examples:
  # List all backups
  k8s-backup list

  # List backups with detailed information
  k8s-backup list --detail

  # List backups from a specific directory
  k8s-backup list --path ./my-backups

  # Sort backups by size
  k8s-backup list --sort-by size`,

	Run: runList,
}

func init() {
	rootCmd.AddCommand(listCmd)

	// List-specific flags
	listCmd.Flags().StringVar(&listPath, "path", "./backups", "path to backup directory")
	listCmd.Flags().BoolVarP(&showDetail, "detail", "d", false, "show detailed backup information")
	listCmd.Flags().StringVar(&sortBy, "sort-by", "timestamp", "sort backups by: timestamp, name, size, resources (default: timestamp)")
}

func runList(cmd *cobra.Command, args []string) {
	if verbose {
		log.Printf("Listing backups from: %s", listPath)
	}

	// Initialize storage
	storageBackend := storage.NewLocalStorage(listPath)

	// Get all backups
	backups, err := storageBackend.ListBackups()
	if err != nil {
		log.Fatalf("Failed to list backups: %v", err)
	}

	if len(backups) == 0 {
		fmt.Printf("No backups found in %s\n", listPath)
		return
	}

	// Sort backups
	sortBackups(backups, sortBy)

	// Print header
	if showDetail {
		printDetailedBackups(backups)
	} else {
		printSimpleBackups(backups)
	}
}

func sortBackups(backups []*types.BackupMetadata, sortBy string) {
	switch sortBy {
	case "name":
		sort.Slice(backups, func(i, j int) bool {
			return backups[i].Name < backups[j].Name
		})
	case "size":
		sort.Slice(backups, func(i, j int) bool {
			return backups[i].Size > backups[j].Size
		})
	case "resources":
		sort.Slice(backups, func(i, j int) bool {
			return backups[i].TotalResources > backups[j].TotalResources
		})
	case "timestamp":
		fallthrough
	default:
		sort.Slice(backups, func(i, j int) bool {
			return backups[i].Timestamp.After(backups[j].Timestamp)
		})
	}
}

func printSimpleBackups(backups []*types.BackupMetadata) {
	fmt.Printf("%-30s %-20s %-10s %-10s %s\n", "NAME", "CREATED", "SIZE", "RESOURCES", "NAMESPACES")
	fmt.Printf("%-30s %-20s %-10s %-10s %s\n", strings.Repeat("-", 30), strings.Repeat("-", 20), strings.Repeat("-", 10), strings.Repeat("-", 10), strings.Repeat("-", 20))

	for _, backup := range backups {
		size := formatSize(backup.Size)
		created := backup.Timestamp.Format("2006-01-02 15:04:05")
		namespaces := strings.Join(backup.Namespaces, ",")
		if len(namespaces) > 20 {
			namespaces = namespaces[:17] + "..."
		}

		fmt.Printf("%-30s %-20s %-10s %-10d %s\n",
			backup.Name,
			created,
			size,
			backup.TotalResources,
			namespaces,
		)
	}
}

func printDetailedBackups(backups []*types.BackupMetadata) {
	for i, backup := range backups {
		if i > 0 {
			fmt.Println()
		}

		fmt.Printf("Name: %s\n", backup.Name)
		fmt.Printf("Created: %s\n", backup.Timestamp.Format("2006-01-02 15:04:05 MST"))
		fmt.Printf("Version: %s\n", backup.Version)
		fmt.Printf("Size: %s\n", formatSize(backup.Size))
		fmt.Printf("Resources: %d\n", backup.TotalResources)
		fmt.Printf("K8s Version: %s\n", backup.KubernetesVersion)
		fmt.Printf("Path: %s\n", backup.BackupPath)

		if len(backup.Namespaces) > 0 {
			fmt.Printf("Namespaces (%d): %s\n", len(backup.Namespaces), strings.Join(backup.Namespaces, ", "))
		}

		if len(backup.ResourceTypes) > 0 {
			fmt.Printf("Resource Types (%d): %s\n", len(backup.ResourceTypes), strings.Join(backup.ResourceTypes, ", "))
		}

		// Calculate age
		age := time.Since(backup.Timestamp)
		fmt.Printf("Age: %s\n", formatDuration(age))
	}
}

func formatSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.0fs", d.Seconds())
	} else if d < time.Hour {
		return fmt.Sprintf("%.0fm", d.Minutes())
	} else if d < 24*time.Hour {
		return fmt.Sprintf("%.1fh", d.Hours())
	} else {
		return fmt.Sprintf("%.1fd", d.Hours()/24)
	}
}

package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	// Global flags
	kubeconfig string
	namespace  string
	verbose    bool
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "k8s-backup",
	Short: "A simple Kubernetes backup and restore tool",
	Long: `k8s-backup is a CLI tool for backing up and restoring Kubernetes cluster resources.

It can export your cluster's current state (deployments, services, configmaps, secrets, etc.) 
into organized backup files and restore them later to bring your cluster back to its previous state.

Features:
- Backup cluster resources to local storage
- Selective backups by namespace or resource type
- Restore from backup files with dependency ordering
- List and manage existing backups
- Comprehensive logging and error handling`,

	// Uncomment the following line if your bare application
	// has an action associated with it:
	// Run: func(cmd *cobra.Command, args []string) { },
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func init() {
	// Global flags
	rootCmd.PersistentFlags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig file (default: $HOME/.kube/config)")
	rootCmd.PersistentFlags().StringVarP(&namespace, "namespace", "n", "", "kubernetes namespace (default: all namespaces)")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "verbose output")

	// Bind environment variables
	if kubeconfig == "" {
		if home := os.Getenv("HOME"); home != "" {
			kubeconfig = fmt.Sprintf("%s/.kube/config", home)
		}
	}
}

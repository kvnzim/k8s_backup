# K8s-Backup

A simple yet powerful Kubernetes backup and restore tool written in Go, designed as a lightweight alternative to Velero for basic backup and restore operations.

## 🎯 Project Goals

K8s-Backup provides a CLI tool that can:
- **Backup**: Export the current state of your Kubernetes cluster (namespaces, deployments, services, configmaps, secrets, PVCs, etc.) into organized backup files
- **Store**: Save backups locally with optional compression (extensible to object storage like S3, GCS, NFS)
- **Restore**: Reapply resources from backup files to bring the cluster back to its previous state
- **Manage**: List, view, and delete existing backups

## 🏗️ Architecture

### Project Structure

```
k8s-backup/
├── cmd/                    # Cobra CLI commands
│   ├── root.go            # Root command and global flags
│   ├── backup.go          # Backup command implementation
│   ├── restore.go         # Restore command implementation
│   └── list.go            # List command implementation
├── pkg/
│   ├── types/             # Common types and structures
│   │   ├── types.go       # Backup metadata, options, and constants
│   │   └── types_test.go  # Unit tests for types
│   ├── k8s/               # Kubernetes client wrapper
│   │   └── client.go      # Client-go integration and API operations
│   ├── backup/            # Backup logic
│   │   └── backup.go      # Resource fetching and export logic
│   ├── restore/           # Restore logic
│   │   └── restore.go     # Resource application with dependency ordering
│   └── storage/           # Storage backend
│       ├── storage.go     # Local storage with tarball support
│       └── storage_test.go # Unit tests for storage
├── main.go                # Application entry point
├── go.mod                 # Go module definition
└── README.md              # This file
```

### Key Features

- **Idiomatic Go**: Clean interfaces, proper error handling, context usage
- **Production-ready**: Well-structured codebase with comprehensive logging
- **Dependency-aware**: Resources are restored in the correct order to respect dependencies
- **Selective operations**: Backup/restore specific namespaces or resource types
- **Progress tracking**: Real-time progress reporting with error handling
- **Comprehensive testing**: Unit tests for critical functionality

## 🚀 Getting Started

### Prerequisites

- Go 1.21 or later
- Access to a Kubernetes cluster
- kubectl configured with appropriate permissions

### Installation

1. Clone the repository:
```bash
git clone <your-repo-url>
cd k8s-backup
```

2. Build the application:
```bash
go build -o k8s-backup
```

3. (Optional) Install globally:
```bash
go install
```

### Basic Usage

#### Backup Operations

```bash
# Backup all resources from all namespaces
./k8s-backup backup

# Backup specific namespaces
./k8s-backup backup --namespaces app1,app2

# Backup only specific resource types
./k8s-backup backup --resource-types deployments,services

# Create a named backup with compression
./k8s-backup backup --name my-backup --compress

# Backup to a specific directory
./k8s-backup backup --output ./my-backups/

# Exclude system namespaces (default behavior)
./k8s-backup backup --exclude-namespaces kube-system,kube-public
```

#### Restore Operations

```bash
# Restore from the latest backup
./k8s-backup restore

# Restore from a specific backup
./k8s-backup restore --backup ./backups/backup-2025-09-12-15-00-00

# Restore only specific namespaces
./k8s-backup restore --namespaces app1,app2

# Restore only specific resource types
./k8s-backup restore --resource-types deployments,services

# Dry run to see what would be restored
./k8s-backup restore --dry-run

# Wait for resources to become ready
./k8s-backup restore --wait --timeout 300s

# Overwrite existing resources
./k8s-backup restore --overwrite
```

#### Management Operations

```bash
# List all available backups
./k8s-backup list

# List backups with detailed information
./k8s-backup list --detail

# List backups from a specific directory
./k8s-backup list --path ./my-backups

# Sort backups by size
./k8s-backup list --sort-by size
```

### Global Flags

- `--kubeconfig`: Path to kubeconfig file (default: `$HOME/.kube/config`)
- `--namespace`, `-n`: Kubernetes namespace (default: all namespaces)
- `--verbose`, `-v`: Verbose output

## 📋 Supported Resources

The tool supports backup and restore of the following Kubernetes resources:

### Cluster-scoped Resources
- CustomResourceDefinitions
- ClusterRoles
- ClusterRoleBindings
- PersistentVolumes
- StorageClasses
- VolumeSnapshotClasses
- Namespaces

### Namespaced Resources
- Deployments
- StatefulSets
- DaemonSets
- Jobs
- CronJobs
- ReplicaSets
- Pods
- Services
- Endpoints
- Ingresses
- NetworkPolicies
- ConfigMaps
- Secrets
- ServiceAccounts
- Roles
- RoleBindings
- PersistentVolumeClaims
- HorizontalPodAutoscalers
- PodDisruptionBudgets

## 🗂️ Backup Format

Backups are stored in an organized directory structure:

```
backups/
└── backup-2025-09-12-15-00-00/
    ├── manifest.yaml                    # Backup metadata
    ├── cluster/                         # Cluster-scoped resources
    │   ├── clusterrole-admin.yaml
    │   ├── namespace-production.yaml
    │   └── persistentvolume-pv001.yaml
    ├── default/                         # Namespaced resources
    │   ├── deployment-nginx.yaml
    │   ├── service-nginx.yaml
    │   └── configmap-app-config.yaml
    └── app1/
        ├── deployment-api.yaml
        └── secret-database.yaml
```

### Backup Manifest

Each backup includes a manifest file with metadata:

```yaml
metadata:
  name: backup-2025-09-12-15-00-00
  timestamp: "2025-09-12T15:00:00Z"
  version: v1
  kubernetesVersion: v1.28.4
  namespaces: ["default", "app1"]
  resourceTypes: ["deployments", "services", "configmaps"]
  totalResources: 42
  backupPath: "./backups/backup-2025-09-12-15-00-00"
  size: 1048576
  compress: false
resources:
  - apiVersion: apps/v1
    kind: Deployment
    namespace: default
    name: nginx
    relativePath: "default/deployment-nginx.yaml"
    labels:
      app: nginx
```

## 🔧 Development

### Running Tests

```bash
# Run all tests
go test ./...

# Run tests with verbose output
go test -v ./...

# Run tests for a specific package
go test ./pkg/types/ -v
go test ./pkg/storage/ -v

# Run tests with coverage
go test -cover ./...
```

### Code Structure

The codebase follows Go best practices:

- **Separation of concerns**: Clear separation between CLI, business logic, and storage
- **Interface-driven design**: Storage backend is interface-based for extensibility
- **Error handling**: Comprehensive error wrapping and context propagation
- **Testability**: Unit tests for critical functionality
- **Documentation**: Well-commented code for learning purposes

### Extending the Tool

#### Adding New Storage Backends

Implement the `Storage` interface in `pkg/storage/storage.go`:

```go
type Storage interface {
    SaveBackup(ctx context.Context, metadata *types.BackupMetadata, resources []types.ResourceWithContent) error
    LoadBackup(ctx context.Context, backupPath string) (*types.BackupManifest, []types.ResourceWithContent, error)
    ListBackups() ([]*types.BackupMetadata, error)
    DeleteBackup(backupPath string) error
    GetBackupPath(backupName string) string
}
```

#### Adding New Resource Types

1. Add the resource type to `SupportedResourceTypes` in `pkg/types/types.go`
2. Add client methods in `pkg/k8s/client.go`
3. Add backup methods in `pkg/backup/backup.go`
4. Update the resource order in `ResourceOrder` if needed

## ⚠️ Important Notes

### Current Limitations

- **ApplyResource Implementation**: The current implementation includes a placeholder for resource application. In a production environment, you would need to implement proper server-side apply logic using the Kubernetes API.

- **Resource Validation**: The tool performs basic validation but doesn't include comprehensive resource validation or conflict resolution.

- **Incremental Backups**: Currently supports full backups only. Incremental backup support could be added as an enhancement.

### Security Considerations

- Secrets are backed up and restored as-is. Consider implementing encryption for sensitive data.
- Ensure proper RBAC permissions for the service account used by the tool.
- Review backed up data before storing in shared locations.

### Performance Considerations

- Large clusters may take significant time to backup/restore.
- Consider using namespace and resource type filters for large deployments.
- Compressed backups reduce storage space but increase CPU usage.

## 🤝 Contributing

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Add tests for your changes
4. Ensure tests pass (`go test ./...`)
5. Commit your changes (`git commit -m 'Add amazing feature'`)
6. Push to the branch (`git push origin feature/amazing-feature`)
7. Open a Pull Request

## 📄 License

This project is open source and available under the [MIT License](LICENSE).

## 🙏 Acknowledgments

- Built with [client-go](https://github.com/kubernetes/client-go) for Kubernetes API interactions
- CLI powered by [Cobra](https://github.com/spf13/cobra)
- YAML processing with [sigs.k8s.io/yaml](https://github.com/kubernetes-sigs/yaml)
- Inspired by [Velero](https://velero.io/) for backup/restore concepts

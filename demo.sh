#!/bin/bash

# Demo script for k8s-backup tool
# This script demonstrates the basic usage of the tool

echo "🚀 K8s-Backup Tool Demo"
echo "======================="
echo

# Build the tool
echo "📦 Building the k8s-backup tool..."
go build -o k8s-backup
echo "✅ Build completed!"
echo

# Show help
echo "📖 Showing tool help:"
./k8s-backup --help
echo

# Show backup command help
echo "📖 Showing backup command help:"
./k8s-backup backup --help
echo

# Show restore command help  
echo "📖 Showing restore command help:"
./k8s-backup restore --help
echo

# Show list command help
echo "📖 Showing list command help:"
./k8s-backup list --help
echo

# Test listing (should be empty initially)
echo "📋 Testing list command (should be empty initially):"
./k8s-backup list
echo

# Note about actual usage
echo "📝 Note: To use this tool with a real Kubernetes cluster:"
echo "   1. Ensure kubectl is configured and you have cluster access"
echo "   2. Run: ./k8s-backup backup --dry-run (to test without making changes)"
echo "   3. Run: ./k8s-backup backup (to create an actual backup)"
echo "   4. Run: ./k8s-backup list (to see your backups)"
echo "   5. Run: ./k8s-backup restore --dry-run (to test restore without applying)"
echo

echo "🧪 Running unit tests:"
go test ./pkg/types/ ./pkg/storage/ -v
echo

echo "✅ Demo completed! The k8s-backup tool is ready to use."
echo "📁 Check the README.md for detailed usage instructions."

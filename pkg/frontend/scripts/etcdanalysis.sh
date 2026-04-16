#!/bin/bash
# Copyright (c) Microsoft Corporation.
# Licensed under the Apache License 2.0.
#
# Installs OctoSQL and analyzes an etcd snapshot.
# Usage: etcdanalysis.sh [--delete] /path/to/snapshot.snapshot
set -euo pipefail

DELETE_SNAPSHOT=false
if [ "${1:-}" = "--delete" ]; then
    DELETE_SNAPSHOT=true
    shift
fi

SNAPSHOT_PATH="${1:-}"
if [ -z "$SNAPSHOT_PATH" ]; then
    echo "Error: snapshot path required" >&2
    echo "Usage: $0 [--delete] /path/to/snapshot.snapshot" >&2
    exit 1
fi

OCTOSQL_VERSION="0.13.0"

echo "Installing dependencies..."
microdnf install -y curl tar gzip jq ca-certificates 2>&1

echo "Downloading OctoSQL ${OCTOSQL_VERSION}..."
curl -sL "https://github.com/cube2222/octosql/releases/download/v${OCTOSQL_VERSION}/octosql_${OCTOSQL_VERSION}_linux_amd64.tar.gz" | \
    tar -xz -C /usr/local/bin octosql
chmod +x /usr/local/bin/octosql

echo "Installing etcdsnapshot plugin..."
export HOME=/tmp
export OCTOSQL_NO_TELEMETRY=1
octosql plugin repository add https://raw.githubusercontent.com/tjungblu/octosql-plugin-etcdsnapshot/main/plugin_repository.json 2>&1
octosql plugin install etcdsnapshot/etcdsnapshot 2>&1

echo "Analyzing snapshot: ${SNAPSHOT_PATH}"
SNAPSHOT_DIR=$(dirname "$SNAPSHOT_PATH")
SNAPSHOT_FILE=$(basename "$SNAPSHOT_PATH")
cd "$SNAPSHOT_DIR"

# Get metadata
echo ""
echo "=== Snapshot Metadata ==="
octosql -ojson "SELECT * FROM ${SNAPSHOT_FILE}?meta=true" 2>/dev/null || echo "Warning: metadata query failed"

# Top 20 namespaces by total size
echo ""
echo "=== Top 20 Namespaces by Size ==="
octosql -ojson "SELECT namespace, COUNT(*) as key_count, SUM(valueSize) as total_bytes FROM ${SNAPSHOT_FILE} WHERE namespace IS NOT NULL GROUP BY namespace ORDER BY total_bytes DESC LIMIT 20" 2>/dev/null || echo "Warning: namespace query failed"

# Top 20 largest individual resources
echo ""
echo "=== Top 20 Largest Resources ==="
octosql -ojson "SELECT namespace, resourceType, name, valueSize FROM ${SNAPSHOT_FILE} WHERE namespace IS NOT NULL ORDER BY valueSize DESC LIMIT 20" 2>/dev/null || echo "Warning: largest resources query failed"

# Resource type distribution
echo ""
echo "=== Resource Type Distribution ==="
octosql -ojson "SELECT resourceType, COUNT(*) as count, SUM(valueSize) as total_bytes FROM ${SNAPSHOT_FILE} WHERE resourceType IS NOT NULL GROUP BY resourceType ORDER BY total_bytes DESC LIMIT 30" 2>/dev/null || echo "Warning: resource type query failed"

# Clean up snapshot if requested
if [ "$DELETE_SNAPSHOT" = "true" ]; then
    echo ""
    echo "Deleting snapshot file: ${SNAPSHOT_PATH}"
    rm -f "$SNAPSHOT_PATH"
fi

echo ""
echo "Analysis complete."

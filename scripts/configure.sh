#!/usr/bin/env bash
#
# configure.sh — print env exports for Azure and GCP CLI configuration
# pointing at a local AltoCirrus instance.
#
# Usage: eval $(./scripts/configure.sh [--host HOST] [--port PORT])

HOST=localhost
PORT=4567

while [ $# -gt 0 ]; do
  case "$1" in
    --host) HOST="$2"; shift 2 ;;
    --port) PORT="$2"; shift 2 ;;
    *)
      echo "Unknown option: $1" >&2
      echo "Usage: $0 [--host HOST] [--port PORT]" >&2
      exit 1
      ;;
  esac
done

BASE="http://${HOST}:${PORT}"

echo "# --- Azure CLI ---"
echo "export AZURE_AUTHORITY_HOST=${BASE}"
echo "export AZURE_KEYVAULT_URL=${BASE}"
echo "export ARM_ENDPOINT=${BASE}"

echo ""
echo "# --- GCP CLI ---"
echo "export STORAGE_EMULATOR_HOST=${HOST}:${PORT}"
echo "export SECRET_MANAGER_EMULATOR_HOST=${HOST}:${PORT}"
echo "export CLOUDSDK_API_ENDPOINT_OVERRIDES_SECRETMANAGER=${BASE}/"

echo ""
echo "# Run: eval \$(./scripts/configure.sh)"

#!/usr/bin/env bash
set -euo pipefail

# ---------------------------------------------------------------------------
# AltoCirrus CLI smoke test
# Builds the server, starts it, and exercises every emulated endpoint via curl.
# Requires: go, curl, jq  (does NOT require az or gcloud CLIs)
# ---------------------------------------------------------------------------

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
BINARY=/tmp/altocirrus-smoke-$$
PORT=14567  # Use a high port to avoid clashing with a running instance
BASE="http://localhost:${PORT}"
PID=""

PASS=0
FAIL=0
TOTAL=0

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

cleanup() {
    if [ -n "$PID" ]; then
        kill "$PID" 2>/dev/null || true
        wait "$PID" 2>/dev/null || true
    fi
    rm -f "$BINARY"
}
trap cleanup EXIT

# assert_status METHOD URL EXPECTED_STATUS [extra curl args...]
# Sends the request and checks that the HTTP status code matches.
assert_status() {
    local method="$1"; shift
    local url="$1"; shift
    local expected="$1"; shift
    # remaining args are passed to curl

    TOTAL=$((TOTAL + 1))

    local status
    status=$(curl -s -o /dev/null -w "%{http_code}" -X "$method" "$@" "$url")

    if [ "$status" = "$expected" ]; then
        PASS=$((PASS + 1))
        printf "  PASS  %s %s -> %s\n" "$method" "$url" "$status"
    else
        FAIL=$((FAIL + 1))
        printf "  FAIL  %s %s -> got %s, expected %s\n" "$method" "$url" "$status" "$expected"
    fi
}

# assert_body METHOD URL EXPECTED_STATUS JQ_FILTER EXPECTED_VALUE [extra curl args...]
# Like assert_status, but also checks a jq expression against the body.
assert_body() {
    local method="$1"; shift
    local url="$1"; shift
    local expected_status="$1"; shift
    local jq_filter="$1"; shift
    local expected_value="$1"; shift

    TOTAL=$((TOTAL + 1))

    local tmpfile
    tmpfile=$(mktemp)

    local status
    status=$(curl -s -o "$tmpfile" -w "%{http_code}" -X "$method" "$@" "$url")

    if [ "$status" != "$expected_status" ]; then
        FAIL=$((FAIL + 1))
        printf "  FAIL  %s %s -> got status %s, expected %s\n" "$method" "$url" "$status" "$expected_status"
        rm -f "$tmpfile"
        return
    fi

    local actual
    actual=$(jq -r "$jq_filter" < "$tmpfile" 2>/dev/null || echo "__JQ_ERROR__")
    rm -f "$tmpfile"

    if [ "$actual" = "$expected_value" ]; then
        PASS=$((PASS + 1))
        printf "  PASS  %s %s -> %s (%s = %s)\n" "$method" "$url" "$status" "$jq_filter" "$actual"
    else
        FAIL=$((FAIL + 1))
        printf "  FAIL  %s %s -> %s but %s = '%s', expected '%s'\n" "$method" "$url" "$status" "$jq_filter" "$actual" "$expected_value"
    fi
}

# ---------------------------------------------------------------------------
# Build
# ---------------------------------------------------------------------------

echo "==> Building altocirrus..."
(cd "$PROJECT_ROOT" && go build -o "$BINARY" ./cmd/altocirrus)

# ---------------------------------------------------------------------------
# Start server
# ---------------------------------------------------------------------------

echo "==> Starting server on port $PORT..."
ALTOCIRRUS_PORT=$PORT "$BINARY" &
PID=$!

# Wait for the server to become ready (up to 5 seconds).
for i in $(seq 1 50); do
    if curl -s -o /dev/null "$BASE/_altocirrus/health" 2>/dev/null; then
        break
    fi
    sleep 0.1
done

# Final readiness check
if ! curl -s -o /dev/null "$BASE/_altocirrus/health" 2>/dev/null; then
    echo "FATAL: server did not start within 5 seconds"
    exit 1
fi
echo "==> Server is ready (PID $PID)"
echo ""

# ===========================================================================
# Health & Reset
# ===========================================================================

echo "--- Health & Reset ---"
assert_body GET "$BASE/_altocirrus/health" 200 '.status' 'ok'

# ===========================================================================
# Azure Auth -- token endpoint
# ===========================================================================

echo ""
echo "--- Azure Auth ---"
assert_status POST "$BASE/00000000-0000-0000-0000-000000000001/oauth2/v2.0/token" 200 \
    -d "grant_type=client_credentials&client_id=test&client_secret=test&scope=test"

# OIDC discovery
assert_status GET "$BASE/.well-known/openid-configuration" 200

# JWKS
assert_status GET "$BASE/common/discovery/v2.0/keys" 200

# ===========================================================================
# Azure Key Vault -- secrets CRUD
# ===========================================================================

echo ""
echo "--- Azure Key Vault ---"
# Create secret
assert_body PUT "$BASE/secrets/smoke-secret" 200 '.value' 'smoke-value' \
    -H "Content-Type: application/json" \
    -d '{"value":"smoke-value"}'

# Get secret
assert_body GET "$BASE/secrets/smoke-secret" 200 '.value' 'smoke-value'

# List secrets (should contain our secret)
assert_status GET "$BASE/secrets" 200

# Delete secret
assert_status DELETE "$BASE/secrets/smoke-secret" 200

# Confirm deleted (should 404)
assert_status GET "$BASE/secrets/smoke-secret" 404

# ===========================================================================
# Azure ARM -- resource groups
# ===========================================================================

echo ""
echo "--- Azure ARM ---"
# Create resource group
assert_body PUT "$BASE/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/smoke-rg?api-version=2024-03-01" 200 \
    '.properties.provisioningState' 'Succeeded' \
    -H "Content-Type: application/json" \
    -d '{"location":"eastus"}'

# Get resource group
assert_body GET "$BASE/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/smoke-rg" 200 \
    '.name' 'smoke-rg'

# List resource groups
assert_status GET "$BASE/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups" 200

# List subscriptions
assert_status GET "$BASE/subscriptions" 200

# Delete resource group (returns 202 Accepted)
assert_status DELETE "$BASE/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/smoke-rg" 202

# Confirm deleted
assert_status GET "$BASE/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/smoke-rg" 404

# ===========================================================================
# Azure Blob Storage
# ===========================================================================

echo ""
echo "--- Azure Blob Storage ---"
ACCT="devstoreaccount1"

# Create container
assert_status PUT "$BASE/${ACCT}/smoke-container?restype=container" 201

# List containers
assert_status GET "$BASE/${ACCT}?comp=list" 200

# Upload blob
assert_status PUT "$BASE/${ACCT}/smoke-container/test.txt" 201 \
    -H "x-ms-blob-type: BlockBlob" \
    -H "Content-Type: text/plain" \
    -d "hello from smoke test"

# List blobs
assert_status GET "$BASE/${ACCT}/smoke-container?restype=container&comp=list" 200

# Download blob
TOTAL=$((TOTAL + 1))
BLOB_BODY=$(curl -s "$BASE/${ACCT}/smoke-container/test.txt")
if [ "$BLOB_BODY" = "hello from smoke test" ]; then
    PASS=$((PASS + 1))
    printf "  PASS  GET %s/%s/smoke-container/test.txt -> body matches\n" "$BASE" "$ACCT"
else
    FAIL=$((FAIL + 1))
    printf "  FAIL  GET %s/%s/smoke-container/test.txt -> body mismatch: '%s'\n" "$BASE" "$ACCT" "$BLOB_BODY"
fi

# HEAD blob (use -I instead of -X HEAD to avoid curl waiting for body)
TOTAL=$((TOTAL + 1))
HEAD_STATUS=$(curl -s -o /dev/null -w "%{http_code}" -I "$BASE/${ACCT}/smoke-container/test.txt")
if [ "$HEAD_STATUS" = "200" ]; then
    PASS=$((PASS + 1))
    printf "  PASS  HEAD %s/%s/smoke-container/test.txt -> %s\n" "$BASE" "$ACCT" "$HEAD_STATUS"
else
    FAIL=$((FAIL + 1))
    printf "  FAIL  HEAD %s/%s/smoke-container/test.txt -> got %s, expected 200\n" "$BASE" "$ACCT" "$HEAD_STATUS"
fi

# Delete blob (returns 202 Accepted)
assert_status DELETE "$BASE/${ACCT}/smoke-container/test.txt" 202

# Delete container (returns 202 Accepted)
assert_status DELETE "$BASE/${ACCT}/smoke-container?restype=container" 202

# ===========================================================================
# GCP Auth -- token endpoint
# ===========================================================================

echo ""
echo "--- GCP Auth ---"
assert_body POST "$BASE/token" 200 '.token_type' 'Bearer' \
    -d "grant_type=client_credentials"

# Alternate token endpoint
assert_status POST "$BASE/oauth2/v4/token" 200 \
    -d "grant_type=client_credentials"

# Metadata endpoints
assert_status GET "$BASE/computeMetadata/v1/project/project-id" 200 \
    -H "Metadata-Flavor: Google"
assert_status GET "$BASE/computeMetadata/v1/project/numeric-project-id" 200 \
    -H "Metadata-Flavor: Google"

# ===========================================================================
# GCP Secret Manager
# ===========================================================================

echo ""
echo "--- GCP Secret Manager ---"
# Create secret
assert_body POST "$BASE/v1/projects/local-project/secrets?secretId=smoke-secret" 200 \
    '.name' 'projects/local-project/secrets/smoke-secret' \
    -H "Content-Type: application/json" \
    -d '{"replication":{"automatic":{}}}'

# Add version (echo -n "hello" | base64 = aGVsbG8=)
assert_body POST "$BASE/v1/projects/local-project/secrets/smoke-secret:addVersion" 200 \
    '.state' 'ENABLED' \
    -H "Content-Type: application/json" \
    -d '{"payload":{"data":"aGVsbG8="}}'

# Access version 1
assert_body GET "$BASE/v1/projects/local-project/secrets/smoke-secret/versions/1:access" 200 \
    '.payload.data' 'aGVsbG8='

# Access latest
assert_status GET "$BASE/v1/projects/local-project/secrets/smoke-secret/versions/latest:access" 200

# Get secret metadata
assert_status GET "$BASE/v1/projects/local-project/secrets/smoke-secret" 200

# List secrets
assert_status GET "$BASE/v1/projects/local-project/secrets" 200

# Delete secret
assert_status DELETE "$BASE/v1/projects/local-project/secrets/smoke-secret" 200

# Confirm deleted (should 404)
assert_status GET "$BASE/v1/projects/local-project/secrets/smoke-secret" 404

# ===========================================================================
# GCP Cloud Storage (GCS)
# ===========================================================================

echo ""
echo "--- GCP Cloud Storage ---"
# Create bucket
assert_body POST "$BASE/storage/v1/b?project=local-project" 200 \
    '.name' 'smoke-bucket' \
    -H "Content-Type: application/json" \
    -d '{"name":"smoke-bucket"}'

# Get bucket
assert_body GET "$BASE/storage/v1/b/smoke-bucket" 200 \
    '.kind' 'storage#bucket'

# List buckets
assert_status GET "$BASE/storage/v1/b?project=local-project" 200

# Upload object (simple upload)
assert_body POST "$BASE/upload/storage/v1/b/smoke-bucket/o?uploadType=media&name=test.txt" 200 \
    '.name' 'test.txt' \
    -H "Content-Type: text/plain" \
    -d "hello world"

# List objects
assert_body GET "$BASE/storage/v1/b/smoke-bucket/o" 200 \
    '.kind' 'storage#objects'

# Get object metadata
assert_body GET "$BASE/storage/v1/b/smoke-bucket/o/test.txt" 200 \
    '.name' 'test.txt'

# Download object
TOTAL=$((TOTAL + 1))
GCS_BODY=$(curl -s "$BASE/storage/v1/b/smoke-bucket/o/test.txt?alt=media")
if [ "$GCS_BODY" = "hello world" ]; then
    PASS=$((PASS + 1))
    printf "  PASS  GET .../o/test.txt?alt=media -> body matches\n"
else
    FAIL=$((FAIL + 1))
    printf "  FAIL  GET .../o/test.txt?alt=media -> body mismatch: '%s'\n" "$GCS_BODY"
fi

# Delete object (returns 204 No Content)
assert_status DELETE "$BASE/storage/v1/b/smoke-bucket/o/test.txt" 204

# Delete bucket (returns 204 No Content)
assert_status DELETE "$BASE/storage/v1/b/smoke-bucket" 204

# ===========================================================================
# GCP Pub/Sub
# ===========================================================================

echo ""
echo "--- GCP Pub/Sub ---"
# Create topic
assert_body PUT "$BASE/v1/projects/local-project/topics/smoke-topic" 200 \
    '.name' 'projects/local-project/topics/smoke-topic'

# Get topic
assert_status GET "$BASE/v1/projects/local-project/topics/smoke-topic" 200

# List topics
assert_status GET "$BASE/v1/projects/local-project/topics" 200

# Create subscription
assert_body PUT "$BASE/v1/projects/local-project/subscriptions/smoke-sub" 200 \
    '.name' 'projects/local-project/subscriptions/smoke-sub' \
    -H "Content-Type: application/json" \
    -d '{"topic":"projects/local-project/topics/smoke-topic","ackDeadlineSeconds":10}'

# Get subscription
assert_status GET "$BASE/v1/projects/local-project/subscriptions/smoke-sub" 200

# List subscriptions
assert_status GET "$BASE/v1/projects/local-project/subscriptions" 200

# Publish a message (echo -n "smoke" | base64 = c21va2U=)
assert_body POST "$BASE/v1/projects/local-project/topics/smoke-topic:publish" 200 \
    '.messageIds | length' '1' \
    -H "Content-Type: application/json" \
    -d '{"messages":[{"data":"c21va2U="}]}'

# Pull message
assert_status POST "$BASE/v1/projects/local-project/subscriptions/smoke-sub:pull" 200 \
    -H "Content-Type: application/json" \
    -d '{"maxMessages":1}'

# Delete subscription
assert_status DELETE "$BASE/v1/projects/local-project/subscriptions/smoke-sub" 200

# Delete topic
assert_status DELETE "$BASE/v1/projects/local-project/topics/smoke-topic" 200

# ===========================================================================
# Reset state
# ===========================================================================

echo ""
echo "--- Reset ---"
assert_body POST "$BASE/_altocirrus/reset" 200 '.status' 'reset'

# ===========================================================================
# Summary
# ===========================================================================

echo ""
echo "==========================================="
echo "  Smoke test results: $PASS passed, $FAIL failed (out of $TOTAL)"
echo "==========================================="

if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
echo "All tests passed."

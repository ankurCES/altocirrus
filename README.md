<p align="center">
  <strong>AltoCirrus</strong><br>
  Local Azure + GCP cloud emulator for development, testing, and CI
</p>

<p align="center">
  <a href="https://opensource.org/licenses/MIT"><img src="https://img.shields.io/badge/license-MIT-blue.svg" alt="License: MIT"></a>
  <a href="https://go.dev/"><img src="https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go&logoColor=white" alt="Go 1.22+"></a>
  <a href="https://hub.docker.com/"><img src="https://img.shields.io/badge/docker-ready-2496ED?logo=docker&logoColor=white" alt="Docker"></a>
</p>

---

Free, open-source, zero-config. No account, no auth token, no feature gates.
Just `docker compose up`.

One process, one port, both clouds. AltoCirrus serves Azure and GCP REST APIs on a single endpoint using their naturally disjoint URL patterns -- no path prefixes, no proxying.

Inspired by [LocalStack](https://github.com/localstack/localstack) (AWS) and [floci](https://github.com/floci-io/floci) (also AWS). AltoCirrus does the same for Azure and GCP.

## Quick Start

### Docker (recommended)

```bash
docker compose up
```

### From source

```bash
go run ./cmd/altocirrus
```

### Verify it works

```bash
curl http://localhost:4567/_altocirrus/health
```

```json
{
  "status": "ok",
  "version": "0.1.0",
  "services": {
    "azure": ["auth", "keyvault", "arm"],
    "gcp": ["auth", "secretmanager", "storage"]
  }
}
```

## Supported Services

| Cloud | Service | Emulated Operations | API Endpoints |
|-------|---------|---------------------|---------------|
| Azure | **Entra ID (Auth)** | OAuth2 token issuance, OIDC discovery, JWKS | `POST /{tenantId}/oauth2/v2.0/token` |
| Azure | **Key Vault Secrets** | Create, read, list, delete (soft-delete) | `PUT/GET/DELETE /secrets/{name}` |
| Azure | **ARM Resource Groups** | Create, read, list, delete | `/subscriptions/{sub}/resourceGroups/{rg}` |
| GCP | **OAuth (Auth)** | Token issuance, metadata server | `POST /token`, `POST /oauth2/v4/token` |
| GCP | **Secret Manager** | Create secret, add version, access, list, delete | `/v1/projects/{project}/secrets/...` |
| GCP | **Cloud Storage** | Bucket CRUD, object upload (simple + resumable), download, list, delete | `/storage/v1/b/{bucket}/o/{object}` |

All auth endpoints return valid-looking tokens. Azure tokens are real RS256-signed JWTs with correct claims (`aud`, `iss`, `tid`, `oid`, `scp`, `exp`), so SDKs that introspect tokens will work.

## CLI Configuration

Point `az` and `gcloud` at your local AltoCirrus instance:

### Automatic (recommended)

```bash
eval $(./scripts/configure.sh)

# Custom host/port:
eval $(./scripts/configure.sh --host 192.168.1.10 --port 9000)
```

### Manual

```bash
# --- Azure CLI ---
export AZURE_AUTHORITY_HOST=http://localhost:4567
export AZURE_KEYVAULT_URL=http://localhost:4567
export ARM_ENDPOINT=http://localhost:4567

# --- GCP CLI ---
export STORAGE_EMULATOR_HOST=localhost:4567
export SECRET_MANAGER_EMULATOR_HOST=localhost:4567
export CLOUDSDK_API_ENDPOINT_OVERRIDES_SECRETMANAGER=http://localhost:4567/
```

## SDK Usage Examples

### Azure Key Vault -- Go

```go
import (
    "github.com/Azure/azure-sdk-for-go/sdk/azidentity"
    "github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"
)

// Set AZURE_AUTHORITY_HOST=http://localhost:4567
// Set AZURE_KEYVAULT_URL=http://localhost:4567
cred, _ := azidentity.NewClientSecretCredential("tenant", "client", "secret", nil)
client, _ := azsecrets.NewClient("http://localhost:4567", cred, nil)

// Create a secret
client.SetSecret(ctx, "my-secret", azsecrets.SetSecretParameters{
    Value: to.Ptr("super-secret-value"),
}, nil)

// Read it back
resp, _ := client.GetSecret(ctx, "my-secret", "", nil)
fmt.Println(*resp.Value) // "super-secret-value"
```

### Azure Key Vault -- Python

```python
from azure.identity import ClientSecretCredential
from azure.keyvault.secrets import SecretClient

# Set AZURE_AUTHORITY_HOST=http://localhost:4567
credential = ClientSecretCredential("tenant-id", "client-id", "client-secret")
client = SecretClient("http://localhost:4567", credential)

# Create and read
client.set_secret("my-secret", "super-secret-value")
secret = client.get_secret("my-secret")
print(secret.value)  # "super-secret-value"
```

### GCP Secret Manager -- Go

```go
import (
    secretmanager "cloud.google.com/go/secretmanager/apiv1"
    smpb "cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
    "google.golang.org/api/option"
)

// Set SECRET_MANAGER_EMULATOR_HOST=localhost:4567
client, _ := secretmanager.NewClient(ctx,
    option.WithEndpoint("http://localhost:4567"),
    option.WithoutAuthentication(),
)

// Create a secret
client.CreateSecret(ctx, &smpb.CreateSecretRequest{
    Parent:   "projects/local-project",
    SecretId: "my-secret",
    Secret:   &smpb.Secret{Replication: &smpb.Replication{...}},
})

// Add a version and access it
client.AddSecretVersion(ctx, &smpb.AddSecretVersionRequest{...})
resp, _ := client.AccessSecretVersion(ctx, &smpb.AccessSecretVersionRequest{
    Name: "projects/local-project/secrets/my-secret/versions/latest",
})
fmt.Println(string(resp.Payload.Data))
```

### GCP Secret Manager -- Python

```python
from google.cloud import secretmanager
import os

os.environ["SECRET_MANAGER_EMULATOR_HOST"] = "localhost:4567"
client = secretmanager.SecretManagerServiceClient()

# Create, add version, access
client.create_secret(request={"parent": "projects/local-project", "secret_id": "my-secret", ...})
client.add_secret_version(request={"parent": "projects/local-project/secrets/my-secret", ...})
response = client.access_secret_version(
    request={"name": "projects/local-project/secrets/my-secret/versions/latest"}
)
print(response.payload.data.decode("utf-8"))
```

### Reset all state between tests

```bash
curl -X POST http://localhost:4567/_altocirrus/reset
# {"status":"reset"}
```

## Configuration Reference

All configuration is via environment variables. Every value has a sensible default.

| Variable | Default | Description |
|----------|---------|-------------|
| `ALTOCIRRUS_PORT` | `4567` | Port the emulator listens on |
| `ALTOCIRRUS_AZURE_SUBSCRIPTION_ID` | `00000000-0000-0000-0000-000000000000` | Azure subscription ID returned by ARM APIs |
| `ALTOCIRRUS_AZURE_TENANT_ID` | `00000000-0000-0000-0000-000000000001` | Azure tenant ID used in auth tokens |
| `ALTOCIRRUS_AZURE_REGION` | `eastus` | Azure region returned in Key Vault / ARM headers |
| `ALTOCIRRUS_GCP_PROJECT_ID` | `local-project` | GCP project ID used in resource names |
| `ALTOCIRRUS_GCP_PROJECT_NUMBER` | `123456789` | GCP numeric project ID (metadata endpoint) |
| `ALTOCIRRUS_GCP_REGION` | `us-central1` | GCP region for resource metadata |

## Architecture

AltoCirrus is a single Go binary that multiplexes Azure and GCP REST APIs on one HTTP port. Azure and GCP APIs use naturally disjoint URL path patterns (`/subscriptions/...` vs `/v1/projects/...`), so no path-prefix routing or reverse-proxy layer is needed. All state is held in a thread-safe in-memory key-value store, namespaced per service. A `POST /_altocirrus/reset` endpoint clears all namespaces for clean test isolation.

```
cmd/
  altocirrus/
    main.go              # entry point, wires everything together

internal/
  config/
    config.go            # env-var-driven configuration
  server/
    server.go            # HTTP mux, health, reset, logging middleware
    errors.go            # Azure/GCP error envelopes, JSON helpers
  storage/
    memory.go            # thread-safe in-memory Store interface + implementation
  azure/
    auth/auth.go         # Entra ID: OAuth2 tokens (RS256 JWT), OIDC, JWKS
    keyvault/keyvault.go # Key Vault secrets: CRUD with versioning, soft-delete
    arm/arm.go           # ARM: subscriptions, resource group CRUD
  gcp/
    auth/auth.go         # OAuth2 token endpoint, compute metadata
    secretmanager/       # Secret Manager: secrets + versions CRUD
      secretmanager.go
    storage/gcs.go       # Cloud Storage: buckets, objects, resumable uploads

scripts/
  configure.sh           # prints shell exports for az/gcloud CLI overrides

Dockerfile               # multi-stage build, alpine runtime (~15 MB image)
docker-compose.yml       # one-command startup
```

## Building from Source

```bash
# Requires Go 1.22+

# Build
go build -o altocirrus ./cmd/altocirrus

# Run
./altocirrus

# Run tests
go test ./...

# Build Docker image
docker build -t altocirrus .
docker run -p 4567:4567 altocirrus
```

## Contributing

Contributions are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines on getting started, adding new services, and commit conventions.

## License

[MIT](LICENSE)

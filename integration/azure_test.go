//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	azarm "github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azqueue"
)

// azuriteKey is the well-known Azurite shared key — our server ignores HMAC
// validation so any valid base64 value works; using the canonical Azurite key
// makes connection strings portable.
const azuriteKey = "Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw=="

// tlsOpts returns azcore.ClientOptions pointing auth and ARM at the TLS test server.
// azidentity v1.x enforces HTTPS for authority host, so we use tlsTestServer (not testServer).
// Transport is set to tlsTestServer.Client() so the self-signed test cert is trusted.
func tlsOpts() azcore.ClientOptions {
	return azcore.ClientOptions{
		Cloud: cloud.Configuration{
			ActiveDirectoryAuthorityHost: tlsTestServer.URL,
			Services: map[cloud.ServiceName]cloud.ServiceConfiguration{
				cloud.ResourceManager: {
					Endpoint: tlsTestServer.URL + "/",
					// ponytail: audience kept as prod value — emulator ignores token validation
					Audience: "https://management.azure.com/",
				},
			},
		},
		Transport: tlsTestServer.Client(), // trust the test TLS cert
	}
}

// newCred returns a ClientSecretCredential pointed at the TLS test server auth endpoint.
// DisableInstanceDiscovery skips the external call to login.microsoftonline.com that
// azidentity makes by default to validate the authority host.
func newCred(t *testing.T) *azidentity.ClientSecretCredential {
	t.Helper()
	cred, err := azidentity.NewClientSecretCredential(
		testCfg.Azure.TenantID,
		"test-client-id",
		"test-client-secret",
		&azidentity.ClientSecretCredentialOptions{
			ClientOptions:            tlsOpts(),
			DisableInstanceDiscovery: true,
		},
	)
	if err != nil {
		t.Fatalf("NewClientSecretCredential: %v", err)
	}
	return cred
}

// ── 1. Auth ──────────────────────────────────────────────────────────────────

func TestAzureAuth(t *testing.T) {
	cred := newCred(t)
	tok, err := cred.GetToken(context.Background(), policy.TokenRequestOptions{
		Scopes: []string{"https://management.azure.com/.default"},
	})
	if err != nil {
		t.Fatalf("GetToken: %v", err)
	}
	if tok.Token == "" {
		t.Fatal("expected non-empty access token")
	}
}

// ── 2. Key Vault secrets ──────────────────────────────────────────────────────

func TestAzureKeyVault(t *testing.T) {
	cred := newCred(t)
	// Vault URL must be HTTPS; azcore rejects bearer-token requests to plain HTTP.
	kv, err := azsecrets.NewClient(tlsTestServer.URL, cred, &azsecrets.ClientOptions{
		ClientOptions: tlsOpts(),
		// ponytail: emulator returns 200 directly, no WWW-Authenticate challenge
		DisableChallengeResourceVerification: true,
	})
	if err != nil {
		t.Fatalf("azsecrets.NewClient: %v", err)
	}

	ctx := context.Background()
	const secretName = "integration-secret"
	const secretVal = "hunter2"

	t.Run("SetSecret", func(t *testing.T) {
		_, err := kv.SetSecret(ctx, secretName, azsecrets.SetSecretParameters{
			Value: to.Ptr(secretVal),
		}, nil)
		if err != nil {
			t.Fatalf("SetSecret: %v", err)
		}
	})

	t.Run("GetSecret", func(t *testing.T) {
		resp, err := kv.GetSecret(ctx, secretName, "", nil)
		if err != nil {
			t.Fatalf("GetSecret: %v", err)
		}
		if resp.Value == nil || *resp.Value != secretVal {
			t.Fatalf("got value %v, want %q", resp.Value, secretVal)
		}
	})

	t.Run("ListSecrets", func(t *testing.T) {
		pager := kv.NewListSecretPropertiesPager(nil)
		found := false
		for pager.More() {
			page, err := pager.NextPage(ctx)
			if err != nil {
				t.Fatalf("ListSecrets: %v", err)
			}
			for _, s := range page.Value {
				if s.ID != nil && s.ID.Name() == secretName {
					found = true
				}
			}
		}
		if !found {
			t.Fatalf("secret %q not found in list", secretName)
		}
	})

	t.Run("DeleteSecret", func(t *testing.T) {
		_, err := kv.DeleteSecret(ctx, secretName, nil)
		if err != nil {
			t.Fatalf("DeleteSecret: %v", err)
		}
		// Confirm it's gone.
		_, err = kv.GetSecret(ctx, secretName, "", nil)
		if err == nil {
			t.Fatal("expected error getting deleted secret, got nil")
		}
	})
}

// ── 3. ARM Resource Groups ───────────────────────────────────────────────────

func TestAzureARM(t *testing.T) {
	cred := newCred(t)
	// ARM API calls go to plain HTTP — no TLS cert trust issue.
	// InsecureAllowCredentialWithHTTP lets the SDK send the bearer token over HTTP.
	// Auth still routes through the TLS server via cred's own transport.
	armOpts := &azarm.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			Cloud: cloud.Configuration{
				ActiveDirectoryAuthorityHost: tlsTestServer.URL,
				Services: map[cloud.ServiceName]cloud.ServiceConfiguration{
					cloud.ResourceManager: {
						Endpoint: testServer.URL + "/",
						Audience: "https://management.azure.com/",
					},
				},
			},
			InsecureAllowCredentialWithHTTP: true,
		},
	}

	rgClient, err := armresources.NewResourceGroupsClient(
		testCfg.Azure.SubscriptionID, cred, armOpts,
	)
	if err != nil {
		t.Fatalf("NewResourceGroupsClient: %v", err)
	}

	ctx := context.Background()
	const rgName = "integration-rg"
	const location = "eastus"

	t.Run("CreateOrUpdate", func(t *testing.T) {
		_, err := rgClient.CreateOrUpdate(ctx, rgName, armresources.ResourceGroup{
			Location: to.Ptr(location),
		}, nil)
		if err != nil {
			t.Fatalf("CreateOrUpdate: %v", err)
		}
	})

	t.Run("Get", func(t *testing.T) {
		resp, err := rgClient.Get(ctx, rgName, nil)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if resp.Name == nil || *resp.Name != rgName {
			t.Fatalf("got name %v, want %q", resp.Name, rgName)
		}
		if resp.Location == nil || *resp.Location != location {
			t.Fatalf("got location %v, want %q", resp.Location, location)
		}
	})

	t.Run("List", func(t *testing.T) {
		pager := rgClient.NewListPager(nil)
		found := false
		for pager.More() {
			page, err := pager.NextPage(ctx)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			for _, rg := range page.Value {
				if rg.Name != nil && *rg.Name == rgName {
					found = true
				}
			}
		}
		if !found {
			t.Fatalf("resource group %q not found in list", rgName)
		}
	})

	t.Run("Delete", func(t *testing.T) {
		poller, err := rgClient.BeginDelete(ctx, rgName, nil)
		if err != nil {
			t.Fatalf("BeginDelete: %v", err)
		}
		if _, err := poller.PollUntilDone(ctx, nil); err != nil {
			t.Fatalf("PollUntilDone: %v", err)
		}
	})
}

// ── 4. Blob Storage ───────────────────────────────────────────────────────────

func TestAzureBlob(t *testing.T) {
	// Shared key auth; our server does not validate HMAC signatures.
	sharedKey, err := azblob.NewSharedKeyCredential("devstoreaccount1", azuriteKey)
	if err != nil {
		t.Fatalf("NewSharedKeyCredential: %v", err)
	}

	// Azurite-style path-based URL: {host}/{accountName}
	serviceURL := testServer.URL + "/devstoreaccount1"
	blobClient, err := azblob.NewClientWithSharedKeyCredential(serviceURL, sharedKey, nil)
	if err != nil {
		t.Fatalf("azblob.NewClientWithSharedKeyCredential: %v", err)
	}

	ctx := context.Background()
	const containerName = "integration-container"
	const blobName = "hello.txt"
	content := []byte("hello, altocirrus!")

	t.Run("CreateContainer", func(t *testing.T) {
		_, err := blobClient.CreateContainer(ctx, containerName, nil)
		if err != nil {
			t.Fatalf("CreateContainer: %v", err)
		}
	})

	t.Run("UploadBlob", func(t *testing.T) {
		_, err := blobClient.UploadBuffer(ctx, containerName, blobName, content, nil)
		if err != nil {
			t.Fatalf("UploadBuffer: %v", err)
		}
	})

	t.Run("DownloadBlob", func(t *testing.T) {
		resp, err := blobClient.DownloadStream(ctx, containerName, blobName, nil)
		if err != nil {
			t.Fatalf("DownloadStream: %v", err)
		}
		defer resp.Body.Close()
		got, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		if !bytes.Equal(got, content) {
			t.Fatalf("downloaded %q, want %q", got, content)
		}
	})

	t.Run("ListBlobs", func(t *testing.T) {
		pager := blobClient.NewListBlobsFlatPager(containerName, nil)
		found := false
		for pager.More() {
			page, err := pager.NextPage(ctx)
			if err != nil {
				t.Fatalf("ListBlobs: %v", err)
			}
			for _, b := range page.Segment.BlobItems {
				if b.Name != nil && *b.Name == blobName {
					found = true
				}
			}
		}
		if !found {
			t.Fatalf("blob %q not found in listing", blobName)
		}
	})

	t.Run("DeleteBlob", func(t *testing.T) {
		_, err := blobClient.DeleteBlob(ctx, containerName, blobName, nil)
		if err != nil {
			t.Fatalf("DeleteBlob: %v", err)
		}
	})

	t.Run("DeleteContainer", func(t *testing.T) {
		_, err := blobClient.DeleteContainer(ctx, containerName, nil)
		if err != nil {
			t.Fatalf("DeleteContainer: %v", err)
		}
	})
}

// ── 5. Queue Storage ─────────────────────────────────────────────────────────

func TestAzureQueue(t *testing.T) {
	// Queue account is devstoreaccount1queue (not devstoreaccount1) to avoid
	// ServeMux path conflicts with blob storage. See queuestorage package.
	const queueAccount = "devstoreaccount1queue"
	sharedKey, err := azqueue.NewSharedKeyCredential(queueAccount, azuriteKey)
	if err != nil {
		t.Fatalf("NewSharedKeyCredential: %v", err)
	}

	const queueName = "integration-queue"
	// Azurite-style path-based URL: {host}/{accountName}/{queueName}
	queueURL := testServer.URL + "/" + queueAccount + "/" + queueName
	qClient, err := azqueue.NewQueueClientWithSharedKeyCredential(queueURL, sharedKey, nil)
	if err != nil {
		t.Fatalf("NewQueueClientWithSharedKeyCredential: %v", err)
	}

	ctx := context.Background()
	const msgContent = "hello from queue"

	t.Run("CreateQueue", func(t *testing.T) {
		_, err := qClient.Create(ctx, nil)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
	})

	var msgID, popReceipt string

	t.Run("SendMessage", func(t *testing.T) {
		resp, err := qClient.EnqueueMessage(ctx, msgContent, nil)
		if err != nil {
			t.Fatalf("EnqueueMessage: %v", err)
		}
		if len(resp.Messages) == 0 {
			t.Fatal("expected enqueued message in response")
		}
		msgID = *resp.Messages[0].MessageID
		popReceipt = *resp.Messages[0].PopReceipt
	})

	t.Run("ReceiveMessages", func(t *testing.T) {
		resp, err := qClient.DequeueMessages(ctx, nil)
		if err != nil {
			t.Fatalf("DequeueMessages: %v", err)
		}
		if len(resp.Messages) == 0 {
			t.Fatal("expected at least one message")
		}
		// Update receipt from dequeue — it rotates on each dequeue call.
		msgID = *resp.Messages[0].MessageID
		popReceipt = *resp.Messages[0].PopReceipt
		got := *resp.Messages[0].MessageText
		if got != msgContent {
			t.Fatalf("got message %q, want %q", got, msgContent)
		}
	})

	t.Run("DeleteMessage", func(t *testing.T) {
		_, err := qClient.DeleteMessage(ctx, msgID, popReceipt, nil)
		if err != nil {
			t.Fatalf("DeleteMessage: %v", err)
		}
	})

	t.Run("DeleteQueue", func(t *testing.T) {
		_, err := qClient.Delete(ctx, nil)
		if err != nil {
			t.Fatalf("Delete: %v", err)
		}
	})
}

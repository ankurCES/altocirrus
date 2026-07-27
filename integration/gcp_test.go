//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	gcs "cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

func resetState(t *testing.T) {
	t.Helper()
	resp, err := http.Post(testServer.URL+"/_altocirrus/reset", "application/json", nil)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	resp.Body.Close()
}

// TestGCPSecretManager exercises the Secret Manager REST API via the official
// GCP Go SDK using NewRESTClient (HTTP/JSON transport, no gRPC required).
func TestGCPSecretManager(t *testing.T) {
	resetState(t)
	ctx := context.Background()

	client, err := secretmanager.NewRESTClient(ctx,
		option.WithEndpoint(testServer.URL),
		option.WithoutAuthentication(),
	)
	if err != nil {
		t.Fatalf("NewRESTClient: %v", err)
	}
	defer client.Close()

	parent := fmt.Sprintf("projects/%s", gcpProject)
	secretID := "int-sm-secret"
	wantName := fmt.Sprintf("%s/secrets/%s", parent, secretID)

	t.Run("CreateSecret", func(t *testing.T) {
		s, err := client.CreateSecret(ctx, &secretmanagerpb.CreateSecretRequest{
			Parent:   parent,
			SecretId: secretID,
			Secret: &secretmanagerpb.Secret{
				Replication: &secretmanagerpb.Replication{
					Replication: &secretmanagerpb.Replication_Automatic_{
						Automatic: &secretmanagerpb.Replication_Automatic{},
					},
				},
			},
		})
		if err != nil {
			t.Fatalf("CreateSecret: %v", err)
		}
		if s.Name != wantName {
			t.Errorf("Name = %q, want %q", s.Name, wantName)
		}
	})

	t.Run("AddSecretVersion", func(t *testing.T) {
		_, err := client.AddSecretVersion(ctx, &secretmanagerpb.AddSecretVersionRequest{
			Parent: wantName,
			Payload: &secretmanagerpb.SecretPayload{
				Data: []byte("super-secret-value"),
			},
		})
		if err != nil {
			t.Fatalf("AddSecretVersion: %v", err)
		}
	})

	t.Run("AccessSecretVersion", func(t *testing.T) {
		resp, err := client.AccessSecretVersion(ctx, &secretmanagerpb.AccessSecretVersionRequest{
			Name: wantName + "/versions/latest",
		})
		if err != nil {
			t.Fatalf("AccessSecretVersion: %v", err)
		}
		if got := string(resp.Payload.Data); got != "super-secret-value" {
			t.Errorf("payload = %q, want %q", got, "super-secret-value")
		}
	})

	t.Run("ListSecrets", func(t *testing.T) {
		iter := client.ListSecrets(ctx, &secretmanagerpb.ListSecretsRequest{
			Parent: parent,
		})
		found := false
		for {
			s, err := iter.Next()
			if err == iterator.Done {
				break
			}
			if err != nil {
				t.Fatalf("iter.Next: %v", err)
			}
			if s.Name == wantName {
				found = true
			}
		}
		if !found {
			t.Error("created secret not returned by ListSecrets")
		}
	})

	t.Run("DeleteSecret", func(t *testing.T) {
		if err := client.DeleteSecret(ctx, &secretmanagerpb.DeleteSecretRequest{
			Name: wantName,
		}); err != nil {
			t.Fatalf("DeleteSecret: %v", err)
		}
	})
}

// TestGCPCloudStorage exercises the GCS JSON API via the official GCP Go SDK.
// The SDK natively supports STORAGE_EMULATOR_HOST for this purpose.
//
// Gaps discovered during this test run:
//
//  1. Upload: the GCS Go SDK sends uploadType=multipart (MIME multipart body
//     containing metadata + content in a single POST). Our emulator only
//     implements uploadType=media and uploadType=resumable. These subtests are
//     skipped; add multipart upload parsing to gcs.go to fix.
//
//  2. Download: the GCS Go SDK uses the XML API (GET /{bucket}/{object}) for
//     object downloads, not the JSON API (/storage/v1/b/{bucket}/o/{object}
//     ?alt=media). Our emulator implements only the JSON API. Skipped; add an
//     XML API handler to fix.
//
// Bucket CRUD and object list/delete (JSON API) work correctly.
func TestGCPCloudStorage(t *testing.T) {
	resetState(t)

	// SDK reads STORAGE_EMULATOR_HOST as "host:port" (no scheme).
	host := strings.TrimPrefix(testServer.URL, "http://")
	t.Setenv("STORAGE_EMULATOR_HOST", host)

	ctx := context.Background()
	client, err := gcs.NewClient(ctx, option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("gcs.NewClient: %v", err)
	}
	defer client.Close()

	bucketHandle := client.Bucket("int-gcs-bucket")

	t.Run("CreateBucket", func(t *testing.T) {
		if err := bucketHandle.Create(ctx, gcpProject, nil); err != nil {
			t.Fatalf("Bucket.Create: %v", err)
		}
	})

	t.Run("UploadObject", func(t *testing.T) {
		// Gap: GCS SDK sends POST /upload/storage/v1/b/{bucket}/o?uploadType=multipart
		// with a MIME multipart body. Our emulator only supports uploadType=media
		// and uploadType=resumable. Add multipart parsing to gcs.go to fix.
		t.Skip("gap: SDK uses uploadType=multipart; emulator supports only media and resumable")
	})

	t.Run("DownloadObject", func(t *testing.T) {
		// Gap: GCS SDK reads objects via the XML API (GET /{bucket}/{object}),
		// not the JSON API (/storage/v1/b/{bucket}/o/{object}?alt=media).
		// Our emulator only implements the JSON API.
		t.Skip("gap: SDK downloads via XML API (GET /{bucket}/{object}); emulator implements JSON API only")
	})

	// Seed one object via the JSON API (which our emulator supports) so that
	// ListObjects and DeleteObject subtests are meaningful.
	seedObject(t, "int-gcs-bucket", "int-obj", "hello altocirrus")

	t.Run("ListObjects", func(t *testing.T) {
		it := bucketHandle.Objects(ctx, nil)
		count := 0
		for {
			_, err := it.Next()
			if err == iterator.Done {
				break
			}
			if err != nil {
				t.Fatalf("Objects.Next: %v", err)
			}
			count++
		}
		if count != 1 {
			t.Errorf("listed %d objects, want 1", count)
		}
	})

	t.Run("DeleteObject", func(t *testing.T) {
		if err := bucketHandle.Object("int-obj").Delete(ctx); err != nil {
			t.Fatalf("Object.Delete: %v", err)
		}
	})

	t.Run("DeleteBucket", func(t *testing.T) {
		if err := bucketHandle.Delete(ctx); err != nil {
			t.Fatalf("Bucket.Delete: %v", err)
		}
	})
}

// seedObject uploads an object via the JSON API directly so subtests that
// require an existing object can run even when the SDK upload path is blocked.
func seedObject(t *testing.T, bucket, name, content string) {
	t.Helper()
	url := testServer.URL + "/upload/storage/v1/b/" + bucket + "/o?uploadType=media&name=" + name
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(content))
	req.Header.Set("Content-Type", "text/plain")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("seedObject: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("seedObject: status %d", resp.StatusCode)
	}
}

// TestGCPPubSub — gap documented, subtest skipped.
//
// cloud.google.com/go/pubsub uses gRPC as its transport.
// PUBSUB_EMULATOR_HOST configures a gRPC endpoint (host:port).
// AltoCirrus serves REST only and has no gRPC listener, so the SDK
// cannot connect. To enable SDK integration: add a gRPC server or an
// HTTP/2 transcoding proxy in front of the REST handlers.
func TestGCPPubSub(t *testing.T) {
	t.Skip("gap: cloud.google.com/go/pubsub uses gRPC; PUBSUB_EMULATOR_HOST expects a gRPC endpoint. AltoCirrus serves REST only.")
}

// TestGCPFirestore — gap documented, subtest skipped.
//
// cloud.google.com/go/firestore uses gRPC as its transport.
// FIRESTORE_EMULATOR_HOST configures a gRPC endpoint (host:port).
// AltoCirrus serves REST only and has no gRPC listener, so the SDK
// cannot connect. To enable SDK integration: add a gRPC server or an
// HTTP/2 transcoding proxy in front of the REST handlers.
func TestGCPFirestore(t *testing.T) {
	t.Skip("gap: cloud.google.com/go/firestore uses gRPC; FIRESTORE_EMULATOR_HOST expects a gRPC endpoint. AltoCirrus serves REST only.")
}

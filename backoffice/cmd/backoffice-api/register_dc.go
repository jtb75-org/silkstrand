package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"

	"github.com/jtb75/silkstrand/backoffice/internal/config"
	"github.com/jtb75/silkstrand/backoffice/internal/crypto"
	"github.com/jtb75/silkstrand/backoffice/internal/model"
	"github.com/jtb75/silkstrand/backoffice/internal/store"
)

// registerDC idempotently registers (or updates) a data center row in the
// backoffice database. It's the worker behind the k3s gitops bootstrap Job:
// each dc-* namespace runs `backoffice-api register-dc ...` so the data
// center self-registers, instead of a human POSTing to /api/v1/data-centers.
//
// The DC's internal API key is encrypted at rest with ENCRYPTION_KEY (the same
// AES-256-GCM the HTTP handler uses), so this needs Go code — a plain SQL
// insert can't produce the ciphertext.
//
// Idempotent by --name: re-running updates the existing row, which is what a
// Job that fires on every deploy needs. It assumes the schema already exists
// (the backoffice deployment owns migrations); it does not migrate.
func registerDC(args []string) error {
	fs := flag.NewFlagSet("register-dc", flag.ContinueOnError)
	name := fs.String("name", "", "data center name; the idempotency key (e.g. dc-us)")
	region := fs.String("region", "", "region label (e.g. us-central1)")
	apiURL := fs.String("api-url", "", "DC API base URL, typically in-cluster service DNS")
	apiKey := fs.String("api-key", "", "DC internal API key; prefer the DC_INTERNAL_API_KEY env var over argv (argv is visible in ps)")
	environment := fs.String("environment", model.DCEnvProd, "stage|prod")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Prefer the env var so the key isn't exposed on the command line / in ps.
	// The gitops Job sets DC_INTERNAL_API_KEY from the ESO-synced Secret.
	if *apiKey == "" {
		*apiKey = os.Getenv("DC_INTERNAL_API_KEY")
	}

	switch {
	case *name == "":
		return fmt.Errorf("register-dc: --name is required")
	case *region == "":
		return fmt.Errorf("register-dc: --region is required")
	case *apiURL == "":
		return fmt.Errorf("register-dc: --api-url is required")
	case *apiKey == "":
		return fmt.Errorf("register-dc: --api-key or DC_INTERNAL_API_KEY is required")
	}
	if *environment != model.DCEnvStage && *environment != model.DCEnvProd {
		return fmt.Errorf("register-dc: --environment must be 'stage' or 'prod', got %q", *environment)
	}

	normURL, err := normalizeAPIURL(*apiURL)
	if err != nil {
		return fmt.Errorf("register-dc: %w", err)
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	if len(cfg.EncryptionKey) == 0 {
		return fmt.Errorf("register-dc: ENCRYPTION_KEY must be set to encrypt the DC API key")
	}

	pgStore, err := store.NewPostgresStore(cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}
	defer pgStore.Close()

	encrypted, err := crypto.Encrypt([]byte(*apiKey), cfg.EncryptionKey)
	if err != nil {
		return fmt.Errorf("encrypting DC API key: %w", err)
	}

	dc := model.DataCenter{
		Name:            *name,
		Region:          *region,
		Environment:     *environment,
		APIURL:          normURL,
		APIKeyEncrypted: encrypted,
		Status:          model.DCStatusActive,
	}

	ctx := context.Background()
	existing, err := pgStore.GetDataCenterByName(ctx, *name)
	if err != nil {
		return fmt.Errorf("looking up data center %q: %w", *name, err)
	}

	if existing != nil {
		if _, err := pgStore.UpdateDataCenter(ctx, existing.ID, dc); err != nil {
			return fmt.Errorf("updating data center %q: %w", *name, err)
		}
		slog.Info("register-dc: updated data center", "name", *name, "id", existing.ID, "api_url", normURL)
		return nil
	}

	created, err := pgStore.CreateDataCenter(ctx, dc)
	if err != nil {
		return fmt.Errorf("creating data center %q: %w", *name, err)
	}
	slog.Info("register-dc: created data center", "name", *name, "id", created.ID, "api_url", normURL)
	return nil
}

// normalizeAPIURL validates and canonicalizes a DC API base URL for storage.
// Mirrors backoffice/internal/dcclient.NormalizeBaseURL (separate PR); the two
// should be consolidated once both land. Accepts in-cluster service DNS and
// public https URLs; trims a trailing slash so later path joins don't double up.
func normalizeAPIURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("api-url is not a valid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("api-url scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("api-url must include a host")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

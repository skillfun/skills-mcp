package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"skillfun-mcp/internal/auth"
	bundlepkg "skillfun-mcp/internal/bundle"
	"skillfun-mcp/internal/mcp"
)

func TestEndToEndBundleCatalogFlowWithRealPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real e2e test in short mode")
	}

	db, cleanupPostgres := startTestPostgres(t)
	defer cleanupPostgres()

	if err := ensureSchema(db); err != nil {
		t.Fatalf("ensureSchema() error = %v", err)
	}

	t.Setenv("BUNDLE_ADMIN_TOKEN", "secret-token")
	gateway := httptest.NewServer(newEngine(
		db,
		mcp.NewSchemaAggregator(db),
		bundlepkg.NewStore(db, bundlepkg.WithSkillSyncer(stubSkillStorage{})),
		stubSkillStorage{},
	))
	defer gateway.Close()

	registerBundle(t, gateway.URL, "secret-token")

	if _, err := db.Exec(
		`INSERT INTO payment_proofs (proof, grant_type, grant_target) VALUES ($1, $2, $3)`,
		"proof-e2e",
		"bundle",
		"weather",
	); err != nil {
		t.Fatalf("insert payment proof error = %v", err)
	}

	tools := fetchBundleTools(t, gateway.URL, "wx7abcde9f", "proof-e2e", "weather")
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "current" {
		t.Fatalf("unexpected discovered tools: %#v", tools.Tools)
	}
}

func startTestPostgres(t *testing.T) (*sql.DB, func()) {
	t.Helper()

	initdbPath, err := exec.LookPath("initdb")
	if err != nil {
		t.Skip("initdb not available")
	}
	pgCtlPath, err := exec.LookPath("pg_ctl")
	if err != nil {
		t.Skip("pg_ctl not available")
	}

	dataDir := filepath.Join(t.TempDir(), "pgdata")
	logFile := filepath.Join(t.TempDir(), "postgres.log")
	port := freeTCPPort(t)

	initdbCmd := exec.Command(initdbPath, "-D", dataDir, "-A", "trust", "-U", "postgres", "--encoding=UTF8", "--locale=C")
	if output, err := initdbCmd.CombinedOutput(); err != nil {
		t.Fatalf("initdb error: %v\n%s", err, string(output))
	}

	startCmd := exec.Command(
		pgCtlPath,
		"-D", dataDir,
		"-l", logFile,
		"-o", fmt.Sprintf("-F -p %d -c listen_addresses=127.0.0.1", port),
		"-w",
		"start",
	)
	if output, err := startCmd.CombinedOutput(); err != nil {
		t.Fatalf("pg_ctl start error: %v\n%s", err, string(output))
	}

	dsn := fmt.Sprintf("postgres://postgres@127.0.0.1:%d/postgres?sslmode=disable", port)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("db.PingContext() error = %v", err)
	}

	cleanup := func() {
		_ = db.Close()
		stopCmd := exec.Command(pgCtlPath, "-D", dataDir, "-m", "fast", "-w", "stop")
		_, _ = stopCmd.CombinedOutput()
	}

	return db, cleanup
}

func freeTCPPort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	defer listener.Close()

	return listener.Addr().(*net.TCPAddr).Port
}

func registerBundle(t *testing.T, gatewayURL string, adminToken string) {
	t.Helper()

	request, err := http.NewRequest(
		http.MethodPost,
		gatewayURL+"/v1/mcp/bundles",
		bytes.NewBufferString(`{"bundleName":"weather","displayName":"Weather Bundle","description":"Weather tools","subdomain":"wx7abcde9f","skills":[{"nftId":1001,"name":"current","description":"Get current weather","inputSchema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]},"githubUrl":"https://github.com/example/weather-skill/tree/main/skills/current"}]}`),
	)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+adminToken)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("register bundle request error = %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("register bundle status = %d, want %d, body=%s", response.StatusCode, http.StatusCreated, string(body))
	}
}

func fetchBundleTools(t *testing.T, gatewayURL string, subdomain string, proof string, cursorContext string) mcp.ToolsListResponse {
	t.Helper()

	request, err := http.NewRequest(http.MethodGet, gatewayURL+"/"+subdomain+"/tools?cursor_context="+cursorContext, nil)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	request.Header.Set(auth.PaymentProofHeader, proof)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("fetch bundle tools request error = %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("fetch bundle tools status = %d, want %d, body=%s", response.StatusCode, http.StatusOK, string(body))
	}

	var payload mcp.ToolsListResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}

	return payload
}

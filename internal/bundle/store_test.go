package bundle

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
)

func TestUpsertBundleRejectsSubdomainCooldown(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	store := NewStore(db)
	mock.ExpectQuery(`(?s)SELECT bundle_name, subdomain, display_name, COALESCE\(description, ''\), is_active\s+FROM bundles.*`).
		WithArgs("weather").
		WillReturnRows(sqlmock.NewRows([]string{"bundle_name", "subdomain", "display_name", "description", "is_active"}).
			AddRow("weather", "weatherabcd", "Weather", "desc", true))
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT changed_at\s+FROM bundle_subdomain_changes.*`).
		WithArgs("weather").
		WillReturnRows(sqlmock.NewRows([]string{"changed_at"}).AddRow(time.Now()))
	mock.ExpectRollback()

	_, err = store.UpsertBundle(context.Background(), UpsertBundleInput{
		BundleName:  "weather",
		Subdomain:   "weatherxyz1",
		DisplayName: "Weather",
		Description: "desc",
		IsActive:    true,
	})
	if !errors.Is(err, ErrSubdomainCooldown) {
		t.Fatalf("err = %v, want %v", err, ErrSubdomainCooldown)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestUpsertBundleRejectsMonthlySubdomainChangeCap(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	store := NewStore(db)
	mock.ExpectQuery(`(?s)SELECT bundle_name, subdomain, display_name, COALESCE\(description, ''\), is_active\s+FROM bundles.*`).
		WithArgs("weather").
		WillReturnRows(sqlmock.NewRows([]string{"bundle_name", "subdomain", "display_name", "description", "is_active"}).
			AddRow("weather", "weatherabcd", "Weather", "desc", true))
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT changed_at\s+FROM bundle_subdomain_changes.*`).
		WithArgs("weather").
		WillReturnRows(sqlmock.NewRows([]string{"changed_at"}).AddRow(time.Now().Add(-48 * time.Hour)))
	mock.ExpectQuery(`(?s)SELECT COUNT\(\*\)\s+FROM bundle_subdomain_changes.*`).
		WithArgs("weather").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(maxSubdomainChangesMonth))
	mock.ExpectRollback()

	_, err = store.UpsertBundle(context.Background(), UpsertBundleInput{
		BundleName:  "weather",
		Subdomain:   "weatherxyz1",
		DisplayName: "Weather",
		Description: "desc",
		IsActive:    true,
	})
	if !errors.Is(err, ErrSubdomainChangeCap) {
		t.Fatalf("err = %v, want %v", err, ErrSubdomainChangeCap)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestGenerateRandomSubdomainLength(t *testing.T) {
	subdomain, err := generateRandomSubdomain()
	if err != nil {
		t.Fatalf("generateRandomSubdomain() error = %v", err)
	}

	if len(subdomain) != autoSubdomainLength {
		t.Fatalf("len(subdomain) = %d, want %d", len(subdomain), autoSubdomainLength)
	}

	if err := validateSubdomain(subdomain); err != nil {
		t.Fatalf("validateSubdomain() error = %v", err)
	}
}

func TestAllocateSkillDirName(t *testing.T) {
	occupied := map[string]struct{}{
		"Current_Weather": {},
	}

	dirName := allocateSkillDirName(" Current Weather! ", occupied)
	if dirName != "Current_Weather-2" {
		t.Fatalf("allocateSkillDirName() = %q, want %q", dirName, "Current_Weather-2")
	}
}

func TestListActiveBundles(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	store := NewStore(db)
	mock.ExpectQuery(`(?s)SELECT b.bundle_name, b.subdomain, b.display_name, COALESCE\(b.description, ''\), b.is_active, COUNT\(s.tool_name\) AS skill_count.*`).
		WillReturnRows(sqlmock.NewRows([]string{"bundle_name", "subdomain", "display_name", "description", "is_active", "skill_count"}).
			AddRow("weather", "weatherhub", "Weather", "desc", true, 2))

	bundles, err := store.ListActiveBundles(context.Background())
	if err != nil {
		t.Fatalf("ListActiveBundles() error = %v", err)
	}
	if len(bundles) != 1 || bundles[0].BundleName != "weather" || bundles[0].SkillCount != 2 {
		t.Fatalf("bundles = %#v", bundles)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestGetBundleTools(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	store := NewStore(db)
	mock.ExpectQuery(`(?s)SELECT bundle_name, subdomain, display_name, COALESCE\(description, ''\), is_active\s+FROM bundles.*`).
		WithArgs("weatherhub").
		WillReturnRows(sqlmock.NewRows([]string{"bundle_name", "subdomain", "display_name", "description", "is_active"}).
			AddRow("weather", "weatherhub", "Weather", "desc", true))
	mock.ExpectQuery(`(?s)SELECT s.tool_name, s.schema_json\s+FROM bundle_skills bs.*`).
		WithArgs("weatherhub").
		WillReturnRows(sqlmock.NewRows([]string{"tool_name", "schema_json"}).
			AddRow("weather.current", `{"name":"weather.current","description":"Get weather","inputSchema":{"type":"object"}}`))

	response, err := store.GetBundleTools(context.Background(), "weatherhub")
	if err != nil {
		t.Fatalf("GetBundleTools() error = %v", err)
	}
	if response.Bundle.BundleName != "weather" || len(response.Tools) != 1 || response.Tools[0].Name != "weather.current" {
		t.Fatalf("response = %#v", response)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestListBundleResourceBindingsFiltersAllowedTools(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	store := NewStore(db)
	mock.ExpectQuery(`(?s)SELECT bundle_name, subdomain, display_name, COALESCE\(description, ''\), is_active\s+FROM bundles.*`).
		WithArgs("weatherhub").
		WillReturnRows(sqlmock.NewRows([]string{"bundle_name", "subdomain", "display_name", "description", "is_active"}).
			AddRow("weather", "weatherhub", "Weather", "desc", true))
	mock.ExpectQuery(`(?s)SELECT s.tool_name, s.skill_dir_name\s+FROM bundle_skills bs.*`).
		WithArgs("weatherhub").
		WillReturnRows(sqlmock.NewRows([]string{"tool_name", "skill_dir_name"}).
			AddRow("weather.current", "weather-current").
			AddRow("weather.forecast", "weather-forecast"))

	bindings, err := store.ListBundleResourceBindings(context.Background(), "weatherhub", map[string]struct{}{
		"weather.forecast": {},
	})
	if err != nil {
		t.Fatalf("ListBundleResourceBindings() error = %v", err)
	}
	if len(bindings) != 1 || bindings[0].ToolName != "weather.forecast" {
		t.Fatalf("bindings = %#v", bindings)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestGetBundleResourceBindingNotFound(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	store := NewStore(db)
	mock.ExpectQuery(`(?s)SELECT bundle_name, subdomain, display_name, COALESCE\(description, ''\), is_active\s+FROM bundles.*`).
		WithArgs("weatherhub").
		WillReturnRows(sqlmock.NewRows([]string{"bundle_name", "subdomain", "display_name", "description", "is_active"}).
			AddRow("weather", "weatherhub", "Weather", "desc", true))
	mock.ExpectQuery(`(?s)SELECT s.tool_name, s.skill_dir_name\s+FROM bundle_skills bs.*`).
		WithArgs("weatherhub").
		WillReturnRows(sqlmock.NewRows([]string{"tool_name", "skill_dir_name"}).
			AddRow("weather.current", "weather-current"))

	_, err = store.GetBundleResourceBinding(context.Background(), "weatherhub", "weather.forecast", nil)
	if !errors.Is(err, ErrBundleSkillNotFound) {
		t.Fatalf("err = %v, want %v", err, ErrBundleSkillNotFound)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestDeactivateBundle(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	store := NewStore(db)
	mock.ExpectExec(`(?s)UPDATE bundles\s+SET is_active = FALSE.*`).
		WithArgs("weather").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := store.DeactivateBundle(context.Background(), "weather"); err != nil {
		t.Fatalf("DeactivateBundle() error = %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestDeactivateBundleReturnsNotFound(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	store := NewStore(db)
	mock.ExpectExec(`(?s)UPDATE bundles\s+SET is_active = FALSE.*`).
		WithArgs("weather").
		WillReturnResult(sqlmock.NewResult(0, 0))

	err = store.DeactivateBundle(context.Background(), "weather")
	if !errors.Is(err, ErrBundleNotFound) {
		t.Fatalf("err = %v, want %v", err, ErrBundleNotFound)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestUpsertBundleReturnsSyncFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	store := NewStore(db, WithSkillSyncer(failingSkillSyncer{}))
	mock.ExpectQuery(`(?s)SELECT bundle_name, subdomain, display_name, COALESCE\(description, ''\), is_active\s+FROM bundles.*`).
		WithArgs("weather").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectBegin()
	mock.ExpectExec(`(?s)INSERT INTO bundles .*ON CONFLICT.*`).
		WithArgs("weather", "weatherhub", "Weather", "desc", true).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`(?s)SELECT nft_id, tool_name, COALESCE\(skill_dir_name, ''\), COALESCE\(sync_status, ''\), schema_json, COALESCE\(github_url, ''\)\s+FROM skills.*`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"nft_id", "tool_name", "skill_dir_name", "sync_status", "schema_json", "github_url"}))
	mock.ExpectQuery(`(?s)SELECT nft_id, COALESCE\(skill_dir_name, ''\)\s+FROM skills.*`).
		WillReturnRows(sqlmock.NewRows([]string{"nft_id", "skill_dir_name"}))
	mock.ExpectExec(`(?s)INSERT INTO skills .*ON CONFLICT.*`).
		WithArgs(
			int64(1001),
			"weather.current",
			sqlmock.AnyArg(),
			"https://github.com/example/weather-skill/tree/main/skills/current",
			"weather.current",
		).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`(?s)SELECT tool_name\s+FROM skills.*tool_name = ANY\(\$1\)`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"tool_name"}).AddRow("weather.current"))
	mock.ExpectExec(`(?s)DELETE FROM bundle_skills.*`).
		WithArgs("weather").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`(?s)INSERT INTO bundle_skills .*`).
		WithArgs("weather", "weather.current").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	mock.ExpectExec(`(?s)UPDATE skills\s+SET sync_status = \$2, sync_error = \$3.*`).
		WithArgs(int64(1001), "sync_failed", "sync failed").
		WillReturnResult(sqlmock.NewResult(1, 1))

	_, err = store.UpsertBundle(context.Background(), UpsertBundleInput{
		BundleName:  "weather",
		Subdomain:   "weatherhub",
		DisplayName: "Weather",
		Description: "desc",
		Skills: []ManagedSkillInput{
			{
				NFTID:       1001,
				Name:        "weather.current",
				Description: "Get current weather",
				InputSchema: []byte(`{"type":"object"}`),
				GitHubURL:   "https://github.com/example/weather-skill/tree/main/skills/current",
			},
		},
		IsActive: true,
	})
	if !errors.Is(err, ErrSkillSyncFailed) {
		t.Fatalf("err = %v, want %v", err, ErrSkillSyncFailed)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestUpsertBundleRetriesFailedSync(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	store := NewStore(db, WithSkillSyncer(&flakySkillSyncer{failuresRemaining: 1}))

	mock.ExpectQuery(`(?s)SELECT bundle_name, subdomain, display_name, COALESCE\(description, ''\), is_active\s+FROM bundles.*`).
		WithArgs("weather").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectBegin()
	mock.ExpectExec(`(?s)INSERT INTO bundles .*ON CONFLICT.*`).
		WithArgs("weather", "weatherhub", "Weather", "desc", true).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`(?s)SELECT nft_id, tool_name, COALESCE\(skill_dir_name, ''\), COALESCE\(sync_status, ''\), schema_json, COALESCE\(github_url, ''\)\s+FROM skills.*`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"nft_id", "tool_name", "skill_dir_name", "sync_status", "schema_json", "github_url"}))
	mock.ExpectQuery(`(?s)SELECT nft_id, COALESCE\(skill_dir_name, ''\)\s+FROM skills.*`).
		WillReturnRows(sqlmock.NewRows([]string{"nft_id", "skill_dir_name"}))
	mock.ExpectExec(`(?s)INSERT INTO skills .*ON CONFLICT.*`).
		WithArgs(
			int64(1001),
			"weather.current",
			sqlmock.AnyArg(),
			"https://github.com/example/weather-skill/tree/main/skills/current",
			"weather.current",
		).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`(?s)SELECT tool_name\s+FROM skills.*tool_name = ANY\(\$1\)`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"tool_name"}).AddRow("weather.current"))
	mock.ExpectExec(`(?s)DELETE FROM bundle_skills.*`).
		WithArgs("weather").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`(?s)INSERT INTO bundle_skills .*`).
		WithArgs("weather", "weather.current").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	mock.ExpectExec(`(?s)UPDATE skills\s+SET sync_status = \$2, sync_error = \$3.*`).
		WithArgs(int64(1001), "sync_failed", "sync failed").
		WillReturnResult(sqlmock.NewResult(1, 1))

	_, err = store.UpsertBundle(context.Background(), UpsertBundleInput{
		BundleName:  "weather",
		Subdomain:   "weatherhub",
		DisplayName: "Weather",
		Description: "desc",
		Skills: []ManagedSkillInput{
			{
				NFTID:       1001,
				Name:        "weather.current",
				Description: "Get current weather",
				InputSchema: []byte(`{"type":"object"}`),
				GitHubURL:   "https://github.com/example/weather-skill/tree/main/skills/current",
			},
		},
		IsActive: true,
	})
	if !errors.Is(err, ErrSkillSyncFailed) {
		t.Fatalf("first err = %v, want %v", err, ErrSkillSyncFailed)
	}

	mock.ExpectQuery(`(?s)SELECT bundle_name, subdomain, display_name, COALESCE\(description, ''\), is_active\s+FROM bundles.*`).
		WithArgs("weather").
		WillReturnRows(sqlmock.NewRows([]string{"bundle_name", "subdomain", "display_name", "description", "is_active"}).
			AddRow("weather", "weatherhub", "Weather", "desc", true))
	mock.ExpectBegin()
	mock.ExpectExec(`(?s)INSERT INTO bundles .*ON CONFLICT.*`).
		WithArgs("weather", "weatherhub", "Weather", "desc", true).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`(?s)SELECT nft_id, tool_name, COALESCE\(skill_dir_name, ''\), COALESCE\(sync_status, ''\), schema_json, COALESCE\(github_url, ''\)\s+FROM skills.*`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"nft_id", "tool_name", "skill_dir_name", "sync_status", "schema_json", "github_url"}).
			AddRow(int64(1001), "weather.current", "weather.current", "sync_failed", []byte(`{"name":"weather.current","description":"Get current weather","inputSchema":{"type":"object"}}`), "https://github.com/example/weather-skill/tree/main/skills/current"))
	mock.ExpectQuery(`(?s)SELECT nft_id, COALESCE\(skill_dir_name, ''\)\s+FROM skills.*`).
		WillReturnRows(sqlmock.NewRows([]string{"nft_id", "skill_dir_name"}).
			AddRow(int64(1001), "weather.current"))
	mock.ExpectExec(`(?s)INSERT INTO skills .*ON CONFLICT.*`).
		WithArgs(
			int64(1001),
			"weather.current",
			sqlmock.AnyArg(),
			"https://github.com/example/weather-skill/tree/main/skills/current",
			"weather.current",
		).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`(?s)SELECT tool_name\s+FROM skills.*tool_name = ANY\(\$1\)`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"tool_name"}).AddRow("weather.current"))
	mock.ExpectExec(`(?s)DELETE FROM bundle_skills.*`).
		WithArgs("weather").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`(?s)INSERT INTO bundle_skills .*`).
		WithArgs("weather", "weather.current").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	mock.ExpectExec(`(?s)UPDATE skills\s+SET sync_status = 'ready'.*`).
		WithArgs(int64(1001)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`(?s)SELECT bundle_name, subdomain, display_name, COALESCE\(description, ''\), is_active\s+FROM bundles.*`).
		WithArgs("weather").
		WillReturnRows(sqlmock.NewRows([]string{"bundle_name", "subdomain", "display_name", "description", "is_active"}).
			AddRow("weather", "weatherhub", "Weather", "desc", true))

	_, err = store.UpsertBundle(context.Background(), UpsertBundleInput{
		BundleName:  "weather",
		Subdomain:   "weatherhub",
		DisplayName: "Weather",
		Description: "desc",
		Skills: []ManagedSkillInput{
			{
				NFTID:       1001,
				Name:        "weather.current",
				Description: "Get current weather",
				InputSchema: []byte(`{"type":"object"}`),
				GitHubURL:   "https://github.com/example/weather-skill/tree/main/skills/current",
			},
		},
		IsActive: true,
	})
	if err != nil {
		t.Fatalf("second UpsertBundle() error = %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestUpsertBundleDefersReadySkillPublishUntilSyncCompletes(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	store := NewStore(db, WithSkillSyncer(successfulSkillSyncer{}))

	mock.ExpectQuery(`(?s)SELECT bundle_name, subdomain, display_name, COALESCE\(description, ''\), is_active\s+FROM bundles.*`).
		WithArgs("weather").
		WillReturnRows(sqlmock.NewRows([]string{"bundle_name", "subdomain", "display_name", "description", "is_active"}).
			AddRow("weather", "weatherhub", "Weather", "desc", true))
	mock.ExpectBegin()
	mock.ExpectExec(`(?s)INSERT INTO bundles .*ON CONFLICT.*`).
		WithArgs("weather", "weatherhub", "Weather", "desc", true).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`(?s)SELECT nft_id, tool_name, COALESCE\(skill_dir_name, ''\), COALESCE\(sync_status, ''\), schema_json, COALESCE\(github_url, ''\)\s+FROM skills.*`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"nft_id", "tool_name", "skill_dir_name", "sync_status", "schema_json", "github_url"}).
			AddRow(int64(1001), "weather.old", "weather-current", "ready", []byte(`{"name":"weather.old","description":"Old weather","inputSchema":{"type":"object"}}`), "https://github.com/example/weather-skill/tree/main/skills/old"))
	mock.ExpectQuery(`(?s)SELECT nft_id, COALESCE\(skill_dir_name, ''\)\s+FROM skills.*`).
		WillReturnRows(sqlmock.NewRows([]string{"nft_id", "skill_dir_name"}).
			AddRow(int64(1001), "weather-current"))
	mock.ExpectExec(`(?s)INSERT INTO skills .*ON CONFLICT.*`).
		WithArgs(
			int64(1001),
			"weather.current",
			sqlmock.AnyArg(),
			"https://github.com/example/weather-skill/tree/main/skills/current",
			"weather-current",
		).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`(?s)SELECT tool_name\s+FROM skills.*tool_name = ANY\(\$1\)`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"tool_name"}).AddRow("weather.old"))
	mock.ExpectExec(`(?s)DELETE FROM bundle_skills.*`).
		WithArgs("weather").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`(?s)INSERT INTO bundle_skills .*`).
		WithArgs("weather", "weather.old").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	mock.ExpectBegin()
	mock.ExpectExec(`(?s)UPDATE skills\s+SET tool_name = \$2,.*sync_status = 'ready'.*`).
		WithArgs(int64(1001), "weather.current", sqlmock.AnyArg(), "https://github.com/example/weather-skill/tree/main/skills/current").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`(?s)UPDATE bundle_skills\s+SET tool_name = \$3.*`).
		WithArgs("weather", "weather.old", "weather.current").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(`(?s)SELECT bundle_name, subdomain, display_name, COALESCE\(description, ''\), is_active\s+FROM bundles.*`).
		WithArgs("weather").
		WillReturnRows(sqlmock.NewRows([]string{"bundle_name", "subdomain", "display_name", "description", "is_active"}).
			AddRow("weather", "weatherhub", "Weather", "desc", true))

	_, err = store.UpsertBundle(context.Background(), UpsertBundleInput{
		BundleName:  "weather",
		Subdomain:   "weatherhub",
		DisplayName: "Weather",
		Description: "desc",
		Skills: []ManagedSkillInput{
			{
				NFTID:       1001,
				Name:        "weather.current",
				Description: "Get current weather",
				InputSchema: []byte(`{"type":"object"}`),
				GitHubURL:   "https://github.com/example/weather-skill/tree/main/skills/current",
			},
		},
		IsActive: true,
	})
	if err != nil {
		t.Fatalf("UpsertBundle() error = %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestUpsertBundleRestoresReadySkillAfterSyncFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	store := NewStore(db, WithSkillSyncer(failingSkillSyncer{}))
	mock.ExpectQuery(`(?s)SELECT bundle_name, subdomain, display_name, COALESCE\(description, ''\), is_active\s+FROM bundles.*`).
		WithArgs("weather").
		WillReturnRows(sqlmock.NewRows([]string{"bundle_name", "subdomain", "display_name", "description", "is_active"}).
			AddRow("weather", "weatherhub", "Weather", "desc", true))
	mock.ExpectBegin()
	mock.ExpectExec(`(?s)INSERT INTO bundles .*ON CONFLICT.*`).
		WithArgs("weather", "weatherhub", "Weather", "desc", true).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`(?s)SELECT nft_id, tool_name, COALESCE\(skill_dir_name, ''\), COALESCE\(sync_status, ''\), schema_json, COALESCE\(github_url, ''\)\s+FROM skills.*`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"nft_id", "tool_name", "skill_dir_name", "sync_status", "schema_json", "github_url"}).
			AddRow(int64(1001), "weather.old", "weather-current", "ready", []byte(`{"name":"weather.old","description":"Old weather","inputSchema":{"type":"object"}}`), "https://github.com/example/weather-skill/tree/main/skills/old"))
	mock.ExpectQuery(`(?s)SELECT nft_id, COALESCE\(skill_dir_name, ''\)\s+FROM skills.*`).
		WillReturnRows(sqlmock.NewRows([]string{"nft_id", "skill_dir_name"}).
			AddRow(int64(1001), "weather-current"))
	mock.ExpectExec(`(?s)INSERT INTO skills .*ON CONFLICT.*`).
		WithArgs(
			int64(1001),
			"weather.current",
			sqlmock.AnyArg(),
			"https://github.com/example/weather-skill/tree/main/skills/current",
			"weather-current",
		).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`(?s)SELECT tool_name\s+FROM skills.*tool_name = ANY\(\$1\)`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"tool_name"}).AddRow("weather.old"))
	mock.ExpectExec(`(?s)DELETE FROM bundle_skills.*`).
		WithArgs("weather").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`(?s)INSERT INTO bundle_skills .*`).
		WithArgs("weather", "weather.old").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	mock.ExpectBegin()
	mock.ExpectExec(`(?s)UPDATE bundle_skills\s+SET tool_name = \$3.*`).
		WithArgs("weather", "weather.current", "weather.old").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`(?s)UPDATE skills\s+SET tool_name = \$2,.*sync_status = 'ready'.*sync_error = \$5.*`).
		WithArgs(int64(1001), "weather.old", []byte(`{"name":"weather.old","description":"Old weather","inputSchema":{"type":"object"}}`), "https://github.com/example/weather-skill/tree/main/skills/old", "sync failed").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	_, err = store.UpsertBundle(context.Background(), UpsertBundleInput{
		BundleName:  "weather",
		Subdomain:   "weatherhub",
		DisplayName: "Weather",
		Description: "desc",
		Skills: []ManagedSkillInput{
			{
				NFTID:       1001,
				Name:        "weather.current",
				Description: "Get current weather",
				InputSchema: []byte(`{"type":"object"}`),
				GitHubURL:   "https://github.com/example/weather-skill/tree/main/skills/current",
			},
		},
		IsActive: true,
	})
	if !errors.Is(err, ErrSkillSyncFailed) {
		t.Fatalf("err = %v, want %v", err, ErrSkillSyncFailed)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestPublishedToolNamesPrefersPreviousReadyToolName(t *testing.T) {
	toolNames := publishedToolNames([]string{"weather.current"}, []preparedManagedSkill{
		{
			ManagedSkillInput: ManagedSkillInput{Name: "weather.current"},
			PreviousToolName:  "weather.old",
			DeferMetadataSync: true,
		},
		{
			ManagedSkillInput: ManagedSkillInput{Name: "weather.forecast"},
		},
	})

	if len(toolNames) != 2 || toolNames[0] != "weather.old" || toolNames[1] != "weather.forecast" {
		t.Fatalf("publishedToolNames() = %#v", toolNames)
	}
}

func TestMarshalSkillSchemaRejectsInvalidInput(t *testing.T) {
	_, err := marshalSkillSchema(ManagedSkillInput{
		NFTID:       1001,
		Name:        "weather.current",
		Description: "Get weather",
		InputSchema: []byte(`{"type":`),
		GitHubURL:   "https://github.com/example/weather-skill",
	})
	if !errors.Is(err, ErrInvalidSkillPayload) {
		t.Fatalf("err = %v, want %v", err, ErrInvalidSkillPayload)
	}
}

func TestUpsertBundleRejectsInvalidStoreAndInput(t *testing.T) {
	var nilStore *Store
	if _, err := nilStore.UpsertBundle(context.Background(), UpsertBundleInput{}); err == nil {
		t.Fatal("nil store UpsertBundle() error = nil")
	}

	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	if _, err := NewStore(nil).UpsertBundle(context.Background(), UpsertBundleInput{}); err == nil {
		t.Fatal("nil db UpsertBundle() error = nil")
	}
	if _, err := NewStore(db).UpsertBundle(context.Background(), UpsertBundleInput{BundleName: "weather"}); err == nil {
		t.Fatal("missing display name UpsertBundle() error = nil")
	}
}

func TestUpsertBundleRequiresSkillSyncerAndPropagatesLookupErrors(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	store := NewStore(db)
	_, err = store.UpsertBundle(context.Background(), UpsertBundleInput{
		BundleName:  "weather",
		DisplayName: "Weather",
		Skills: []ManagedSkillInput{{
			NFTID:       1001,
			Name:        "weather.current",
			Description: "Get weather",
			InputSchema: []byte(`{"type":"object"}`),
			GitHubURL:   "https://github.com/example/weather-skill/tree/main/skills/current",
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "skill syncer is nil") {
		t.Fatalf("UpsertBundle() error = %v", err)
	}

	mock.ExpectQuery(regexp.QuoteMeta(getBundleQuery)).
		WithArgs("weather").
		WillReturnError(sql.ErrConnDone)
	_, err = store.UpsertBundle(context.Background(), UpsertBundleInput{
		BundleName:  "weather",
		DisplayName: "Weather",
	})
	if err == nil || !strings.Contains(err.Error(), "get active bundle") {
		t.Fatalf("UpsertBundle() error = %v", err)
	}
}

func TestUpsertBundleMapsUniqueViolationAndCommitFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	store := NewStore(db)
	mock.ExpectQuery(regexp.QuoteMeta(getBundleQuery)).
		WithArgs("weather").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(upsertBundleQuery)).
		WithArgs("weather", "weatherhub", "Weather", nil, true).
		WillReturnError(&pq.Error{Code: "23505"})
	mock.ExpectRollback()
	_, err = store.UpsertBundle(context.Background(), UpsertBundleInput{
		BundleName:  "weather",
		Subdomain:   "weatherhub",
		DisplayName: "Weather",
		IsActive:    true,
	})
	if !errors.Is(err, ErrSubdomainTaken) {
		t.Fatalf("UpsertBundle() error = %v, want %v", err, ErrSubdomainTaken)
	}

	mock.ExpectQuery(regexp.QuoteMeta(getBundleQuery)).
		WithArgs("weather").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectBegin().WillReturnError(sql.ErrConnDone)
	_, err = store.UpsertBundle(context.Background(), UpsertBundleInput{
		BundleName:  "weather",
		DisplayName: "Weather",
	})
	if err == nil || !strings.Contains(err.Error(), "begin bundle transaction") {
		t.Fatalf("UpsertBundle() begin error = %v", err)
	}

	mock.ExpectQuery(regexp.QuoteMeta(getBundleQuery)).
		WithArgs("weather").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(upsertBundleQuery)).
		WithArgs("weather", "weatherhub", "Weather", nil, true).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta(deleteBundleSkillsQuery)).
		WithArgs("weather").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit().WillReturnError(sql.ErrConnDone)
	mock.ExpectRollback()
	_, err = store.UpsertBundle(context.Background(), UpsertBundleInput{
		BundleName:  "weather",
		Subdomain:   "weatherhub",
		DisplayName: "Weather",
		IsActive:    true,
	})
	if err == nil || !strings.Contains(err.Error(), "commit bundle transaction") {
		t.Fatalf("UpsertBundle() commit error = %v", err)
	}
}

func TestReplaceBundleSkillsPropagatesInsertFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	mock.ExpectQuery(regexp.QuoteMeta(activeSkillsByNamesQuery)).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"tool_name"}).AddRow("weather.current"))
	mock.ExpectExec(regexp.QuoteMeta(deleteBundleSkillsQuery)).
		WithArgs("weather").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(insertBundleSkillQuery)).
		WithArgs("weather", "weather.current").
		WillReturnError(sql.ErrConnDone)
	if err := replaceBundleSkills(context.Background(), tx, "weather", []string{"weather.current"}); err == nil || !strings.Contains(err.Error(), "insert bundle skill") {
		t.Fatalf("replaceBundleSkills() error = %v", err)
	}
}

func TestNewStoreWithSkillSyncerOption(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	syncer := successfulSkillSyncer{}
	store := NewStore(db, WithSkillSyncer(syncer))
	if store.skillSyncer != syncer {
		t.Fatalf("skillSyncer = %#v, want %#v", store.skillSyncer, syncer)
	}
}

func TestLockManagedSkillsDeduplicatesNFTIDs(t *testing.T) {
	store := NewStore(nil)
	unlock := store.lockManagedSkills([]ManagedSkillInput{
		{NFTID: 1002},
		{NFTID: 1001},
		{NFTID: 1002},
		{NFTID: 0},
	})
	defer unlock()

	if len(store.skillLocks) != 2 {
		t.Fatalf("len(skillLocks) = %d, want 2", len(store.skillLocks))
	}
}

func TestListActiveBundlesPropagatesQueryError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	store := NewStore(db)
	mock.ExpectQuery(regexp.QuoteMeta(listActiveBundlesQuery)).WillReturnError(sql.ErrConnDone)

	_, err = store.ListActiveBundles(context.Background())
	if err == nil || !strings.Contains(err.Error(), "list active bundles") {
		t.Fatalf("ListActiveBundles() error = %v", err)
	}
}

func TestGetBundleToolsRejectsInvalidSchema(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	store := NewStore(db)
	mock.ExpectQuery(regexp.QuoteMeta(getActiveBundleQuery)).
		WithArgs("weatherhub").
		WillReturnRows(sqlmock.NewRows([]string{"bundle_name", "subdomain", "display_name", "description", "is_active"}).
			AddRow("weather", "weatherhub", "Weather", "desc", true))
	mock.ExpectQuery(regexp.QuoteMeta(listBundleToolsQuery)).
		WithArgs("weatherhub").
		WillReturnRows(sqlmock.NewRows([]string{"tool_name", "schema_json"}).
			AddRow("weather.current", `{"name":`))

	_, err = store.GetBundleTools(context.Background(), "weatherhub")
	if err == nil || !strings.Contains(err.Error(), "unmarshal bundle tool schema") {
		t.Fatalf("GetBundleTools() error = %v", err)
	}
}

func TestGetBundleResourceBindingReturnsMatch(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	store := NewStore(db)
	mock.ExpectQuery(regexp.QuoteMeta(getActiveBundleQuery)).
		WithArgs("weatherhub").
		WillReturnRows(sqlmock.NewRows([]string{"bundle_name", "subdomain", "display_name", "description", "is_active"}).
			AddRow("weather", "weatherhub", "Weather", "desc", true))
	mock.ExpectQuery(regexp.QuoteMeta(listBundleResourceBindingsQuery)).
		WithArgs("weatherhub").
		WillReturnRows(sqlmock.NewRows([]string{"tool_name", "skill_dir_name"}).
			AddRow("weather.current", "weather-current"))

	binding, err := store.GetBundleResourceBinding(context.Background(), "weatherhub", "weather.current", nil)
	if err != nil {
		t.Fatalf("GetBundleResourceBinding() error = %v", err)
	}
	if binding.SkillDirName != "weather-current" {
		t.Fatalf("binding = %#v", binding)
	}
}

func TestDeactivateBundlePropagatesRowsAffectedError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	store := NewStore(db)
	mock.ExpectExec(regexp.QuoteMeta(deactivateBundleQuery)).
		WithArgs("weather").
		WillReturnResult(sqlmock.NewErrorResult(sql.ErrConnDone))

	err = store.DeactivateBundle(context.Background(), "weather")
	if err == nil || !strings.Contains(err.Error(), "read deactivate bundle result") {
		t.Fatalf("DeactivateBundle() error = %v", err)
	}
}

func TestPromoteReadySkillUpdatesBundleToolName(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	store := NewStore(db)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(promoteReadySkillQuery)).
		WithArgs(int64(1001), "weather.current", []byte(`{"name":"weather.current"}`), "https://github.com/example/weather-skill").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta(restoreBundleSkillToolNameQuery)).
		WithArgs("weather", "weather.old", "weather.current").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	err = store.promoteReadySkill(context.Background(), preparedManagedSkill{
		ManagedSkillInput: ManagedSkillInput{
			NFTID:     1001,
			Name:      "weather.current",
			GitHubURL: "https://github.com/example/weather-skill",
		},
		BundleName:       "weather",
		SchemaJSON:       []byte(`{"name":"weather.current"}`),
		PreviousToolName: "weather.old",
	})
	if err != nil {
		t.Fatalf("promoteReadySkill() error = %v", err)
	}
}

func TestSyncPreparedSkillsPropagatesPromotionAndMarkErrors(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	store := NewStore(db, WithSkillSyncer(successfulSkillSyncer{}))
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(promoteReadySkillQuery)).
		WithArgs(int64(1001), "weather.current", []byte(`{"name":"weather.current"}`), "https://github.com/example/weather-skill").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	mock.ExpectExec(regexp.QuoteMeta(markSkillSyncReadyQuery)).
		WithArgs(int64(1002)).
		WillReturnError(sql.ErrConnDone)

	err = store.syncPreparedSkills(context.Background(), []preparedManagedSkill{
		{
			ManagedSkillInput: ManagedSkillInput{NFTID: 1001, Name: "weather.current", GitHubURL: "https://github.com/example/weather-skill"},
			SchemaJSON:        []byte(`{"name":"weather.current"}`),
			DeferMetadataSync: true,
		},
		{
			ManagedSkillInput: ManagedSkillInput{NFTID: 1002, Name: "weather.forecast", GitHubURL: "https://github.com/example/weather-skill"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "mark skill sync ready") {
		t.Fatalf("syncPreparedSkills() error = %v", err)
	}
}

func TestPromoteAndRestoreReadySkillErrorPaths(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	store := NewStore(db)
	mock.ExpectBegin().WillReturnError(sql.ErrConnDone)
	if err := store.promoteReadySkill(context.Background(), preparedManagedSkill{}); err == nil || !strings.Contains(err.Error(), "begin promote skill transaction") {
		t.Fatalf("promoteReadySkill() begin error = %v", err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(promoteReadySkillQuery)).
		WithArgs(int64(1001), "weather.current", []byte(`{"name":"weather.current"}`), "https://github.com/example/weather-skill").
		WillReturnError(sql.ErrConnDone)
	mock.ExpectRollback()
	if err := store.promoteReadySkill(context.Background(), preparedManagedSkill{
		ManagedSkillInput: ManagedSkillInput{NFTID: 1001, Name: "weather.current", GitHubURL: "https://github.com/example/weather-skill"},
		SchemaJSON:        []byte(`{"name":"weather.current"}`),
	}); err == nil || !strings.Contains(err.Error(), "promote ready skill metadata") {
		t.Fatalf("promoteReadySkill() exec error = %v", err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(promoteReadySkillQuery)).
		WithArgs(int64(1001), "weather.current", []byte(`{"name":"weather.current"}`), "https://github.com/example/weather-skill").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit().WillReturnError(sql.ErrConnDone)
	if err := store.promoteReadySkill(context.Background(), preparedManagedSkill{
		ManagedSkillInput: ManagedSkillInput{NFTID: 1001, Name: "weather.current", GitHubURL: "https://github.com/example/weather-skill"},
		SchemaJSON:        []byte(`{"name":"weather.current"}`),
	}); err == nil || !strings.Contains(err.Error(), "commit promote skill transaction") {
		t.Fatalf("promoteReadySkill() commit error = %v", err)
	}

	mock.ExpectBegin().WillReturnError(sql.ErrConnDone)
	if err := store.restoreReadySkill(context.Background(), preparedManagedSkill{}, "boom"); err == nil || !strings.Contains(err.Error(), "begin restore skill transaction") {
		t.Fatalf("restoreReadySkill() begin error = %v", err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(restoreBundleSkillToolNameQuery)).
		WithArgs("weather", "weather.current", "weather.old").
		WillReturnError(sql.ErrConnDone)
	mock.ExpectRollback()
	if err := store.restoreReadySkill(context.Background(), preparedManagedSkill{
		ManagedSkillInput: ManagedSkillInput{NFTID: 1001, Name: "weather.current"},
		BundleName:        "weather",
		PreviousToolName:  "weather.old",
	}, "boom"); err == nil || !strings.Contains(err.Error(), "restore bundle skill tool name") {
		t.Fatalf("restoreReadySkill() bundle error = %v", err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(restoreReadySkillQuery)).
		WithArgs(int64(1001), "", sqlmock.AnyArg(), nil, "boom").
		WillReturnError(sql.ErrConnDone)
	mock.ExpectRollback()
	if err := store.restoreReadySkill(context.Background(), preparedManagedSkill{
		ManagedSkillInput: ManagedSkillInput{NFTID: 1001},
	}, "boom"); err == nil || !strings.Contains(err.Error(), "restore ready skill metadata") {
		t.Fatalf("restoreReadySkill() metadata error = %v", err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(restoreReadySkillQuery)).
		WithArgs(int64(1001), "", sqlmock.AnyArg(), nil, "boom").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit().WillReturnError(sql.ErrConnDone)
	if err := store.restoreReadySkill(context.Background(), preparedManagedSkill{
		ManagedSkillInput: ManagedSkillInput{NFTID: 1001},
	}, "boom"); err == nil || !strings.Contains(err.Error(), "commit restore skill transaction") {
		t.Fatalf("restoreReadySkill() commit error = %v", err)
	}
}

func TestSyncPreparedSkillsRestoreAndFailureMarkErrors(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	store := NewStore(db, WithSkillSyncer(failingSkillSyncer{}))
	mock.ExpectBegin().WillReturnError(sql.ErrConnDone)
	err = store.syncPreparedSkills(context.Background(), []preparedManagedSkill{{
		ManagedSkillInput:  ManagedSkillInput{NFTID: 1001, Name: "weather.current", GitHubURL: "https://github.com/example/weather-skill"},
		PreviousSyncStatus: "ready",
	}})
	if err == nil || !strings.Contains(err.Error(), "restore ready skill") {
		t.Fatalf("syncPreparedSkills() restore error = %v", err)
	}

	mock.ExpectExec(regexp.QuoteMeta(markSkillSyncFailedQuery)).
		WithArgs(int64(1002), "sync_failed", "sync failed").
		WillReturnError(sql.ErrConnDone)
	err = store.syncPreparedSkills(context.Background(), []preparedManagedSkill{{
		ManagedSkillInput: ManagedSkillInput{NFTID: 1002, Name: "weather.forecast", GitHubURL: "https://github.com/example/weather-skill"},
	}})
	if err == nil || !strings.Contains(err.Error(), "mark skill sync failed") {
		t.Fatalf("syncPreparedSkills() mark failed error = %v", err)
	}
}

func TestMarkSkillSyncHelpersPropagateExecErrors(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	store := NewStore(db)
	mock.ExpectExec(regexp.QuoteMeta(markSkillSyncReadyQuery)).
		WithArgs(int64(1001)).
		WillReturnError(sql.ErrConnDone)
	if err := store.markSkillSyncReady(context.Background(), 1001); err == nil {
		t.Fatal("markSkillSyncReady() error = nil, want error")
	}

	mock.ExpectExec(regexp.QuoteMeta(markSkillSyncFailedQuery)).
		WithArgs(int64(1001), "sync_failed", "boom").
		WillReturnError(sql.ErrConnDone)
	if err := store.markSkillSyncFailed(context.Background(), 1001, "sync_failed", "boom"); err == nil {
		t.Fatal("markSkillSyncFailed() error = nil, want error")
	}
}

func TestGetBundleByQueryErrors(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	store := NewStore(db)
	mock.ExpectQuery(regexp.QuoteMeta(getBundleQuery)).
		WithArgs("weather").
		WillReturnError(sql.ErrConnDone)
	_, err = store.getBundle(context.Background(), "weather")
	if err == nil || !strings.Contains(err.Error(), "get active bundle") {
		t.Fatalf("getBundle() error = %v", err)
	}
}

func TestReplaceBundleSkillsBranches(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}

	mock.ExpectExec(regexp.QuoteMeta(deleteBundleSkillsQuery)).
		WithArgs("weather").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	if err := replaceBundleSkills(context.Background(), tx, "weather", nil); err != nil {
		t.Fatalf("replaceBundleSkills() clear error = %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	mock.ExpectBegin()
	tx, err = db.Begin()
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	mock.ExpectQuery(regexp.QuoteMeta(activeSkillsByNamesQuery)).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"tool_name"}).AddRow("weather.current"))
	mock.ExpectRollback()
	if err := replaceBundleSkills(context.Background(), tx, "weather", []string{"weather.current", "weather.forecast"}); !errors.Is(err, ErrUnknownSkill) {
		t.Fatalf("replaceBundleSkills() error = %v, want %v", err, ErrUnknownSkill)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	mock.ExpectBegin()
	tx, err = db.Begin()
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	mock.ExpectQuery(regexp.QuoteMeta(activeSkillsByNamesQuery)).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"tool_name"}).AddRow("weather.current"))
	mock.ExpectExec(regexp.QuoteMeta(deleteBundleSkillsQuery)).
		WithArgs("weather").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(insertBundleSkillQuery)).
		WithArgs("weather", "weather.current").
		WillReturnError(&pq.Error{Code: "23505"})
	mock.ExpectRollback()
	if err := replaceBundleSkills(context.Background(), tx, "weather", []string{"weather.current"}); !errors.Is(err, ErrSkillAlreadyBundled) {
		t.Fatalf("replaceBundleSkills() duplicate error = %v", err)
	}
}

func TestPrepareManagedSkillsAndLoadHelpers(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	mock.ExpectQuery(regexp.QuoteMeta(existingSkillsByNFTIDsQuery)).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"nft_id", "tool_name", "skill_dir_name", "sync_status", "schema_json", "github_url"}).
			AddRow(int64(1001), "weather.current", "weather-current", "ready", []byte(`{"name":"weather.current"}`), "https://github.com/example/weather-skill"))
	mock.ExpectQuery(regexp.QuoteMeta(listSkillDirectoryNamesQuery)).
		WillReturnRows(sqlmock.NewRows([]string{"nft_id", "skill_dir_name"}).AddRow(int64(1001), "weather-current"))
	mock.ExpectRollback()

	preparedSkills, err := prepareManagedSkills(context.Background(), tx, "weather", []ManagedSkillInput{
		{
			NFTID:       1001,
			Name:        "weather.current",
			Description: "Get weather",
			InputSchema: []byte(`{"type":"object"}`),
			GitHubURL:   "https://github.com/example/weather-skill/tree/main/skills/current",
		},
	})
	if err != nil {
		t.Fatalf("prepareManagedSkills() error = %v", err)
	}
	if len(preparedSkills) != 1 || !preparedSkills[0].DeferMetadataSync || preparedSkills[0].SkillDirName != "weather-current" {
		t.Fatalf("preparedSkills = %#v", preparedSkills)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
}

func TestPrepareManagedSkillsRejectsInvalidGitHubURL(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	mock.ExpectQuery(regexp.QuoteMeta(existingSkillsByNFTIDsQuery)).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"nft_id", "tool_name", "skill_dir_name", "sync_status", "schema_json", "github_url"}))
	mock.ExpectQuery(regexp.QuoteMeta(listSkillDirectoryNamesQuery)).
		WillReturnRows(sqlmock.NewRows([]string{"nft_id", "skill_dir_name"}))

	_, err = prepareManagedSkills(context.Background(), tx, "weather", []ManagedSkillInput{
		{
			NFTID:       1001,
			Name:        "weather.current",
			Description: "Get weather",
			InputSchema: []byte(`{"type":"object"}`),
			GitHubURL:   "https://example.com/weather-skill",
		},
	})
	if !errors.Is(err, ErrInvalidSkillPayload) {
		t.Fatalf("prepareManagedSkills() error = %v, want %v", err, ErrInvalidSkillPayload)
	}
}

func TestPrepareManagedSkillsEmptyAndUpsertManagedSkillsBranches(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	prepared, err := prepareManagedSkills(context.Background(), tx, "weather", nil)
	if err != nil || prepared != nil {
		t.Fatalf("prepareManagedSkills() = (%#v, %v)", prepared, err)
	}
	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	mock.ExpectBegin()
	tx, err = db.Begin()
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	mock.ExpectExec(regexp.QuoteMeta(upsertSkillQuery)).
		WithArgs(int64(1001), "weather.current", []byte(`{"name":"weather.current"}`), "https://github.com/example/weather-skill", "weather-current").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta(preserveReadySkillQuery)).
		WithArgs(int64(1002), "weather.forecast", []byte(`{"name":"weather.forecast"}`), "https://github.com/example/weather-skill", "weather-forecast").
		WillReturnResult(sqlmock.NewResult(1, 1))
	if err := upsertManagedSkills(context.Background(), tx, []preparedManagedSkill{
		{
			ManagedSkillInput: ManagedSkillInput{NFTID: 1001, Name: "weather.current", GitHubURL: "https://github.com/example/weather-skill"},
			SkillDirName:      "weather-current",
			SchemaJSON:        []byte(`{"name":"weather.current"}`),
		},
		{
			ManagedSkillInput: ManagedSkillInput{NFTID: 1002, Name: "weather.forecast", GitHubURL: "https://github.com/example/weather-skill"},
			SkillDirName:      "weather-forecast",
			SchemaJSON:        []byte(`{"name":"weather.forecast"}`),
			DeferMetadataSync: true,
		},
	}); err != nil {
		t.Fatalf("upsertManagedSkills() error = %v", err)
	}
	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	mock.ExpectBegin()
	tx, err = db.Begin()
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	mock.ExpectExec(regexp.QuoteMeta(upsertSkillQuery)).
		WithArgs(int64(1001), "weather.current", []byte(`{"name":"weather.current"}`), "https://github.com/example/weather-skill", "weather-current").
		WillReturnError(sql.ErrConnDone)
	if err := upsertManagedSkills(context.Background(), tx, []preparedManagedSkill{{
		ManagedSkillInput: ManagedSkillInput{NFTID: 1001, Name: "weather.current", GitHubURL: "https://github.com/example/weather-skill"},
		SkillDirName:      "weather-current",
		SchemaJSON:        []byte(`{"name":"weather.current"}`),
	}}); err == nil || !strings.Contains(err.Error(), "upsert managed skill") {
		t.Fatalf("upsertManagedSkills() error = %v", err)
	}
}

func TestLoadExistingSkillsAndOccupiedDirNamesErrors(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	mock.ExpectQuery(regexp.QuoteMeta(existingSkillsByNFTIDsQuery)).
		WithArgs(sqlmock.AnyArg()).
		WillReturnError(sql.ErrConnDone)
	if _, err := loadExistingSkillsByNFTID(context.Background(), tx, []ManagedSkillInput{{NFTID: 1001}}); err == nil || !strings.Contains(err.Error(), "load existing skills") {
		t.Fatalf("loadExistingSkillsByNFTID() query error = %v", err)
	}

	mock.ExpectBegin()
	tx, err = db.Begin()
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	mock.ExpectQuery(regexp.QuoteMeta(existingSkillsByNFTIDsQuery)).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"nft_id", "tool_name", "skill_dir_name", "sync_status", "schema_json", "github_url"}).
			AddRow("bad", "weather.current", "weather-current", "ready", []byte(`{"name":"weather.current"}`), "https://github.com/example/weather-skill"))
	if _, err := loadExistingSkillsByNFTID(context.Background(), tx, []ManagedSkillInput{{NFTID: 1001}}); err == nil || !strings.Contains(err.Error(), "scan existing skill") {
		t.Fatalf("loadExistingSkillsByNFTID() scan error = %v", err)
	}

	mock.ExpectBegin()
	tx, err = db.Begin()
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	rows := sqlmock.NewRows([]string{"nft_id", "tool_name", "skill_dir_name", "sync_status", "schema_json", "github_url"}).
		AddRow(int64(1001), "weather.current", "weather-current", "ready", []byte(`{"name":"weather.current"}`), "https://github.com/example/weather-skill").
		RowError(0, sql.ErrConnDone)
	mock.ExpectQuery(regexp.QuoteMeta(existingSkillsByNFTIDsQuery)).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(rows)
	if _, err := loadExistingSkillsByNFTID(context.Background(), tx, []ManagedSkillInput{{NFTID: 1001}}); err == nil || !strings.Contains(err.Error(), "iterate existing skills") {
		t.Fatalf("loadExistingSkillsByNFTID() row error = %v", err)
	}

	mock.ExpectBegin()
	tx, err = db.Begin()
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	mock.ExpectQuery(regexp.QuoteMeta(listSkillDirectoryNamesQuery)).
		WillReturnError(sql.ErrConnDone)
	if _, err := loadOccupiedSkillDirNames(context.Background(), tx); err == nil || !strings.Contains(err.Error(), "load skill directory names") {
		t.Fatalf("loadOccupiedSkillDirNames() query error = %v", err)
	}

	mock.ExpectBegin()
	tx, err = db.Begin()
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	mock.ExpectQuery(regexp.QuoteMeta(listSkillDirectoryNamesQuery)).
		WillReturnRows(sqlmock.NewRows([]string{"nft_id", "skill_dir_name"}).AddRow("bad", "weather-current"))
	if _, err := loadOccupiedSkillDirNames(context.Background(), tx); err == nil || !strings.Contains(err.Error(), "scan skill directory name") {
		t.Fatalf("loadOccupiedSkillDirNames() scan error = %v", err)
	}

	mock.ExpectBegin()
	tx, err = db.Begin()
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	dirRows := sqlmock.NewRows([]string{"nft_id", "skill_dir_name"}).
		AddRow(int64(1001), "weather-current").
		RowError(0, sql.ErrConnDone)
	mock.ExpectQuery(regexp.QuoteMeta(listSkillDirectoryNamesQuery)).
		WillReturnRows(dirRows)
	if _, err := loadOccupiedSkillDirNames(context.Background(), tx); err == nil || !strings.Contains(err.Error(), "iterate skill directory names") {
		t.Fatalf("loadOccupiedSkillDirNames() row error = %v", err)
	}
}

func TestNormalizeBundleHelpers(t *testing.T) {
	toolNames, managedSkills, err := normalizeBundleSkillInputs([]string{" a ", "a", ""}, nil)
	if err != nil {
		t.Fatalf("normalizeBundleSkillInputs() error = %v", err)
	}
	if strings.Join(toolNames, ",") != "a" || managedSkills != nil {
		t.Fatalf("normalizeBundleSkillInputs() = %#v, %#v", toolNames, managedSkills)
	}

	_, _, err = normalizeBundleSkillInputs(nil, []ManagedSkillInput{{Name: "weather.current"}})
	if !errors.Is(err, ErrInvalidSkillPayload) {
		t.Fatalf("normalizeBundleSkillInputs() error = %v", err)
	}

	if normalizeSkillDirBase("  !!!  ") != "skill" || normalizeSkillDirBase("Current Weather!") != "Current_Weather" {
		t.Fatalf("normalizeSkillDirBase() returned unexpected value")
	}
	if err := validateSubdomain("weatherhub"); err != nil {
		t.Fatalf("validateSubdomain() error = %v", err)
	}
	if err := validateSubdomain("bad"); !errors.Is(err, ErrInvalidSubdomain) {
		t.Fatalf("validateSubdomain() error = %v", err)
	}
	if normalizeSubdomain(" WeatherHub ") != "weatherhub" {
		t.Fatalf("normalizeSubdomain() returned unexpected value")
	}
	if strings.Join(normalizeToolNames([]string{" a ", "a", "", "b"}), ",") != "a,b" {
		t.Fatalf("normalizeToolNames() returned unexpected value")
	}
	if nullableDescription("") != nil || nullableDescription("desc") != "desc" {
		t.Fatalf("nullableDescription() returned unexpected value")
	}
	if nullableString(" ") != nil || nullableString("value") != "value" {
		t.Fatalf("nullableString() returned unexpected value")
	}
	if !isToolAllowed("weather.current", nil) || isToolAllowed("weather.current", map[string]struct{}{"weather.forecast": {}}) {
		t.Fatalf("isToolAllowed() returned unexpected value")
	}
}

func TestResolveSubdomainAndChangeRules(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	mock.ExpectRollback()
	subdomain, err := resolveSubdomain(context.Background(), tx, Bundle{}, "weatherhub")
	if err != nil || subdomain != "weatherhub" {
		t.Fatalf("resolveSubdomain() = (%q, %v)", subdomain, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	mock.ExpectBegin()
	tx, err = db.Begin()
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	mock.ExpectRollback()
	subdomain, err = resolveSubdomain(context.Background(), tx, Bundle{BundleName: "weather", Subdomain: "weatherhub"}, "")
	if err != nil || subdomain != "weatherhub" {
		t.Fatalf("resolveSubdomain() existing = (%q, %v)", subdomain, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	mock.ExpectBegin()
	tx, err = db.Begin()
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	mock.ExpectQuery(regexp.QuoteMeta(subdomainExistsQuery)).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta(subdomainExistsQuery)).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectRollback()
	subdomain, err = resolveSubdomain(context.Background(), tx, Bundle{}, "")
	if err != nil || len(subdomain) != autoSubdomainLength {
		t.Fatalf("resolveSubdomain() generated = (%q, %v)", subdomain, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
}

func TestResolveSubdomainAndChangeRuleErrorPaths(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	if _, err := resolveSubdomain(context.Background(), tx, Bundle{}, "BAD"); !errors.Is(err, ErrInvalidSubdomain) {
		t.Fatalf("resolveSubdomain() error = %v", err)
	}
	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	mock.ExpectBegin()
	tx, err = db.Begin()
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	mock.ExpectQuery(regexp.QuoteMeta(subdomainExistsQuery)).
		WithArgs(sqlmock.AnyArg()).
		WillReturnError(sql.ErrConnDone)
	if _, err := generateUniqueSubdomain(context.Background(), tx); err == nil || !strings.Contains(err.Error(), "check subdomain uniqueness") {
		t.Fatalf("generateUniqueSubdomain() error = %v", err)
	}

	mock.ExpectBegin()
	tx, err = db.Begin()
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	mock.ExpectQuery(regexp.QuoteMeta(latestSubdomainChangeQuery)).
		WithArgs("weather").
		WillReturnError(sql.ErrConnDone)
	if err := ensureSubdomainChangeAllowed(context.Background(), tx, "weather"); err == nil || !strings.Contains(err.Error(), "load latest subdomain change") {
		t.Fatalf("ensureSubdomainChangeAllowed() latest error = %v", err)
	}

	mock.ExpectBegin()
	tx, err = db.Begin()
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	mock.ExpectQuery(regexp.QuoteMeta(latestSubdomainChangeQuery)).
		WithArgs("weather").
		WillReturnRows(sqlmock.NewRows([]string{"changed_at"}))
	mock.ExpectQuery(regexp.QuoteMeta(monthlySubdomainChangeCountQuery)).
		WithArgs("weather").
		WillReturnError(sql.ErrConnDone)
	if err := ensureSubdomainChangeAllowed(context.Background(), tx, "weather"); err == nil || !strings.Contains(err.Error(), "count monthly subdomain changes") {
		t.Fatalf("ensureSubdomainChangeAllowed() monthly error = %v", err)
	}
}

func TestBundleStoreAdditionalCoveragePaths(t *testing.T) {
	t.Run("published tool names dedupe", func(t *testing.T) {
		toolNames := publishedToolNames(nil, []preparedManagedSkill{
			{
				ManagedSkillInput: ManagedSkillInput{Name: "weather.current"},
				PreviousToolName:  "weather.old",
				DeferMetadataSync: true,
			},
			{
				ManagedSkillInput: ManagedSkillInput{Name: "weather.other"},
				PreviousToolName:  "weather.old",
				DeferMetadataSync: true,
			},
		})
		if strings.Join(toolNames, ",") != "weather.old" {
			t.Fatalf("publishedToolNames() = %#v", toolNames)
		}
	})

	t.Run("list active bundles scan and rows errors", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New() error = %v", err)
		}
		defer db.Close()

		store := NewStore(db)
		mock.ExpectQuery(regexp.QuoteMeta(listActiveBundlesQuery)).
			WillReturnRows(sqlmock.NewRows([]string{"bundle_name", "subdomain", "display_name", "description", "is_active", "skill_count"}).
				AddRow(nil, "wx", "Weather", "desc", true, 1))
		if _, err := store.ListActiveBundles(context.Background()); err == nil || !strings.Contains(err.Error(), "scan active bundle") {
			t.Fatalf("ListActiveBundles() scan error = %v", err)
		}

		rows := sqlmock.NewRows([]string{"bundle_name", "subdomain", "display_name", "description", "is_active", "skill_count"}).
			AddRow("weather", "wx", "Weather", "desc", true, 1).
			RowError(0, sql.ErrConnDone)
		mock.ExpectQuery(regexp.QuoteMeta(listActiveBundlesQuery)).WillReturnRows(rows)
		if _, err := store.ListActiveBundles(context.Background()); err == nil || !strings.Contains(err.Error(), "iterate active bundles") {
			t.Fatalf("ListActiveBundles() row error = %v", err)
		}
	})

	t.Run("get bundle tools error branches", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New() error = %v", err)
		}
		defer db.Close()

		store := NewStore(db)
		mock.ExpectQuery(regexp.QuoteMeta(getActiveBundleQuery)).
			WithArgs("weatherhub").
			WillReturnError(sql.ErrConnDone)
		if _, err := store.GetBundleTools(context.Background(), "weatherhub"); err == nil || !strings.Contains(err.Error(), "get active bundle") {
			t.Fatalf("GetBundleTools() get bundle error = %v", err)
		}

		mock.ExpectQuery(regexp.QuoteMeta(getActiveBundleQuery)).
			WithArgs("weatherhub").
			WillReturnRows(sqlmock.NewRows([]string{"bundle_name", "subdomain", "display_name", "description", "is_active"}).
				AddRow("weather", "weatherhub", "Weather", "desc", true))
		mock.ExpectQuery(regexp.QuoteMeta(listBundleToolsQuery)).
			WithArgs("weatherhub").
			WillReturnError(sql.ErrConnDone)
		if _, err := store.GetBundleTools(context.Background(), "weatherhub"); err == nil || !strings.Contains(err.Error(), "list bundle tools") {
			t.Fatalf("GetBundleTools() query error = %v", err)
		}

		mock.ExpectQuery(regexp.QuoteMeta(getActiveBundleQuery)).
			WithArgs("weatherhub").
			WillReturnRows(sqlmock.NewRows([]string{"bundle_name", "subdomain", "display_name", "description", "is_active"}).
				AddRow("weather", "weatherhub", "Weather", "desc", true))
		mock.ExpectQuery(regexp.QuoteMeta(listBundleToolsQuery)).
			WithArgs("weatherhub").
			WillReturnRows(sqlmock.NewRows([]string{"tool_name", "schema_json"}).
				AddRow(nil, []byte(`{"name":"weather.current"}`)))
		if _, err := store.GetBundleTools(context.Background(), "weatherhub"); err == nil || !strings.Contains(err.Error(), "scan bundle tool") {
			t.Fatalf("GetBundleTools() scan error = %v", err)
		}

		toolRows := sqlmock.NewRows([]string{"tool_name", "schema_json"}).
			AddRow("weather.current", []byte(`{"name":"weather.current","description":"Get weather","inputSchema":{"type":"object"}}`)).
			RowError(0, sql.ErrConnDone)
		mock.ExpectQuery(regexp.QuoteMeta(getActiveBundleQuery)).
			WithArgs("weatherhub").
			WillReturnRows(sqlmock.NewRows([]string{"bundle_name", "subdomain", "display_name", "description", "is_active"}).
				AddRow("weather", "weatherhub", "Weather", "desc", true))
		mock.ExpectQuery(regexp.QuoteMeta(listBundleToolsQuery)).
			WithArgs("weatherhub").
			WillReturnRows(toolRows)
		if _, err := store.GetBundleTools(context.Background(), "weatherhub"); err == nil || !strings.Contains(err.Error(), "iterate bundle tools") {
			t.Fatalf("GetBundleTools() row error = %v", err)
		}
	})

	t.Run("resource binding error branches", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New() error = %v", err)
		}
		defer db.Close()

		store := NewStore(db)
		mock.ExpectQuery(regexp.QuoteMeta(getActiveBundleQuery)).
			WithArgs("weatherhub").
			WillReturnError(sql.ErrConnDone)
		if _, err := store.ListBundleResourceBindings(context.Background(), "weatherhub", nil); err == nil || !strings.Contains(err.Error(), "get active bundle") {
			t.Fatalf("ListBundleResourceBindings() get bundle error = %v", err)
		}

		mock.ExpectQuery(regexp.QuoteMeta(getActiveBundleQuery)).
			WithArgs("weatherhub").
			WillReturnRows(sqlmock.NewRows([]string{"bundle_name", "subdomain", "display_name", "description", "is_active"}).
				AddRow("weather", "weatherhub", "Weather", "desc", true))
		mock.ExpectQuery(regexp.QuoteMeta(listBundleResourceBindingsQuery)).
			WithArgs("weatherhub").
			WillReturnError(sql.ErrConnDone)
		if _, err := store.ListBundleResourceBindings(context.Background(), "weatherhub", nil); err == nil || !strings.Contains(err.Error(), "list bundle resource bindings") {
			t.Fatalf("ListBundleResourceBindings() query error = %v", err)
		}

		mock.ExpectQuery(regexp.QuoteMeta(getActiveBundleQuery)).
			WithArgs("weatherhub").
			WillReturnRows(sqlmock.NewRows([]string{"bundle_name", "subdomain", "display_name", "description", "is_active"}).
				AddRow("weather", "weatherhub", "Weather", "desc", true))
		mock.ExpectQuery(regexp.QuoteMeta(listBundleResourceBindingsQuery)).
			WithArgs("weatherhub").
			WillReturnRows(sqlmock.NewRows([]string{"tool_name", "skill_dir_name"}).AddRow(nil, "weather-current"))
		if _, err := store.ListBundleResourceBindings(context.Background(), "weatherhub", nil); err == nil || !strings.Contains(err.Error(), "scan bundle resource binding") {
			t.Fatalf("ListBundleResourceBindings() scan error = %v", err)
		}

		bindingRows := sqlmock.NewRows([]string{"tool_name", "skill_dir_name"}).
			AddRow("weather.current", "weather-current").
			RowError(0, sql.ErrConnDone)
		mock.ExpectQuery(regexp.QuoteMeta(getActiveBundleQuery)).
			WithArgs("weatherhub").
			WillReturnRows(sqlmock.NewRows([]string{"bundle_name", "subdomain", "display_name", "description", "is_active"}).
				AddRow("weather", "weatherhub", "Weather", "desc", true))
		mock.ExpectQuery(regexp.QuoteMeta(listBundleResourceBindingsQuery)).
			WithArgs("weatherhub").
			WillReturnRows(bindingRows)
		if _, err := store.ListBundleResourceBindings(context.Background(), "weatherhub", nil); err == nil || !strings.Contains(err.Error(), "iterate bundle resource bindings") {
			t.Fatalf("ListBundleResourceBindings() row error = %v", err)
		}

		mock.ExpectQuery(regexp.QuoteMeta(getActiveBundleQuery)).
			WithArgs("weatherhub").
			WillReturnError(sql.ErrConnDone)
		if _, err := store.GetBundleResourceBinding(context.Background(), "weatherhub", "weather.current", nil); err == nil || !strings.Contains(err.Error(), "get active bundle") {
			t.Fatalf("GetBundleResourceBinding() propagated error = %v", err)
		}
	})

	t.Run("upsert bundle additional failure branches", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New() error = %v", err)
		}
		defer db.Close()

		store := NewStore(db, WithSkillSyncer(successfulSkillSyncer{}))
		if _, err := store.UpsertBundle(context.Background(), UpsertBundleInput{
			BundleName:  "weather",
			DisplayName: "Weather",
			Skills:      []ManagedSkillInput{{GitHubURL: "https://github.com/example/weather-skill"}},
		}); err == nil || !strings.Contains(err.Error(), "skill name is required") {
			t.Fatalf("UpsertBundle() normalize error = %v", err)
		}

		mock.ExpectQuery(regexp.QuoteMeta(getBundleQuery)).
			WithArgs("weather").
			WillReturnError(sql.ErrNoRows)
		mock.ExpectBegin()
		mock.ExpectExec(regexp.QuoteMeta(upsertBundleQuery)).
			WithArgs("weather", "weatherhub", "Weather", nil, true).
			WillReturnError(sql.ErrConnDone)
		mock.ExpectRollback()
		if _, err := store.UpsertBundle(context.Background(), UpsertBundleInput{
			BundleName:  "weather",
			Subdomain:   "weatherhub",
			DisplayName: "Weather",
			IsActive:    true,
		}); err == nil || !strings.Contains(err.Error(), "upsert bundle") {
			t.Fatalf("UpsertBundle() exec error = %v", err)
		}

		mock.ExpectQuery(regexp.QuoteMeta(getBundleQuery)).
			WithArgs("weather").
			WillReturnError(sql.ErrNoRows)
		mock.ExpectBegin()
		mock.ExpectExec(regexp.QuoteMeta(upsertBundleQuery)).
			WithArgs("weather", "weatherhub", "Weather", nil, true).
			WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectQuery(regexp.QuoteMeta(existingSkillsByNFTIDsQuery)).
			WithArgs(sqlmock.AnyArg()).
			WillReturnError(sql.ErrConnDone)
		mock.ExpectRollback()
		if _, err := store.UpsertBundle(context.Background(), UpsertBundleInput{
			BundleName:  "weather",
			Subdomain:   "weatherhub",
			DisplayName: "Weather",
			IsActive:    true,
			Skills: []ManagedSkillInput{{
				NFTID:       1001,
				Name:        "weather.current",
				Description: "Get weather",
				InputSchema: []byte(`{"type":"object"}`),
				GitHubURL:   "https://github.com/example/weather-skill/tree/main/skills/current",
			}},
		}); err == nil || !strings.Contains(err.Error(), "load existing skills") {
			t.Fatalf("UpsertBundle() prepare error = %v", err)
		}

		mock.ExpectQuery(regexp.QuoteMeta(getBundleQuery)).
			WithArgs("weather").
			WillReturnRows(sqlmock.NewRows([]string{"bundle_name", "subdomain", "display_name", "description", "is_active"}).
				AddRow("weather", "weatherold", "Weather", "desc", true))
		mock.ExpectBegin()
		mock.ExpectQuery(regexp.QuoteMeta(latestSubdomainChangeQuery)).
			WithArgs("weather").
			WillReturnRows(sqlmock.NewRows([]string{"changed_at"}))
		mock.ExpectQuery(regexp.QuoteMeta(monthlySubdomainChangeCountQuery)).
			WithArgs("weather").
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
		mock.ExpectExec(regexp.QuoteMeta(upsertBundleQuery)).
			WithArgs("weather", "weatherhub", "Weather", nil, true).
			WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectQuery(regexp.QuoteMeta(existingSkillsByNFTIDsQuery)).
			WithArgs(sqlmock.AnyArg()).
			WillReturnRows(sqlmock.NewRows([]string{"nft_id", "tool_name", "skill_dir_name", "sync_status", "schema_json", "github_url"}))
		mock.ExpectQuery(regexp.QuoteMeta(listSkillDirectoryNamesQuery)).
			WillReturnRows(sqlmock.NewRows([]string{"nft_id", "skill_dir_name"}))
		mock.ExpectExec(regexp.QuoteMeta(upsertSkillQuery)).
			WithArgs(int64(1001), "weather.current", sqlmock.AnyArg(), "https://github.com/example/weather-skill/tree/main/skills/current", "weather.current").
			WillReturnError(sql.ErrConnDone)
		mock.ExpectRollback()
		if _, err := store.UpsertBundle(context.Background(), UpsertBundleInput{
			BundleName:  "weather",
			Subdomain:   "weatherhub",
			DisplayName: "Weather",
			IsActive:    true,
			Skills: []ManagedSkillInput{{
				NFTID:       1001,
				Name:        "weather.current",
				Description: "Get weather",
				InputSchema: []byte(`{"type":"object"}`),
				GitHubURL:   "https://github.com/example/weather-skill/tree/main/skills/current",
			}},
		}); err == nil || !strings.Contains(err.Error(), "upsert managed skill") {
			t.Fatalf("UpsertBundle() upsert managed skill error = %v", err)
		}

		mock.ExpectQuery(regexp.QuoteMeta(getBundleQuery)).
			WithArgs("weather").
			WillReturnError(sql.ErrNoRows)
		mock.ExpectBegin()
		mock.ExpectExec(regexp.QuoteMeta(upsertBundleQuery)).
			WithArgs("weather", "weatherhub", "Weather", nil, true).
			WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectQuery(regexp.QuoteMeta(activeSkillsByNamesQuery)).
			WithArgs(sqlmock.AnyArg()).
			WillReturnError(sql.ErrConnDone)
		mock.ExpectRollback()
		if _, err := store.UpsertBundle(context.Background(), UpsertBundleInput{
			BundleName:  "weather",
			Subdomain:   "weatherhub",
			DisplayName: "Weather",
			IsActive:    true,
			ToolNames:   []string{"weather.current"},
		}); err == nil || !strings.Contains(err.Error(), "load active skills for bundle") {
			t.Fatalf("UpsertBundle() replace skills error = %v", err)
		}

		mock.ExpectQuery(regexp.QuoteMeta(getBundleQuery)).
			WithArgs("weather").
			WillReturnRows(sqlmock.NewRows([]string{"bundle_name", "subdomain", "display_name", "description", "is_active"}).
				AddRow("weather", "weatherold", "Weather", "desc", true))
		mock.ExpectBegin()
		mock.ExpectQuery(regexp.QuoteMeta(latestSubdomainChangeQuery)).
			WithArgs("weather").
			WillReturnRows(sqlmock.NewRows([]string{"changed_at"}))
		mock.ExpectQuery(regexp.QuoteMeta(monthlySubdomainChangeCountQuery)).
			WithArgs("weather").
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
		mock.ExpectExec(regexp.QuoteMeta(upsertBundleQuery)).
			WithArgs("weather", "weatherhub", "Weather", nil, true).
			WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectExec(regexp.QuoteMeta(deleteBundleSkillsQuery)).
			WithArgs("weather").
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(regexp.QuoteMeta(insertSubdomainChangeQuery)).
			WithArgs("weather", "weatherold", "weatherhub").
			WillReturnError(sql.ErrConnDone)
		mock.ExpectRollback()
		if _, err := store.UpsertBundle(context.Background(), UpsertBundleInput{
			BundleName:  "weather",
			Subdomain:   "weatherhub",
			DisplayName: "Weather",
			IsActive:    true,
		}); err == nil || !strings.Contains(err.Error(), "record subdomain change") {
			t.Fatalf("UpsertBundle() subdomain change error = %v", err)
		}
	})

	t.Run("deactivate exec error", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New() error = %v", err)
		}
		defer db.Close()

		store := NewStore(db)
		mock.ExpectExec(regexp.QuoteMeta(deactivateBundleQuery)).
			WithArgs("weather").
			WillReturnError(sql.ErrConnDone)
		if err := store.DeactivateBundle(context.Background(), "weather"); err == nil || !strings.Contains(err.Error(), "deactivate bundle") {
			t.Fatalf("DeactivateBundle() error = %v", err)
		}
	})

	t.Run("promote and sync additional errors", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New() error = %v", err)
		}
		defer db.Close()

		store := NewStore(db, WithSkillSyncer(successfulSkillSyncer{}))
		mock.ExpectBegin()
		mock.ExpectExec(regexp.QuoteMeta(promoteReadySkillQuery)).
			WithArgs(int64(1001), "weather.current", []byte(`{"name":"weather.current"}`), "https://github.com/example/weather-skill").
			WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectExec(regexp.QuoteMeta(restoreBundleSkillToolNameQuery)).
			WithArgs("weather", "weather.old", "weather.current").
			WillReturnError(sql.ErrConnDone)
		mock.ExpectRollback()
		if err := store.promoteReadySkill(context.Background(), preparedManagedSkill{
			ManagedSkillInput: ManagedSkillInput{NFTID: 1001, Name: "weather.current", GitHubURL: "https://github.com/example/weather-skill"},
			BundleName:        "weather",
			SchemaJSON:        []byte(`{"name":"weather.current"}`),
			PreviousToolName:  "weather.old",
		}); err == nil || !strings.Contains(err.Error(), "promote bundle skill tool name") {
			t.Fatalf("promoteReadySkill() rename error = %v", err)
		}

		mock.ExpectBegin().WillReturnError(sql.ErrConnDone)
		if err := store.syncPreparedSkills(context.Background(), []preparedManagedSkill{{
			ManagedSkillInput: ManagedSkillInput{NFTID: 1001, Name: "weather.current", GitHubURL: "https://github.com/example/weather-skill"},
			SchemaJSON:        []byte(`{"name":"weather.current"}`),
			DeferMetadataSync: true,
		}}); err == nil || !strings.Contains(err.Error(), "promote ready skill") {
			t.Fatalf("syncPreparedSkills() promote error = %v", err)
		}
	})

	t.Run("replace bundle skills remaining errors", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New() error = %v", err)
		}
		defer db.Close()

		mock.ExpectBegin()
		tx, err := db.Begin()
		if err != nil {
			t.Fatalf("Begin() error = %v", err)
		}
		mock.ExpectExec(regexp.QuoteMeta(deleteBundleSkillsQuery)).
			WithArgs("weather").
			WillReturnError(sql.ErrConnDone)
		if err := replaceBundleSkills(context.Background(), tx, "weather", nil); err == nil || !strings.Contains(err.Error(), "clear bundle skills") {
			t.Fatalf("replaceBundleSkills() empty clear error = %v", err)
		}

		mock.ExpectBegin()
		tx, err = db.Begin()
		if err != nil {
			t.Fatalf("Begin() error = %v", err)
		}
		mock.ExpectQuery(regexp.QuoteMeta(activeSkillsByNamesQuery)).
			WithArgs(sqlmock.AnyArg()).
			WillReturnError(sql.ErrConnDone)
		if err := replaceBundleSkills(context.Background(), tx, "weather", []string{"weather.current"}); err == nil || !strings.Contains(err.Error(), "load active skills for bundle") {
			t.Fatalf("replaceBundleSkills() query error = %v", err)
		}

		mock.ExpectBegin()
		tx, err = db.Begin()
		if err != nil {
			t.Fatalf("Begin() error = %v", err)
		}
		mock.ExpectQuery(regexp.QuoteMeta(activeSkillsByNamesQuery)).
			WithArgs(sqlmock.AnyArg()).
			WillReturnRows(sqlmock.NewRows([]string{"tool_name"}).AddRow(nil))
		if err := replaceBundleSkills(context.Background(), tx, "weather", []string{"weather.current"}); err == nil || !strings.Contains(err.Error(), "scan active bundle skill") {
			t.Fatalf("replaceBundleSkills() scan error = %v", err)
		}

		mock.ExpectBegin()
		tx, err = db.Begin()
		if err != nil {
			t.Fatalf("Begin() error = %v", err)
		}
		activeRows := sqlmock.NewRows([]string{"tool_name"}).
			AddRow("weather.current").
			RowError(0, sql.ErrConnDone)
		mock.ExpectQuery(regexp.QuoteMeta(activeSkillsByNamesQuery)).
			WithArgs(sqlmock.AnyArg()).
			WillReturnRows(activeRows)
		if err := replaceBundleSkills(context.Background(), tx, "weather", []string{"weather.current"}); err == nil || !strings.Contains(err.Error(), "iterate active bundle skills") {
			t.Fatalf("replaceBundleSkills() row error = %v", err)
		}

		mock.ExpectBegin()
		tx, err = db.Begin()
		if err != nil {
			t.Fatalf("Begin() error = %v", err)
		}
		mock.ExpectQuery(regexp.QuoteMeta(activeSkillsByNamesQuery)).
			WithArgs(sqlmock.AnyArg()).
			WillReturnRows(sqlmock.NewRows([]string{"tool_name"}).AddRow("weather.current"))
		mock.ExpectExec(regexp.QuoteMeta(deleteBundleSkillsQuery)).
			WithArgs("weather").
			WillReturnError(sql.ErrConnDone)
		if err := replaceBundleSkills(context.Background(), tx, "weather", []string{"weather.current"}); err == nil || !strings.Contains(err.Error(), "clear bundle skills") {
			t.Fatalf("replaceBundleSkills() delete error = %v", err)
		}
	})

	t.Run("marshal and prepare helpers remaining branches", func(t *testing.T) {
		previousMarshal := marshalToolSchema
		marshalToolSchema = func(any) ([]byte, error) {
			return nil, sql.ErrConnDone
		}
		defer func() {
			marshalToolSchema = previousMarshal
		}()
		if _, err := marshalSkillSchema(ManagedSkillInput{
			NFTID:       1001,
			Name:        "weather.current",
			Description: "Get weather",
			InputSchema: []byte(`{"type":"object"}`),
			GitHubURL:   "https://github.com/example/weather-skill",
		}); err == nil || !strings.Contains(err.Error(), "marshal managed skill schema") {
			t.Fatalf("marshalSkillSchema() marshal error = %v", err)
		}

		marshalToolSchema = previousMarshal
		if _, err := marshalSkillSchema(ManagedSkillInput{
			NFTID:       1001,
			Name:        "weather.current",
			Description: "Get weather",
			InputSchema: []byte(`[]`),
			GitHubURL:   "https://github.com/example/weather-skill",
		}); err == nil || !strings.Contains(err.Error(), "invalid inputSchema") {
			t.Fatalf("marshalSkillSchema() error = %v", err)
		}

		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New() error = %v", err)
		}
		defer db.Close()

		mock.ExpectBegin()
		tx, err := db.Begin()
		if err != nil {
			t.Fatalf("Begin() error = %v", err)
		}
		mock.ExpectQuery(regexp.QuoteMeta(existingSkillsByNFTIDsQuery)).
			WithArgs(sqlmock.AnyArg()).
			WillReturnError(sql.ErrConnDone)
		if _, err := prepareManagedSkills(context.Background(), tx, "weather", []ManagedSkillInput{{Name: "weather.current"}}); err == nil || !strings.Contains(err.Error(), "load existing skills") {
			t.Fatalf("prepareManagedSkills() existing error = %v", err)
		}

		mock.ExpectBegin()
		tx, err = db.Begin()
		if err != nil {
			t.Fatalf("Begin() error = %v", err)
		}
		mock.ExpectQuery(regexp.QuoteMeta(existingSkillsByNFTIDsQuery)).
			WithArgs(sqlmock.AnyArg()).
			WillReturnRows(sqlmock.NewRows([]string{"nft_id", "tool_name", "skill_dir_name", "sync_status", "schema_json", "github_url"}))
		mock.ExpectQuery(regexp.QuoteMeta(listSkillDirectoryNamesQuery)).
			WillReturnError(sql.ErrConnDone)
		if _, err := prepareManagedSkills(context.Background(), tx, "weather", []ManagedSkillInput{{
			NFTID:       1001,
			Name:        "weather.current",
			Description: "Get weather",
			InputSchema: []byte(`{"type":"object"}`),
			GitHubURL:   "https://github.com/example/weather-skill/tree/main/skills/current",
		}}); err == nil || !strings.Contains(err.Error(), "load skill directory names") {
			t.Fatalf("prepareManagedSkills() dir error = %v", err)
		}

		mock.ExpectBegin()
		tx, err = db.Begin()
		if err != nil {
			t.Fatalf("Begin() error = %v", err)
		}
		mock.ExpectQuery(regexp.QuoteMeta(existingSkillsByNFTIDsQuery)).
			WithArgs(sqlmock.AnyArg()).
			WillReturnRows(sqlmock.NewRows([]string{"nft_id", "tool_name", "skill_dir_name", "sync_status", "schema_json", "github_url"}))
		mock.ExpectQuery(regexp.QuoteMeta(listSkillDirectoryNamesQuery)).
			WillReturnRows(sqlmock.NewRows([]string{"nft_id", "skill_dir_name"}))
		if _, err := prepareManagedSkills(context.Background(), tx, "weather", []ManagedSkillInput{{
			NFTID:       1001,
			Name:        "weather.current",
			Description: "Get weather",
			InputSchema: []byte(`[]`),
			GitHubURL:   "https://github.com/example/weather-skill/tree/main/skills/current",
		}}); err == nil || !strings.Contains(err.Error(), "invalid inputSchema") {
			t.Fatalf("prepareManagedSkills() schema error = %v", err)
		}
	})

	t.Run("normalize helpers remaining branches", func(t *testing.T) {
		if normalizeSkillDirBase("a__b") != "a_b" {
			t.Fatalf("normalizeSkillDirBase() did not collapse underscores")
		}
		_, skills, err := normalizeBundleSkillInputs(nil, []ManagedSkillInput{
			{Name: "weather.current", GitHubURL: "https://github.com/example/weather-skill"},
			{Name: "weather.current", GitHubURL: "https://github.com/example/weather-skill"},
		})
		if err != nil {
			t.Fatalf("normalizeBundleSkillInputs() error = %v", err)
		}
		if len(skills) != 1 {
			t.Fatalf("normalizeBundleSkillInputs() skills = %#v", skills)
		}
	})

	t.Run("generate unique subdomain exhausted", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New() error = %v", err)
		}
		defer db.Close()

		mock.ExpectBegin()
		tx, err := db.Begin()
		if err != nil {
			t.Fatalf("Begin() error = %v", err)
		}
		for range [10]struct{}{} {
			mock.ExpectQuery(regexp.QuoteMeta(subdomainExistsQuery)).
				WithArgs(sqlmock.AnyArg()).
				WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
		}
		if _, err := generateUniqueSubdomain(context.Background(), tx); !errors.Is(err, ErrSubdomainTaken) {
			t.Fatalf("generateUniqueSubdomain() error = %v", err)
		}
	})

	t.Run("generate random subdomain read errors", func(t *testing.T) {
		previousReadRandomBytes := readRandomBytes
		readRandomBytes = func([]byte) (int, error) {
			return 0, sql.ErrConnDone
		}
		defer func() {
			readRandomBytes = previousReadRandomBytes
		}()

		if _, err := generateRandomSubdomain(); err == nil || !strings.Contains(err.Error(), "generate subdomain") {
			t.Fatalf("generateRandomSubdomain() error = %v", err)
		}

		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New() error = %v", err)
		}
		defer db.Close()

		mock.ExpectBegin()
		tx, err := db.Begin()
		if err != nil {
			t.Fatalf("Begin() error = %v", err)
		}
		if _, err := generateUniqueSubdomain(context.Background(), tx); err == nil || !strings.Contains(err.Error(), "generate subdomain") {
			t.Fatalf("generateUniqueSubdomain() error = %v", err)
		}
	})
}

type failingSkillSyncer struct{}

func (failingSkillSyncer) Sync(context.Context, string, string) error {
	return errors.New("sync failed")
}

type flakySkillSyncer struct {
	failuresRemaining int
}

func (f *flakySkillSyncer) Sync(context.Context, string, string) error {
	if f.failuresRemaining <= 0 {
		return nil
	}
	f.failuresRemaining--
	return errors.New("sync failed")
}

type successfulSkillSyncer struct{}

func (successfulSkillSyncer) Sync(context.Context, string, string) error {
	return nil
}

var _ = sql.ErrNoRows

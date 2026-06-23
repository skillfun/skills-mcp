package bundle

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
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

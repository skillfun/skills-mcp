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

var _ = sql.ErrNoRows

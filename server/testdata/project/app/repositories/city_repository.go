package repositories

import (
	"context"
	"fmt"

	"github.com/rusmanplatd/basegoravel/app/models"

	"github.com/rusmanplatd/basegoravel/app/facades"

	"github.com/google/uuid"
)

type CityRepository struct {
}

func NewCityRepository() *CityRepository {
	return &CityRepository{}
}

func (repository *CityRepository) FindAll(ctx context.Context, search string, provinceID *uuid.UUID, page, perPage int) ([]map[string]any, int, error) {
	refGeoProvinceTableName := (models.RefGeoProvince{}).TableName(ctx)
	refGeoCountryTableName := (models.RefGeoCountry{}).TableName(ctx)
	refGeoCityTableName := (models.RefGeoCity{}).TableName(ctx)
	offset := (page - 1) * perPage

	var countQuery, mainQuery string
	var countArgs, args []any
	argIndex := 1

	baseCountQuery := "SELECT COUNT(*) FROM " + refGeoCityTableName + " WHERE deleted_at IS NULL"
	baseMainQuery := `
		SELECT ct.id, ct.province_id, ct.name, ct.code, ct.latitude, ct.longitude,
		       ct.created_at, ct.updated_at, ct.created_by_id, ct.updated_by_id,
		       p.name as province_name, c.name as country_name
		FROM ` + refGeoCityTableName + ` ct
		LEFT JOIN ` + refGeoProvinceTableName + ` p ON ct.province_id = p.id
		LEFT JOIN ` + refGeoCountryTableName + ` c ON p.country_id = c.id
		WHERE ct.deleted_at IS NULL
	`

	where := ""
	if provinceID != nil {
		where += fmt.Sprintf(" AND ct.province_id = $%d", argIndex)
		countArgs = append(countArgs, *provinceID)
		args = append(args, *provinceID)
		argIndex++
	}
	if search != "" {
		where += fmt.Sprintf(" AND ct.name ILIKE $%d", argIndex)
		searchPattern := "%" + search + "%"
		countArgs = append(countArgs, searchPattern)
		args = append(args, searchPattern)
		argIndex++
	}

	countQuery = baseCountQuery + where
	mainQuery = baseMainQuery + where + fmt.Sprintf(" ORDER BY ct.name ASC LIMIT $%d OFFSET $%d", argIndex, argIndex+1)
	args = append(args, perPage, offset)

	var total int
	if err := facades.DB().Select(&total, countQuery, countArgs); err != nil {
		return nil, 0, err
	}

	rows := make([]models.CityRow, 0)
	if err := facades.DB().Select(&rows, mainQuery, args); err != nil {
		return nil, 0, err
	}

	cities := make([]map[string]any, 0)
	for _, row := range rows {
		result := map[string]any{
			"id":            row.ID,
			"province_id":   row.ProvinceID,
			"name":          row.Name,
			"created_at":    row.CreatedAt,
			"updated_at":    row.UpdatedAt,
			"created_by_id": row.CreatedByID,
			"updated_by_id": row.UpdatedByID,
		}

		if row.Code.Valid {
			result["code"] = row.Code.String
		}
		if row.Latitude.Valid {
			result["latitude"] = row.Latitude.Float64
		}
		if row.Longitude.Valid {
			result["longitude"] = row.Longitude.Float64
		}
		if row.ProvinceName.Valid {
			result["province_name"] = row.ProvinceName.String
		}
		if row.CountryName.Valid {
			result["country_name"] = row.CountryName.String
		}

		cities = append(cities, result)
	}

	return cities, total, nil
}

func (repository *CityRepository) FindByID(ctx context.Context, id uuid.UUID) (*models.RefGeoCity, error) {
	refGeoCityTableName := (models.RefGeoCity{}).TableName(ctx)
	var row models.CityRow
	query := `
		SELECT id, province_id, name, code, latitude, longitude, created_at, updated_at, created_by_id, updated_by_id
		FROM ` + refGeoCityTableName + `
		WHERE id = $1 AND deleted_at IS NULL
	`

	err := facades.DB().Select(&row, query, id)
	if err != nil {
		return nil, err
	}

	city := &models.RefGeoCity{
		ID:          row.ID,
		ProvinceID:  row.ProvinceID,
		Name:        row.Name,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
		CreatedByID: row.CreatedByID,
		UpdatedByID: row.UpdatedByID,
	}

	if row.Code.Valid {
		city.Code = &row.Code.String
	}
	if row.Latitude.Valid {
		city.Latitude = &row.Latitude.Float64
	}
	if row.Longitude.Valid {
		city.Longitude = &row.Longitude.Float64
	}

	return city, nil
}

func (repository *CityRepository) Create(ctx context.Context, city *models.RefGeoCity) error {
	refGeoCityTableName := (models.RefGeoCity{}).TableName(ctx)
	query := `
		INSERT INTO ` + refGeoCityTableName + ` (id, province_id, name, code, latitude, longitude, created_by_id, updated_by_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`

	var codeVal, latVal, lonVal any
	if city.Code != nil {
		codeVal = *city.Code
	}
	if city.Latitude != nil {
		latVal = *city.Latitude
	}
	if city.Longitude != nil {
		lonVal = *city.Longitude
	}
	_, err := facades.DB().Insert(query,
		city.ID,
		city.ProvinceID,
		city.Name,
		codeVal,
		latVal,
		lonVal,
		city.CreatedByID,
		city.UpdatedByID,
		city.CreatedAt,
		city.UpdatedAt)

	return err
}

func (repository *CityRepository) Update(ctx context.Context, id uuid.UUID, patches map[string]any) error {
	refGeoCityTableName := (models.RefGeoCity{}).TableName(ctx)
	query, args, err := BuildPatchQueryWithWhere(refGeoCityTableName, id, patches, false)
	if err != nil {
		return fmt.Errorf("failed to build patch query: %w", err)
	}

	_, err = facades.DB().Update(query, args...)
	return err
}

func (repository *CityRepository) Delete(ctx context.Context, id uuid.UUID, deletedByID uuid.UUID) error {
	refGeoCityTableName := (models.RefGeoCity{}).TableName(ctx)
	query := `
		UPDATE ` + refGeoCityTableName + `
		SET deleted_at = NOW(), deleted_by_id = $1
		WHERE id = $2
	`
	_, err := facades.DB().Update(query, deletedByID, id)
	return err
}

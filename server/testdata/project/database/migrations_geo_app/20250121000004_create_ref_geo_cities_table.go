package migrations_geo_app

import (
	"context"

	"github.com/rusmanplatd/basegoravel/app/facades"
	"github.com/rusmanplatd/basegoravel/app/models"
)

type M20250121000004CreateRefGeoCitiesTable struct{}

// Signature The unique signature for the migration.
func (r *M20250121000004CreateRefGeoCitiesTable) Signature() string {
	return "20250121000004_create_ref_geo_cities_table"
}

// Up Run the migrations.
func (r *M20250121000004CreateRefGeoCitiesTable) Up() error {
	ctx := context.Background()
	refGeoCityOriginalTable := string(models.RefGeoCityOriginalTableName)
	schema := models.RefGeoCity{}.SchemaName(ctx)
	refGeoCities := models.RefGeoCity{}.TableName(ctx)
	refGeoProvinces := models.RefGeoProvince{}.TableName(ctx)
	err := facades.DB().Statement(`
		CREATE TABLE ` + refGeoCities + ` (
			id UUID PRIMARY KEY,
			province_id UUID NOT NULL,
			name VARCHAR(255) NOT NULL,
			code VARCHAR(50),
			latitude DECIMAL(10, 8),
			longitude DECIMAL(11, 8),
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			created_by_id UUID NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_by_id UUID NOT NULL,
			deleted_at TIMESTAMPTZ,
			deleted_by_id UUID,
			CONSTRAINT fk_ref_geo_cities_province FOREIGN KEY (province_id) REFERENCES ` + refGeoProvinces + `(id) ON DELETE CASCADE,
			CONSTRAINT fk_ref_geo_cities_created_by_id FOREIGN KEY (created_by_id) REFERENCES auth.users(id),
			CONSTRAINT fk_ref_geo_cities_updated_by_id FOREIGN KEY (updated_by_id) REFERENCES auth.users(id),
			CONSTRAINT fk_ref_geo_cities_deleted_by_id FOREIGN KEY (deleted_by_id) REFERENCES auth.users(id)
		);

		CREATE INDEX idx_ref_geo_cities_name ON ` + refGeoCities + `(name);
		CREATE INDEX idx_ref_geo_cities_province_id ON ` + refGeoCities + `(province_id);
		CREATE INDEX idx_ref_geo_cities_code ON ` + refGeoCities + `(code);
		CREATE INDEX idx_ref_geo_cities_coordinates ON ` + refGeoCities + `(latitude, longitude);
		CREATE INDEX idx_ref_geo_cities_created_at ON ` + refGeoCities + `(created_at);
		CREATE INDEX idx_ref_geo_cities_created_by_id ON ` + refGeoCities + `(created_by_id);
		CREATE INDEX idx_ref_geo_cities_updated_by_id ON ` + refGeoCities + `(updated_by_id);
		CREATE INDEX idx_ref_geo_cities_deleted_by_id ON ` + refGeoCities + `(deleted_by_id);

		DO $$
		BEGIN
			IF NOT EXISTS (
				SELECT 1
				FROM pg_publication_tables
				WHERE pubname = 'supabase_realtime'
				  AND schemaname = '` + schema + `'
				  AND tablename = '` + refGeoCityOriginalTable + `'
			) THEN
				ALTER PUBLICATION supabase_realtime
				ADD TABLE ` + refGeoCities + `;
			END IF;
		END
		$$;
	`)
	return err
}

// Down Reverse the migrations.
func (r *M20250121000004CreateRefGeoCitiesTable) Down() error {
	ctx := context.Background()
	refGeoCities := models.RefGeoCity{}.TableName(ctx)
	err := facades.DB().Statement(`DROP TABLE ` + refGeoCities + ` CASCADE;`)
	return err
}

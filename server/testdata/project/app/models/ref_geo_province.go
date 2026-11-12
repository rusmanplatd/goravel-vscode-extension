package models

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// RefGeoProvince represents a geographical province/state
// @Description Reference data model for provinces or states within a country
type RefGeoProvince struct {
	// @Description Unique identifier
	// @Example "01234567-89ab-cdef-0123-456789abcdef"
	ID uuid.UUID `gorm:"type:uuid;primary_key" json:"id"`

	// @Description Country ID this province belongs to
	// @Example "01234567-89ab-cdef-0123-456789abcdef"
	CountryID uuid.UUID `gorm:"type:uuid;not null;index" json:"country_id"`

	// @Description Province name
	// @Example "West Java"
	Name string `gorm:"type:varchar(255);not null" json:"name"`

	// @Description Province code
	// @Example "JB"
	Code *string `gorm:"type:varchar(50)" json:"code,omitempty"`

	// @Description Timestamp when created
	CreatedAt time.Time `gorm:"not null;default:now()" json:"created_at"`

	// @Description Timestamp when last updated
	UpdatedAt time.Time `gorm:"not null;default:now()" json:"updated_at"`

	// @Description Timestamp when soft deleted
	DeletedAt *time.Time `gorm:"index" json:"deleted_at,omitempty"`

	// Audit fields
	// @Description ID of user who created this record
	CreatedByID uuid.UUID `gorm:"type:uuid;not null" json:"created_by_id"`
	// @Description ID of user who last updated this record
	UpdatedByID uuid.UUID `gorm:"type:uuid;not null" json:"updated_by_id"`
	// @Description ID of user who deleted this record
	DeletedByID *uuid.UUID `gorm:"type:uuid" json:"deleted_by_id,omitempty"`
	// @Description User who created this record
	CreatedBy *SupabaseAuthUser `gorm:"foreignKey:CreatedByID;references:ID" json:"created_by,omitempty"`
	// @Description User who last updated this record
	UpdatedBy *SupabaseAuthUser `gorm:"foreignKey:UpdatedByID;references:ID" json:"updated_by,omitempty"`
	// @Description User who deleted this record
	DeletedBy *SupabaseAuthUser `gorm:"foreignKey:DeletedByID;references:ID" json:"deleted_by,omitempty"`

	// Relationships
	// @Description Country this province belongs to
	Country *RefGeoCountry `gorm:"foreignKey:CountryID;references:ID" json:"country,omitempty"`
	// @Description Cities in this province
	Cities []RefGeoCity `gorm:"foreignKey:ProvinceID;references:ID" json:"cities,omitempty"`
}

func (model RefGeoProvince) TableName(ctx ...context.Context) string {
	if len(ctx) > 0 && ctx[0] != nil {
		return model.SchemaName(ctx[0]) + "." + string(RefGeoProvinceOriginalTableName)
	}
	return model.SchemaName(context.Background()) + "." + string(RefGeoProvinceOriginalTableName)
}

func (model RefGeoProvince) OriginalSchemaName() string {
	return string(DbSchemaGeoApp)
}

func (model RefGeoProvince) SchemaName(ctx context.Context) string {
	return model.OriginalSchemaName() + GetTenantUUIDString(ctx)
}

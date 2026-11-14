package models

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// RefGeoCity represents a geographical city
// @Description Reference data model for cities with optional coordinates
type RefGeoCity struct {
	// @Description Unique identifier
	// @Example "01234567-89ab-cdef-0123-456789abcdef"
	ID uuid.UUID `gorm:"type:uuid;primary_key" json:"id"`

	// @Description Province ID this city belongs to
	// @Example "01234567-89ab-cdef-0123-456789abcdef"
	ProvinceID uuid.UUID `gorm:"type:uuid;not null;index" json:"province_id"`

	// @Description City name
	// @Example "Bandung"
	Name string `gorm:"type:varchar(255);not null" json:"name"`

	// @Description City code
	// @Example "BDG"
	Code *string `gorm:"type:varchar(50)" json:"code,omitempty"`

	// @Description Latitude coordinate
	// @Example -6.9175
	Latitude *float64 `gorm:"type:decimal(10,8)" json:"latitude,omitempty"`

	// @Description Longitude coordinate
	// @Example 107.6191
	Longitude *float64 `gorm:"type:decimal(11,8)" json:"longitude,omitempty"`

	// @Description Timestamp when created
	CreatedAt time.Time `gorm:"not null;default:now()" json:"created_at,omitempty"`
	// @Description Timestamp when last updated
	UpdatedAt time.Time `gorm:"not null;default:now()" json:"updated_at,omitempty"`
	// @Description Timestamp when soft deleted
	DeletedAt *time.Time `gorm:"index" json:"deleted_at,omitempty"`

	// Audit fields
	// @Description ID of user who created this record
	CreatedByID uuid.UUID         `gorm:"type:uuid;not null" json:"created_by_id"`
	UpdatedByID uuid.UUID         `gorm:"type:uuid;not null" json:"updated_by_id"`
	DeletedByID *uuid.UUID        `gorm:"type:uuid" json:"deleted_by_id,omitempty"`
	CreatedBy   *SupabaseAuthUser `gorm:"foreignKey:CreatedByID;references:ID" json:"created_by,omitempty"`
	UpdatedBy   *SupabaseAuthUser `gorm:"foreignKey:UpdatedByID;references:ID" json:"updated_by,omitempty"`
	DeletedBy   *SupabaseAuthUser `gorm:"foreignKey:DeletedByID;references:ID" json:"deleted_by,omitempty"`

	// Relationships
	// @Description Province this city belongs to
	Province *RefGeoProvince `gorm:"foreignKey:ProvinceID;references:ID" json:"province,omitempty"`
	// @Description Districts in this city
	Districts []RefGeoDistrict `gorm:"foreignKey:CityID;references:ID" json:"districts,omitempty"`
}

func (model RefGeoCity) TableName(ctx ...context.Context) string {
	if len(ctx) > 0 && ctx[0] != nil {
		return model.SchemaName(ctx[0]) + "." + string(RefGeoCityOriginalTableName)
	}
	return model.SchemaName(context.Background()) + "." + string(RefGeoCityOriginalTableName)
}

func (model RefGeoCity) OriginalSchemaName() string {
	return string(DbSchemaGeoApp)
}

func (model RefGeoCity) SchemaName(ctx context.Context) string {
	return model.OriginalSchemaName() + GetTenantUUIDString(ctx)
}

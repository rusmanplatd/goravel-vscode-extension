package models

import (
	"database/sql"
	"time"

	"github.com/google/uuid"
)

// ContinentRow represents a continent record from database queries
type ContinentRow struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Code        string    `json:"code"`
	CreatedAt   time.Time `json:"created_at,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
	CreatedByID uuid.UUID `json:"created_by_id"`
	UpdatedByID uuid.UUID `json:"updated_by_id"`
}

// ProvinceRow represents a province record from database queries with joined data
type ProvinceRow struct {
	ID          uuid.UUID      `json:"id"`
	CountryID   uuid.UUID      `json:"country_id"`
	Name        string         `json:"name"`
	Code        sql.NullString `json:"code"`
	CreatedAt   time.Time      `json:"created_at,omitempty"`
	UpdatedAt   time.Time      `json:"updated_at,omitempty"`
	CreatedByID uuid.UUID      `json:"created_by_id"`
	UpdatedByID uuid.UUID      `json:"updated_by_id"`
	CountryName sql.NullString `json:"country_name"`
}

// CityRow represents a city record from database queries with joined data
type CityRow struct {
	ID           uuid.UUID       `json:"id"`
	ProvinceID   uuid.UUID       `json:"province_id"`
	Name         string          `json:"name"`
	Code         sql.NullString  `json:"code"`
	Latitude     sql.NullFloat64 `json:"latitude"`
	Longitude    sql.NullFloat64 `json:"longitude"`
	CreatedAt    time.Time       `json:"created_at,omitempty"`
	UpdatedAt    time.Time       `json:"updated_at,omitempty"`
	CreatedByID  uuid.UUID       `json:"created_by_id"`
	UpdatedByID  uuid.UUID       `json:"updated_by_id"`
	ProvinceName sql.NullString  `json:"province_name"`
	CountryName  sql.NullString  `json:"country_name"`
}

// CountryRow represents a country record from database queries
type CountryRow struct {
	ID          uuid.UUID      `json:"id"`
	ContinentID uuid.UUID      `json:"continent_id"`
	Name        string         `json:"name"`
	IsoCode     string         `json:"iso_code"`
	PhoneCode   sql.NullString `json:"phone_code"`
	CreatedAt   time.Time      `json:"created_at,omitempty"`
	UpdatedAt   time.Time      `json:"updated_at,omitempty"`
	CreatedByID uuid.UUID      `json:"created_by_id"`
	UpdatedByID uuid.UUID      `json:"updated_by_id"`
}

// DistrictRow represents a district record from database queries with joined data
type DistrictRow struct {
	ID           uuid.UUID      `json:"id"`
	CityID       uuid.UUID      `json:"city_id"`
	Name         string         `json:"name"`
	Code         sql.NullString `json:"code"`
	CreatedAt    time.Time      `json:"created_at,omitempty"`
	UpdatedAt    time.Time      `json:"updated_at,omitempty"`
	CreatedByID  uuid.UUID      `json:"created_by_id"`
	UpdatedByID  uuid.UUID      `json:"updated_by_id"`
	CityName     sql.NullString `json:"city_name"`
	ProvinceName sql.NullString `json:"province_name"`
}

// VillageRow represents a village record from database queries with joined data
type VillageRow struct {
	ID           uuid.UUID       `json:"id"`
	DistrictID   uuid.UUID       `json:"district_id"`
	Name         string          `json:"name"`
	Code         sql.NullString  `json:"code"`
	Latitude     sql.NullFloat64 `json:"latitude"`
	Longitude    sql.NullFloat64 `json:"longitude"`
	CreatedAt    time.Time       `json:"created_at,omitempty"`
	UpdatedAt    time.Time       `json:"updated_at,omitempty"`
	CreatedByID  uuid.UUID       `json:"created_by_id"`
	UpdatedByID  uuid.UUID       `json:"updated_by_id"`
	DistrictName sql.NullString  `json:"district_name"`
	CityName     sql.NullString  `json:"city_name"`
}

// RoleRow represents a role record from database queries
type RoleRow struct {
	ID          uuid.UUID      `json:"id"`
	TenantID    uuid.UUID      `json:"tenant_id"`
	Name        string         `json:"name"`
	Description sql.NullString `json:"description"`
	OwnerType   string         `json:"owner_type"`
	OwnerID     uuid.NullUUID  `json:"owner_id"`
	ScopeType   sql.NullString `json:"scope_type"`
	ScopeID     uuid.NullUUID  `json:"scope_id"`
	CreatedAt   time.Time      `json:"created_at,omitempty"`
	UpdatedAt   time.Time      `json:"updated_at,omitempty"`
	CreatedByID uuid.UUID      `json:"created_by_id"`
	UpdatedByID uuid.UUID      `json:"updated_by_id"`
}

// UserRoleRow represents a role record with prefixed fields for joins in user queries
type UserRoleRow struct {
	RoleID          uuid.UUID      `json:"role_id"`
	RoleTenantID    uuid.UUID      `json:"role_tenant_id"`
	RoleName        string         `json:"role_name"`
	RoleDescription sql.NullString `json:"role_description"`
	RoleOwnerType   string         `json:"role_owner_type"`
	RoleOwnerID     uuid.NullUUID  `json:"role_owner_id"`
	RoleScopeType   sql.NullString `json:"role_scope_type"`
	RoleScopeID     uuid.NullUUID  `json:"role_scope_id"`
	RoleCreatedAt   time.Time      `json:"role_created_at"`
	RoleUpdatedAt   time.Time      `json:"role_updated_at"`
	RoleCreatedByID uuid.UUID      `json:"role_created_by_id"`
	RoleUpdatedByID uuid.UUID      `json:"role_updated_by_id"`
}

// PermissionRow represents a permission record from database queries
type PermissionRow struct {
	ID          uuid.UUID      `json:"id"`
	TenantID    uuid.UUID      `json:"tenant_id"`
	Resource    string         `json:"resource"`
	Action      string         `json:"action"`
	OwnerType   string         `json:"owner_type"`
	OwnerID     uuid.NullUUID  `json:"owner_id"`
	ScopeType   sql.NullString `json:"scope_type"`
	ScopeID     uuid.NullUUID  `json:"scope_id"`
	CreatedAt   time.Time      `json:"created_at,omitempty"`
	UpdatedAt   time.Time      `json:"updated_at,omitempty"`
	CreatedByID uuid.UUID      `json:"created_by_id"`
	UpdatedByID uuid.UUID      `json:"updated_by_id"`
}

// UserRow represents a user record from database queries
type UserRow struct {
	ID              uuid.UUID    `json:"id"`
	Name            string       `json:"name"`
	Email           string       `json:"email"`
	EmailVerifiedAt sql.NullTime `json:"email_verified_at"`
	CreatedAt       time.Time    `json:"created_at,omitempty"`
	UpdatedAt       time.Time    `json:"updated_at,omitempty"`
	CreatedByID     uuid.UUID    `json:"created_by_id"`
	UpdatedByID     uuid.UUID    `json:"updated_by_id"`
}

// UserOrgRow represents a user organization record with all related data from joins
type UserOrgRow struct {
	// OrganizationMember fields - these must match SQL aliases exactly
	UorgID        uuid.UUID    `json:"uorg_id"`
	UorgTenantID  uuid.UUID    `json:"uorg_tenant_id"`
	UorgUserID    uuid.UUID    `json:"uorg_user_id"`
	UorgOrgID     uuid.UUID    `json:"uorg_org_id"`
	UorgPosID     uuid.UUID    `json:"uorg_pos_id"`
	UorgIsActive  bool         `json:"uorg_is_active"`
	UorgStartedAt time.Time    `json:"uorg_started_at"`
	UorgEndedAt   sql.NullTime `json:"uorg_ended_at"`
	UorgCreatedAt time.Time    `json:"uorg_created_at"`
	UorgUpdatedAt time.Time    `json:"uorg_updated_at"`
	UorgCreatedBy uuid.UUID    `json:"uorg_created_by"`
	UorgUpdatedBy uuid.UUID    `json:"uorg_updated_by"`

	// Position fields
	PosID        uuid.UUID      `json:"pos_id"`
	PosTenantID  uuid.UUID      `json:"pos_tenant_id"`
	PosOrgID     uuid.UUID      `json:"pos_org_id"`
	PosLevelID   uuid.UUID      `json:"pos_level_id"`
	PosCode      string         `json:"pos_code"`
	PosName      string         `json:"pos_name"`
	PosDesc      sql.NullString `json:"pos_desc"`
	PosIsActive  bool           `json:"pos_is_active"`
	PosMinSalary float64        `json:"pos_min_salary"`
	PosMaxSalary float64        `json:"pos_max_salary"`
	PosMaxInc    int            `json:"pos_max_inc"`
	PosQual      []byte         `json:"pos_qual"`
	PosResp      []byte         `json:"pos_resp"`
	PosCreatedAt time.Time      `json:"pos_created_at"`
	PosUpdatedAt time.Time      `json:"pos_updated_at"`
	PosCreatedBy uuid.UUID      `json:"pos_created_by"`
	PosUpdatedBy uuid.UUID      `json:"pos_updated_by"`

	// Position Level fields
	PositionLevelID        uuid.UUID      `json:"level_id"`
	PositionLevelTenantID  uuid.UUID      `json:"level_tenant_id"`
	PositionLevelOrgID     uuid.UUID      `json:"level_org_id"`
	PositionLevelCode      string         `json:"level_code"`
	PositionLevelName      string         `json:"level_name"`
	PositionLevelDesc      sql.NullString `json:"level_desc"`
	PositionLevelOrder     int            `json:"level_order"`
	PositionLevelRank      int            `json:"level_rank"`
	PositionLevelIsActive  bool           `json:"level_is_active"`
	PositionLevelCreatedAt time.Time      `json:"level_created_at"`
	PositionLevelUpdatedAt time.Time      `json:"level_updated_at"`
	PositionLevelCreatedBy uuid.UUID      `json:"level_created_by"`
	PositionLevelUpdatedBy uuid.UUID      `json:"level_updated_by"`

	// Organization fields
	OrgID               uuid.UUID      `json:"org_id"`
	OrgTenantID         uuid.UUID      `json:"org_tenant_id"`
	OrgParentID         uuid.NullUUID  `json:"org_parent_id"`
	OrgDomainID         uuid.NullUUID  `json:"org_domain_id"`
	OrgLevelID          uuid.UUID      `json:"org_level_id"`
	OrgCode             sql.NullString `json:"org_code"`
	OrgName             string         `json:"org_name"`
	OrgDesc             sql.NullString `json:"org_desc"`
	OrgIsActive         bool           `json:"org_is_active"`
	OrgEstablishedAt    sql.NullTime   `json:"org_established_at"`
	OrgDissolvedAt      sql.NullTime   `json:"org_dissolved_at"`
	OrgEmail            sql.NullString `json:"org_email"`
	OrgPhone            sql.NullString `json:"org_phone"`
	OrgWebsite          sql.NullString `json:"org_website"`
	OrgAddress          sql.NullString `json:"org_address"`
	OrgVillageID        uuid.NullUUID  `json:"org_village_id"`
	OrgDistrictID       uuid.NullUUID  `json:"org_district_id"`
	OrgCityID           uuid.NullUUID  `json:"org_city_id"`
	OrgProvinceID       uuid.NullUUID  `json:"org_province_id"`
	OrgCountryID        uuid.NullUUID  `json:"org_country_id"`
	OrgPostalCode       sql.NullString `json:"org_postal_code"`
	OrgLatitude         sql.NullString `json:"org_latitude"`
	OrgLongitude        sql.NullString `json:"org_longitude"`
	OrgEmployeeCount    sql.NullInt32  `json:"org_employee_count"`
	OrgAnnualBudget     sql.NullString `json:"org_annual_budget"`
	OrgLegalEntityType  sql.NullString `json:"org_legal_entity_type"`
	OrgTaxID            sql.NullString `json:"org_tax_id"`
	OrgRegistrationNum  sql.NullString `json:"org_registration_num"`
	OrgRegistrationDate sql.NullTime   `json:"org_registration_date"`
	OrgCreatedAt        time.Time      `json:"org_created_at"`
	OrgUpdatedAt        time.Time      `json:"org_updated_at"`
	OrgCreatedBy        uuid.NullUUID  `json:"org_created_by"`
	OrgUpdatedBy        uuid.NullUUID  `json:"org_updated_by"`

	// Organization Domain fields
	DomainID        uuid.NullUUID  `json:"domain_id"`
	DomainTenantID  uuid.UUID      `json:"domain_tenant_id"`
	DomainCode      string         `json:"domain_code"`
	DomainName      string         `json:"domain_name"`
	DomainDesc      sql.NullString `json:"domain_desc"`
	DomainCreatedAt time.Time      `json:"domain_created_at"`
	DomainUpdatedAt time.Time      `json:"domain_updated_at"`
	DomainCreatedBy uuid.NullUUID  `json:"domain_created_by"`
	DomainUpdatedBy uuid.NullUUID  `json:"domain_updated_by"`

	// Organization Level fields
	OrganizationLevelID        uuid.NullUUID  `json:"type_id"`
	OrganizationLevelTenantID  uuid.UUID      `json:"type_tenant_id"`
	OrganizationLevelCode      string         `json:"type_code"`
	OrganizationLevelName      string         `json:"type_name"`
	OrganizationLevelDesc      sql.NullString `json:"type_desc"`
	OrganizationLevelCreatedAt time.Time      `json:"type_created_at"`
	OrganizationLevelUpdatedAt time.Time      `json:"type_updated_at"`
	OrganizationLevelCreatedBy uuid.NullUUID  `json:"type_created_by"`
	OrganizationLevelUpdatedBy uuid.NullUUID  `json:"type_updated_by"`
}

// RefFileRow represents a file row from database queries
type RefFileRow struct {
	ID             uuid.UUID     `json:"id"`
	TenantID       uuid.UUID     `json:"tenant_id"`
	Bucket         string        `json:"bucket"`
	Name           string        `json:"name"`
	Path           string        `json:"path"`
	Size           int64         `json:"size"`
	Duration       int64         `json:"duration"`
	Height         int64         `json:"height"`
	Width          int64         `json:"width"`
	Metadata       string        `json:"metadata"`
	MimeType       string        `json:"mime_type"`
	Sha256Checksum string        `json:"sha256_checksum"`
	Sha512Checksum string        `json:"sha512_checksum"`
	CreatedAt      time.Time     `json:"created_at,omitempty"`
	CreatedByID    uuid.NullUUID `json:"created_by_id"`
	UpdatedAt      time.Time     `json:"updated_at,omitempty"`
	UpdatedByID    uuid.UUID     `json:"updated_by_id"`
}

// TransHasFileRow represents a trans_has_files row from database queries
type TransHasFileRow struct {
	ID          uuid.UUID     `json:"id"`
	TenantID    uuid.UUID     `json:"tenant_id"`
	FileID      uuid.UUID     `json:"file_id"`
	ModelType   string        `json:"model_type"`
	ModelID     uuid.UUID     `json:"model_id"`
	Feature     string        `json:"feature"`
	Code        string        `json:"code"`
	CreatedAt   time.Time     `json:"created_at,omitempty"`
	CreatedByID uuid.UUID     `json:"created_by_id"`
	UpdatedAt   time.Time     `json:"updated_at,omitempty"`
	UpdatedByID uuid.UUID     `json:"updated_by_id"`
	DeletedAt   sql.NullTime  `json:"deleted_at"`
	DeletedByID uuid.NullUUID `json:"deleted_by_id"`
}

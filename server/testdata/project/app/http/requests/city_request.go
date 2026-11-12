package requests

type CreateCityRequest struct {
	Name       string `json:"name" form:"name"`
	ProvinceID string `json:"province_id" form:"province_id"`
}

func (r *CreateCityRequest) Rules() map[string]any {
	return map[string]any{
		"name":        "required|string|alpha_dash_custom|max_len:255",
		"province_id": "required|uuid",
	}
}


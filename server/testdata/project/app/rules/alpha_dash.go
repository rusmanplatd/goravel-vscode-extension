package rules

type AlphaDash struct{}

func (r *AlphaDash) Signature() string {
	return "alpha_dash_custom"
}

func (r *AlphaDash) Message() string {
	return "The :attribute may only contain letters, numbers, dashes, and underscores."
}


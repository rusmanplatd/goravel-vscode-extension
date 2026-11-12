package config

import "github.com/rusmanplatd/basegoravel/app/facades"

func init() {
	config := facades.Config()
	config.Add("jwt", map[string]any{
		"secret": config.Env("JWT_SECRET", "default-secret"),
		"ttl":    config.Env("JWT_TTL", 60),
	})
}


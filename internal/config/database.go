package config

type DatabaseConfig struct {
	DSN string
}

func loadDatabase() DatabaseConfig {
	return DatabaseConfig{
		DSN: env("POSTGRES_DSN", "postgres://shitidc:shitidc_change_me@localhost:5432/shitidc?sslmode=disable"),
	}
}

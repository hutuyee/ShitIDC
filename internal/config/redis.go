package config

import "strconv"

type RedisConfig struct {
	Addr     string
	Password string
	DB       int
}

func loadRedis() RedisConfig {
	db, _ := strconv.Atoi(env("REDIS_DB", "0"))
	return RedisConfig{
		Addr:     env("REDIS_ADDR", "localhost:6379"),
		Password: env("REDIS_PASSWORD", ""),
		DB:       db,
	}
}

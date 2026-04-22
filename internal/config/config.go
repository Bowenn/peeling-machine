package config

import (
	"os"
	"path/filepath"
)

type Config struct {
	ProxyAddr  string
	APIAddr    string
	CADir      string
	BufferSize int
	BodyCap    int64
}

func Default() (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, err
	}
	return Config{
		ProxyAddr:  ":8080",
		APIAddr:    ":9090",
		CADir:      filepath.Join(home, ".peeling-machine"),
		BufferSize: 1000,
		BodyCap:    1 << 20,
	}, nil
}

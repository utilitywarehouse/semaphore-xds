package main

import (
	"encoding/json"
	"fmt"
)

type localClusterConfig struct {
	KubeConfigPath string `json:"kubeConfigPath"`
}

type remoteClusterConfig struct {
	KubeConfigPath string `json:"kubeConfigPath"`
	APIURL         string `json:"apiURL"`
	CAURL          string `json:"caURL"`
	SATokenPath    string `json:"saTokenPath"`
}

// Config holds the application configuration
type Config struct {
	Local   localClusterConfig     `json:"local"`
	Remotes []*remoteClusterConfig `json:"remotes"`
}

func parseConfig(rawConfig []byte) (*Config, error) {
	conf := &Config{}
	if err := json.Unmarshal(rawConfig, conf); err != nil {
		return nil, fmt.Errorf("error unmarshalling config: %v", err)
	}
	// Check for mandatory remote config.
	for _, r := range conf.Remotes {
		if (r.APIURL == "" || r.CAURL == "" || r.SATokenPath == "") && r.KubeConfigPath == "" {
			return nil, fmt.Errorf("Insufficient configuration to create remote cluster client. Set kubeConfigPath or apiURL and caURL and saTokenPath")
		}
	}
	return conf, nil
}

package main

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestConfig(t *testing.T) {

	insufficientRemoteKubeConfigPath := []byte(`
{
  "remotes": [
    {
      "remoteCAURL": "remote_ca_url",
      "remoteAPIURL": "remote_api_url"
    }
  ]
}
`)
	_, err := parseConfig(insufficientRemoteKubeConfigPath)
	assert.Equal(t, fmt.Errorf("Insufficient configuration to create remote cluster client. Set kubeConfigPath or apiURL and caURL and saTokenPath"), err)

	rawFullConfig := []byte(`
{
  "local": {
    "kubeConfigPath": "/path/to/kube/config"
  },
  "remotes": [
    {
      "CAURL": "remote_ca_url",
      "APIURL": "remote_api_url",
      "SATokenPath": "/path/to/token"
    },
    {
      "name": "remote_cluster_2",
      "kubeConfigPath": "/path/to/kube/config"
    }
  ]
}
`)
	config, err := parseConfig(rawFullConfig)
	assert.Equal(t, nil, err)
	assert.Equal(t, "/path/to/kube/config", config.Local.KubeConfigPath)
	assert.Equal(t, 2, len(config.Remotes))
	assert.Equal(t, "remote_ca_url", config.Remotes[0].CAURL)
	assert.Equal(t, "remote_api_url", config.Remotes[0].APIURL)
	assert.Equal(t, "/path/to/token", config.Remotes[0].SATokenPath)
	assert.Equal(t, "", config.Remotes[0].KubeConfigPath)
	assert.Equal(t, "", config.Remotes[1].CAURL)
	assert.Equal(t, "", config.Remotes[1].APIURL)
	assert.Equal(t, "", config.Remotes[1].SATokenPath)
	assert.Equal(t, "/path/to/kube/config", config.Remotes[1].KubeConfigPath)
}

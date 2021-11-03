// Copyright (c) 2015-2021 MinIO, Inc.
//
// This file is part of MinIO Object Storage stack
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package subnet

import (
	"github.com/minio/minio/internal/config"
	"github.com/minio/minio/internal/logger"
	"github.com/minio/pkg/env"
	"github.com/minio/pkg/licverifier"
)

var (
	// DefaultKVS - default KV config for subnet settings
	DefaultKVS = config.KVS{
		config.KV{
			Key:   config.License,
			Value: "",
		},
	}

	// HelpLicense - provides help for license config
	HelpLicense = config.HelpKVS{
		config.HelpKV{
			Key:         config.License,
			Type:        "string",
			Description: "Subnet license token for the cluster",
			Optional:    true,
		},
	}
)

// Config represents the subnet related configuration
type Config struct {
	// The subnet license token
	License string `json:"license"`
}

// LookupConfig - lookup config and override with valid environment settings if any.
func LookupConfig(kvs config.KVS, depId string, devMode bool) (cfg Config, err error) {
	if err = config.CheckValidKeys(config.SubnetSubSys, kvs, DefaultKVS); err != nil {
		return cfg, err
	}

	cfg.License = env.Get(config.EnvMinIOSubnetLicense, kvs.Get(config.License))

	if len(cfg.License) > 0 {
		_, err = licverifier.VerifyClusterLicense(cfg.License, depId, devMode)
		if err != nil {
			logger.Info("Inside LookupConfig. Error = %s", err.Error())
		}
	}
	return cfg, err
}

// Update - updates opts with nopts
func (opts *Config) Update(nopts Config) {
	DefaultKVS.Set("license", nopts.License)
	opts.License = nopts.License
	logger.Info("Inside subnet.Update. new license = %s, DefaultKVS= %v", nopts.License, DefaultKVS)
}

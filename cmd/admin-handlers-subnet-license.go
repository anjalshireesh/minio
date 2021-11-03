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

package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/minio/madmin-go"
	"github.com/minio/minio/internal/config"
	"github.com/minio/minio/internal/logger"
	iampolicy "github.com/minio/pkg/iam/policy"
	"github.com/minio/pkg/licverifier"
)

// GetSubnetLicenseHandler - DELETE /minio/admin/v3/del-config-kv
func (a adminAPIHandlers) GetSubnetLicenseHandler(w http.ResponseWriter, r *http.Request) {
	ctx := newContext(r, w, "GetConfigKV")

	defer logger.AuditLog(ctx, w, r, mustGetClaimsFromToken(r))

	objectAPI, cred := validateAdminReq(ctx, w, r, iampolicy.GetSubnetLicenseAdminAction)
	if objectAPI == nil {
		return
	}

	cfg := globalServerConfig.Clone()
	var buf = &bytes.Buffer{}
	cw := config.NewConfigWriteTo(cfg, config.SubnetSubSys)
	if _, err := cw.WriteTo(buf); err != nil {
		fmt.Println("Error in getting config: ", err.Error())
		writeErrorResponseJSON(ctx, w, toAdminAPIErr(ctx, err), r.URL)
		return
	}

	lic, err := licverifier.VerifyClusterLicense(string(buf.Bytes()), globalDeploymentID, true)
	if err != nil {
		writeErrorResponseJSON(ctx, w, toAdminAPIErr(ctx, err), r.URL)
		return
	}

	data, err := json.Marshal(lic)
	if err != nil {
		writeErrorResponseJSON(ctx, w, toAdminAPIErr(ctx, err), r.URL)
		return
	}

	password := cred.SecretKey
	econfigData, err := madmin.EncryptData(password, data)
	if err != nil {
		writeErrorResponseJSON(ctx, w, toAdminAPIErr(ctx, err), r.URL)
		return
	}

	writeSuccessResponseJSON(w, econfigData)
}

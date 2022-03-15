// Copyright (c) 2015-2022 MinIO, Inc.
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
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/minio/madmin-go"
	"github.com/minio/madmin-go/support"
	"github.com/minio/minio-go/v7/pkg/set"
	"github.com/minio/minio/internal/logger"
	iampolicy "github.com/minio/pkg/iam/policy"
	gcpu "github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
)

// SupportMetricsHandler - GET /minio/admin/v3/supportmetrics
// ----------
// Get support metrics
func (a adminAPIHandlers) SupportMetricsHandler(w http.ResponseWriter, r *http.Request) {
	ctx := newContext(r, w, "SupportMetrics")
	defer logger.AuditLog(ctx, w, r, mustGetClaimsFromToken(r))

	// Validate request signature.
	_, adminAPIErr := checkAdminRequestAuth(ctx, r, iampolicy.ServerInfoAdminAction, "")
	if adminAPIErr != ErrNone {
		writeErrorResponseJSON(ctx, w, errorCodes.ToAPIErr(adminAPIErr), r.URL)
		return
	}

	m := GetSupportMetrics(ctx)

	// Marshal API response
	jsonBytes, err := json.Marshal(m)
	if err != nil {
		writeErrorResponseJSON(ctx, w, toAdminAPIErr(ctx, err), r.URL)
		return
	}

	// Reply with storage information (across nodes in a
	// distributed setup) as json.
	writeSuccessResponseJSON(w, jsonBytes)
}

func GetSupportMetrics(ctx context.Context) support.Metrics {
	state, _ := getBackgroundHealStatus(ctx, newObjectLayerFn())

	iostats := []support.IOStats{}
	iostat := GetIOStats(ctx, globalLocalNodeName)
	iostats = append(iostats, iostat)
	peerIOStats := globalNotificationSys.GetIOStats(ctx)
	for _, iostat = range peerIOStats {
		iostats = append(iostats, iostat)
	}

	return support.Metrics{
		Version:     madmin.SupportMetricsVersion,
		TimeStamp:   time.Now(),
		BgHealState: state,
		IOStats:     iostats,
		TLSInfo:     getTLSInfo(),
	}
}

func GetIOStats(ctx context.Context, addr string) support.IOStats {
	iostat := support.IOStats{
		NodeCommon: madmin.NodeCommon{Addr: addr},
	}

	parts, _ := disk.Partitions(false)

	mountPointToDevice := map[string]string{}
	for _, part := range parts {
		if !strings.HasPrefix(part.Device, "/dev/loop") {
			dev := strings.ReplaceAll(part.Device, "/dev/", "")
			mountPointToDevice[part.Mountpoint] = dev
		}
	}

	devices := set.NewStringSet()
	for _, drive := range globalLocalDrives {
		ep := drive.Endpoint().Path
		for mp, d := range mountPointToDevice {
			if strings.HasPrefix(ep, mp) {
				devices.Add(d)
			}
		}
	}

	stats := []support.IOStat{}
	ioStats, _ := disk.IOCounters()

	for _, ioStat := range ioStats {
		if !devices.Contains(ioStat.Name) {
			continue
		}

		// TODO: Handle error
		uptm, _ := host.Uptime()
		upt := float64(uptm)

		// disk
		mib := float64(2 << 19)
		readsPerSec := float64(ioStat.ReadCount) / upt
		writesPerSec := float64(ioStat.WriteCount) / upt
		readMBPerSec := float64(ioStat.ReadBytes) / upt / mib
		writeMBPerSec := float64(ioStat.WriteBytes) / upt / mib
		util := float64(ioStat.IoTime) / (upt * 10)

		// cpu
		// TODO: Handle error
		times, _ := gcpu.Times(false)
		t := times[0]
		percIoWait := t.Iowait * 100 / t.Total()
		percUser := t.User * 100 / t.Total()

		stat := support.IOStat{
			DriveName:  ioStat.Name,
			ReadsPS:    readsPerSec,
			WritesPS:   writesPerSec,
			ReadsMBPS:  readMBPerSec,
			WritesMBPS: writeMBPerSec,
			PercUtil:   util,
			PercUser:   percUser,
			PercIOWait: percIoWait,
		}
		stats = append(stats, stat)
	}
	iostat.Stats = stats

	return iostat
}
